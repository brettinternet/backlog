package tui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/textutil"
)

type GoToItem = engine.Row

type launcherRow struct {
	label, keywords, reason string
	group                   string // help section
	actionRef               string
	change                  *hooks.LinkedChange // the change a configured action runs on
	action                  action
	item                    *GoToItem
	resume                  *engine.ResumeChoice
}
type launcherState struct {
	open, help, goTo, loading bool
	item                      bool // the ! menu: only the selected item's actions
	hidden                    bool // waiting briefly for the item's choices
	query                     lineInput
	err                       string
	cursor                    int
	rows, all                 []launcherRow
	base                      []launcherRow // all without the item's choices
}
type launcherFiltered struct {
	serial uint64
	rows   []launcherRow
}
type goToRequest struct {
	serial  uint64
	query   string
	catalog uint64
	pulls   *engine.Catalog // finds the items showing a pull request query
}
type goToResult struct {
	serial uint64
	rows   []launcherRow
	err    error
}

// One bounded latest-wins worker retains the cache-backed ID/title catalog for
// an open go-to session. Keystrokes never load sources or scan items on Update.
type goToWorker struct {
	ctx      context.Context
	load     func(context.Context) ([]GoToItem, error)
	once     sync.Once
	requests chan goToRequest
	results  chan goToResult
}

func newGoToWorker(ctx context.Context, load func(context.Context) ([]GoToItem, error)) *goToWorker {
	return &goToWorker{ctx: ctx, load: load, requests: make(chan goToRequest, 1), results: make(chan goToResult, 1)}
}
func (w *goToWorker) submit(r goToRequest) {
	w.once.Do(func() { go w.run() })
	select {
	case <-w.requests:
	default:
	}
	select {
	case w.requests <- r:
	case <-w.ctx.Done():
	}
}
func (w *goToWorker) run() {
	var items []GoToItem
	var loadErr error
	var catalog uint64
	for {
		select {
		case <-w.ctx.Done():
			return
		case r := <-w.requests:
			if r.catalog != catalog || items == nil {
				items, loadErr = w.load(w.ctx)
				catalog = r.catalog
			}
			result := goToResult{serial: r.serial, err: loadErr}
			if loadErr == nil {
				result.rows = searchGoTo(w.ctx, items, r.query)
			}
			if match, label, ok := pullQuery(r.query); ok {
				result.rows, result.err = showingPull(r.pulls, match, label, result.rows)
			}
			select {
			case <-w.results:
			default:
			}
			select {
			case w.results <- result:
			case <-w.ctx.Done():
				return
			}
		}
	}
}
func searchGoTo(ctx context.Context, items []GoToItem, text string) []launcherRow {
	type match struct{ index, score int }
	// Retain only the best 100 matches: memory and sort cost are bounded even
	// for an empty query over 50k items. Equal scores retain source order.
	matches := make([]match, 0, 100)
	for i := range items {
		if i%256 == 0 && ctx.Err() != nil {
			return nil
		}
		score, ok := pickerScore(text, items[i].Ref.ID+" "+items[i].Title)
		if !ok {
			continue
		}
		at := sort.Search(len(matches), func(j int) bool { return matches[j].score > score })
		if at >= 100 {
			continue
		}
		matches = append(matches, match{})
		copy(matches[at+1:], matches[at:])
		matches[at] = match{i, score}
		if len(matches) > 100 {
			matches = matches[:100]
		}
	}
	rows := make([]launcherRow, 0, len(matches))
	for _, m := range matches {
		item := items[m.index]
		rows = append(rows, launcherRow{label: textutil.Line(item.Ref.Source + ":" + item.Ref.ID + "  " + item.Title), item: &item})
	}
	return rows
}

// pullQuery reads #N or a pull or merge request URL as a match for the
// changes that are that pull request.
func pullQuery(text string) (func(changes.Change) bool, string, bool) {
	text = strings.TrimSpace(text)
	if n, err := strconv.Atoi(strings.TrimPrefix(text, "#")); err == nil && n > 0 && strings.HasPrefix(text, "#") {
		return func(c changes.Change) bool { return c.Number == n }, text, true
	}
	key := changes.PullKey(text)
	if key == "" {
		return nil, "", false
	}
	return func(c changes.Change) bool { return c.Number > 0 && changes.PullKey(c.URL) == key }, text, true
}

// showingPull puts the items showing a pull request before the other matches,
// or says that no item shows it.
func showingPull(catalog *engine.Catalog, match func(changes.Change) bool, label string, rows []launcherRow) ([]launcherRow, error) {
	if catalog == nil {
		return rows, nil
	}
	shown := catalog.Showing(match)
	if len(shown) == 0 {
		return rows, fmt.Errorf("no item shows pull request %s", label)
	}
	out := make([]launcherRow, 0, len(shown)+len(rows))
	for i := range shown {
		item := shown[i]
		out = append(out, launcherRow{label: textutil.Line(item.Ref.Source + ":" + item.Ref.ID + "  " + item.Title), item: &item})
	}
	for _, row := range rows {
		if !slices.ContainsFunc(shown, func(s engine.Row) bool { return s.Ref == row.item.Ref }) {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m Model) waitGoTo() tea.Cmd {
	w := m.goToWorker
	return func() tea.Msg {
		select {
		case r := <-w.results:
			return r
		case <-w.ctx.Done():
			return nil
		}
	}
}
func (m Model) openGoTo() (tea.Model, tea.Cmd) {
	m.keyPrefix = ""
	m.count = 0
	m.inputSerial++
	m.launcherSerial++
	m.launcher = launcherState{open: true, goTo: true, loading: true}
	m.goToSession++
	m.goToWorker.submit(goToRequest{serial: m.launcherSerial, catalog: m.goToSession, pulls: m.catalog})
	return m, nil
}

// branchItemMsg is the item the checkout's branch names, opened at startup.
type branchItemMsg GoToItem

// openDetached shows item's detail outside the list, as go-to does.
func (m *Model) openDetached(item GoToItem) tea.Cmd {
	m.detached, m.heldPlace = &item, 0
	m.detailHistory = nil
	m.detailOpen = true
	m.detailFocus = true
	return m.scheduleDetail()
}
func (m Model) receiveGoTo(r goToResult) (tea.Model, tea.Cmd) {
	if m.launcher.open && m.launcher.goTo && r.serial == m.launcherSerial {
		m.launcher.rows, m.launcher.cursor, m.launcher.loading = r.rows, 0, false
		if r.err != nil {
			m.launcher.err = textutil.Line(r.err.Error())
		}
	}
	return m, m.waitGoTo()
}
func (m Model) openActions(help bool) (tea.Model, tea.Cmd) {
	m.keyPrefix = ""
	m.actionSession, m.actionChange = "", nil
	m.count = 0
	m.inputSerial++
	var rows, rest []launcherRow
	_, selected := m.selectedRow()
	group := ""
	for _, a := range m.keymap {
		if a.group != "" {
			group = a.group
		}
		if strings.HasPrefix(a.id, "action:") {
			continue
		}
		row := m.actionRow(a)
		row.group = group
		// The palette lists the selected item's actions first; help keeps
		// catalog order.
		if !help && selected && itemAction(a.id) {
			rows = append(rows, row)
		} else {
			rest = append(rest, row)
		}
	}
	rows = append(rows, rest...)
	if help {
		for _, g := range changeLegend {
			rows = append(rows, launcherRow{label: g[1], keywords: "change column glyph legend", group: "Change column", action: action{label: g[1], bindings: []string{g[0]}}})
		}
	}
	m.launcher = launcherState{open: true, help: help, rows: rows, all: rows}
	// Configured actions and resumable sessions both come from the selected item.
	if _, selected := m.selectedRow(); m.opts.Automation != nil && (len(m.opts.Automation.Actions) > 0 || selected) {
		m.launcherSerial++
		return m.loadItemChoices()
	}
	return m, nil
}

// changeLegend is help's key to the change column's glyphs: the stage, then
// the verdict or checks, by what to fix first (design.md D46).
var changeLegend = [][2]string{
	{"◇", "Worktree"}, {"⎇", "Branch"}, {"⇡", "Pull request"}, {"◆", "Landed"}, {"?", "Unknown or stale"},
	{"⚠", "Conflicts"}, {"✕", "Checks failing"}, {"✎", "Changes requested"}, {"↓", "Behind base"},
	{"●", "Checks running"}, {"○", "Review required"}, {"⊘", "Blocked"},
	{"✓", "Ready, or checks passing"},
}

func (m Model) actionRow(a action) launcherRow {
	return launcherRow{label: a.label, keywords: a.id + " " + strings.Join(a.bindings, " ") + " " + searchAliases[a.id], reason: m.disabledReason(a), action: a}
}

// itemMenuActions are the built-in actions the ! menu offers, in menu order.
// It shows only those that apply to the item now, so it stays short; the
// palette has the rest.
var itemMenuActions = []string{"status", "start", "done", "priority", "assign-me", "assignees", "labels", "title", "comment", "claim", "yank-id", "yank-branch", "open-url", "archive"}

// itemMenuRows are the ! menu's built-in rows for the selected item.
func (m Model) itemMenuRows() []launcherRow {
	item, ok := m.selectedRow()
	if !ok {
		return nil
	}
	me := m.opts.Me[item.Ref.Source]
	mine := me != "" && slices.Contains(item.Assignees, me)
	offered := map[string]bool{
		"start":     item.Category == model.Backlog || item.Category == model.Todo,
		"done":      item.Category == model.Active,
		"assign-me": me != "" && !mine,
		"assignees": me == "" || mine,
	}
	var rows []launcherRow
	for _, id := range itemMenuActions {
		if shown, ok := offered[id]; ok && !shown {
			continue
		}
		for _, a := range m.keymap {
			if a.id == id {
				if row := m.actionRow(a); row.reason == "" {
					rows = append(rows, row)
				}
				break
			}
		}
	}
	return rows
}

func (m Model) filterLauncher() tea.Cmd {
	rows, query, serial, ordered := m.launcher.all, m.launcher.query.text, m.launcherSerial, m.launcher.help
	return func() tea.Msg {
		type scored struct {
			row   launcherRow
			score int
		}
		var found []scored
		for _, r := range rows {
			score, ok := pickerScore(query, r.label+" "+r.keywords)
			if ok {
				found = append(found, scored{r, score})
			}
		}
		// Help keeps catalog order so matches stay in their sections.
		if !ordered {
			sort.SliceStable(found, func(i, j int) bool { return found[i].score < found[j].score })
		}
		result := make([]launcherRow, 0, len(found))
		for _, f := range found {
			result = append(result, f.row)
		}
		return launcherFiltered{serial, result}
	}
}

// launcherLast bounds the cursor: the palette's last row, or the help page's
// last scroll offset at the current size.
func (m Model) launcherLast() int {
	if m.launcher.help {
		_, _, limit := helpLayout(m.launcher.rows, m.width)
		return helpScrollMax(limit, m.helpRows())
	}
	return max(0, len(m.launcher.rows)-1)
}

func (m Model) updateLauncher(key, text string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+c":
		m.launcher = launcherState{}
		m.launcherSerial++
		return m, nil
	case "down", "tab":
		m.launcher.cursor = min(m.launcherLast(), m.launcher.cursor+1)
		return m, nil
	case "up", "shift+tab":
		m.launcher.cursor = max(0, min(m.launcherLast(), m.launcher.cursor)-1)
		return m, nil
	case "pagedown":
		m.launcher.cursor = min(m.launcherLast(), m.launcher.cursor+10)
		return m, nil
	case "pageup":
		m.launcher.cursor = max(0, min(m.launcherLast(), m.launcher.cursor)-10)
		return m, nil
	case "enter":
		// Help's cursor is a scroll offset, not a row.
		if m.launcher.help || m.launcher.loading || len(m.launcher.rows) == 0 {
			return m, nil
		}
		row := m.launcher.rows[m.launcher.cursor]
		if row.reason != "" {
			m.launcher.err = row.reason
			return m, nil
		}
		m.launcher = launcherState{}
		m.launcherSerial++
		if row.item != nil {
			cmd := m.openDetached(*row.item)
			return m, cmd
		}
		if row.resume != nil {
			return m, m.runResume(*row.resume)
		}
		if row.actionRef != "" {
			return m.prepareConfiguredActionForID(strings.TrimPrefix(row.action.id, "action:"), row.actionRef, row.change)
		}
		return m.executeAction(row.action)
	default:
		if _, changed := m.launcher.query.edit(key, text); !changed {
			return m, nil
		}
	}
	m.launcherSerial++
	m.launcher.loading = true
	m.launcher.err = ""
	if m.launcher.goTo {
		m.goToWorker.submit(goToRequest{serial: m.launcherSerial, query: m.launcher.query.text, catalog: m.goToSession, pulls: m.catalog})
		return m, nil
	}
	return m, m.filterLauncher()
}
