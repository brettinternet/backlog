package tui

import (
	"cmp"
	"container/list"
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/history"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/brettinternet/backlog/internal/tour"
	"github.com/charmbracelet/x/ansi"
)

type detailTab uint8

const (
	tabOverview detailTab = iota
	tabCriteria
	tabNotes
	tabComments
	tabRelations
	tabChanges
	tabHistory
	// The change view's tabs replace the item's while it is open.
	tabChangeOverview
	tabChangeFiles
	tabChangeDiff
	tabChangeCommits
	tabChangeConversation
)

var (
	detailTabNames = [...]string{"Overview", "Criteria", "Notes", "Comments", "Relations", "Changes", "History"}
	changeTabNames = [...]string{"Overview", "Files", "Diff", "Commits", "Conversation"}
)

type detailIdentity struct {
	Ref              model.Ref
	Version          string
	ReadinessVersion string
	RelationVersion  string
	ChangesVersion   string
	Valid            bool
}

func identityFor(row engine.Row, ok bool) detailIdentity {
	if !ok {
		return detailIdentity{}
	}
	return detailIdentity{
		Ref: row.Ref, Version: row.Version,
		ReadinessVersion: row.ReadinessVersion, RelationVersion: row.RelationVersion,
		ChangesVersion: changesVersion(row.Changes), Valid: true,
	}
}

type detailRequest struct {
	generation uint64
	row        engine.Row
	width      int
	tab        detailTab
	expanded   bool
	open       string // history entries expanded, one ID per line
	milestones []project.Milestone
	sprints    []model.Sprint
	statuses   []project.StatusConfig
	theme      theme
	now        time.Time
	fresh      bool            // R: reread from the source, bypassing both caches
	change     *changes.Change // the change view's change
}
type detailResult struct {
	generation uint64
	identity   detailIdentity
	lines      []string
	entries    []int             // line index of each criterion or relation, for the tab's cursor
	targets    []engine.Relation // the relation each Relations entry opens
	changes    []changes.Change  // the change each Changes entry is
	commits    []changes.Commit  // the commit each change view Commits entry is
	ids        []string          // the history entry each History entry is, or each commit's SHA
	hints      []string          // a key to show on each History entry when selected
	files      []int             // the Diff tab's line of each file and hunk header
	hunks      []int
	rows       []diffRow // what each Diff entry shows
	head       string    // the commit the Diff shows
	steps      []int     // the Diff's line of each tour step, in tour order
	tour       string    // why the Diff shows no tour steps, when it has a tour
	dirty      []string  // the change's uncommitted files, which aren't at head
	review     *changes.Review
	draft      *changes.Draft // the pull request's pending review
	draftErr   string         // why the draft can't be read
	truncated  bool
	err        string
}

// tabCounts label detail tabs from the row, such as "2/5" criteria, so the
// tab bar is complete before the item is read.
func tabCounts(row engine.Row) [len(detailTabNames)]string {
	var c [len(detailTabNames)]string
	if row.CriteriaTotal > 0 {
		c[tabCriteria] = strconv.Itoa(row.CriteriaDone) + "/" + strconv.Itoa(row.CriteriaTotal)
	}
	if len(row.Relations) > 0 {
		c[tabRelations] = strconv.Itoa(len(row.Relations))
	}
	if len(row.Changes) > 0 {
		c[tabChanges] = strconv.Itoa(len(row.Changes))
	}
	if row.Comments > 0 {
		c[tabComments] = strconv.Itoa(row.Comments)
	}
	return c
}

type detailResultMsg detailResult

type renderKey struct {
	Ref        model.Ref
	Version    string
	Width      int
	Theme      string
	Tab        detailTab
	Expanded   bool
	Open       string
	Dependency string
}
type itemKey struct {
	Ref     model.Ref
	Version string
}
type rendered struct {
	lines     []string
	entries   []int
	targets   []engine.Relation
	changes   []changes.Change
	ids       []string
	hints     []string // a key worth showing on each entry when it is selected
	files     []int    // the Diff tab's line of each file and hunk header
	hunks     []int
	rows      []diffRow // what each Diff entry, or each line of a file's diff, shows
	steps     []int     // the line of each tour step's callout, in tour order
	tour      string    // why a change's tour shows no steps
	truncated bool
	size      int
}
type renderedEntry struct {
	key   renderKey
	value rendered
}
type itemEntry struct {
	key  itemKey
	item model.Item
	size int
}
type detailCache struct {
	renders     map[renderKey]*list.Element
	renderOrder *list.List
	renderBytes int
	items       map[itemKey]*list.Element
	itemOrder   *list.List
	itemBytes   int
	maxEntries  int // renders kept, and their bytes
	maxBytes    int
}

const (
	maxRenderEntries = 24
	maxRenderBytes   = 1 << 20
	// The Diff tab keeps each file's render, so many small entries.
	maxDiffEntries = 4096
	maxDiffBytes   = 16 << 20
	maxDetailItems = 8
	maxDetailBytes = 1 << 20
	bodyLimit      = 32 << 10
	bodyMoreLimit  = 128 << 10
	renderLimit    = 128 << 10
)

func newDetailCache(maxEntries, maxBytes int) detailCache {
	return detailCache{
		renders: make(map[renderKey]*list.Element), renderOrder: list.New(),
		items: make(map[itemKey]*list.Element), itemOrder: list.New(),
		maxEntries: maxEntries, maxBytes: maxBytes,
	}
}
func (cache *detailCache) render(key renderKey) (rendered, bool) {
	element := cache.renders[key]
	if element == nil {
		return rendered{}, false
	}
	cache.renderOrder.MoveToBack(element)
	return element.Value.(renderedEntry).value, true
}
func (cache *detailCache) putRender(key renderKey, value rendered) {
	if element := cache.renders[key]; element != nil {
		cache.renderBytes -= element.Value.(renderedEntry).value.size
		cache.renderOrder.Remove(element)
		delete(cache.renders, key)
	}
	if value.size > cache.maxBytes {
		return
	}
	element := cache.renderOrder.PushBack(renderedEntry{key, value})
	cache.renders[key] = element
	cache.renderBytes += value.size
	for cache.renderOrder.Len() > cache.maxEntries || cache.renderBytes > cache.maxBytes {
		oldest := cache.renderOrder.Front()
		entry := oldest.Value.(renderedEntry)
		cache.renderBytes -= entry.value.size
		delete(cache.renders, entry.key)
		cache.renderOrder.Remove(oldest)
	}
}
func (cache *detailCache) item(key itemKey) (model.Item, bool) {
	element := cache.items[key]
	if element == nil {
		return model.Item{}, false
	}
	cache.itemOrder.MoveToBack(element)
	return element.Value.(itemEntry).item, true
}
func (cache *detailCache) putItem(key itemKey, item model.Item) {
	if element := cache.items[key]; element != nil {
		cache.itemBytes -= element.Value.(itemEntry).size
		cache.itemOrder.Remove(element)
		delete(cache.items, key)
	}
	size := itemSize(item)
	if size > maxDetailBytes {
		return
	}
	element := cache.itemOrder.PushBack(itemEntry{key, item, size})
	cache.items[key] = element
	cache.itemBytes += size
	for cache.itemOrder.Len() > maxDetailItems || cache.itemBytes > maxDetailBytes {
		oldest := cache.itemOrder.Front()
		entry := oldest.Value.(itemEntry)
		cache.itemBytes -= entry.size
		delete(cache.items, entry.key)
		cache.itemOrder.Remove(oldest)
	}
}
func itemSize(item model.Item) int {
	if len(item.Fields) > 0 {
		return maxDetailBytes + 1
	}
	size := len(item.Ref.Source) + len(item.Ref.ID) + len(item.Title) + len(item.Type) + len(item.Status)
	size += len(item.Rank) + len(item.Milestone) + len(item.Due) + len(item.URL)
	size += len(item.Description) + len(item.Plan) + len(item.Notes) + len(item.Summary)
	for _, values := range [][]string{item.Assignees, item.Labels, item.Refs, item.Files} {
		for _, value := range values {
			size += len(value) + 16
		}
	}
	for _, values := range [][]model.Ref{item.Children, item.BlockedBy, item.Blocks, item.Related} {
		for _, value := range values {
			size += len(value.Source) + len(value.ID) + 32
		}
	}
	for _, values := range [][]model.Criterion{item.Criteria, item.DoD} {
		for _, value := range values {
			size += len(value.Text) + len(value.Evidence) + 32
		}
	}
	for _, comment := range item.Comments {
		size += len(comment.Author) + len(comment.Body) + 32
	}
	if item.Parent != nil {
		size += len(item.Parent.Source) + len(item.Parent.ID) + 32
	}
	if item.DuplicateOf != nil {
		size += len(item.DuplicateOf.Source) + len(item.DuplicateOf.ID) + 32
	}
	for _, edge := range item.Edges {
		size += len(edge.Target.Source) + len(edge.Target.ID) + len(edge.Kind) + 32
	}
	return size
}

type historyCache struct {
	key    renderKey
	result history.Result
}

type changeCache struct {
	key    string
	detail changes.Detail
}

type detailWorker struct {
	ctx      context.Context
	read     func(context.Context, model.Ref) (model.Item, error)
	render   func(body string, width int, theme string) (string, error)
	history  func(context.Context, model.Item, []changes.Change) history.Result
	position func(model.Ref) string // the event log's position, so new events redraw History
	recent   historyCache
	// changeDetail reads a change's files and commits, with their patches when
	// asked; recentChange keeps the last that a worktree's edits can't change.
	changeDetail func(context.Context, changes.Change, bool) (changes.Detail, error)
	recentChange changeCache
	// changeReview reads a pull request's review, which its forge caches;
	// enrich adds Git's and the forges' facts to the Changes tab.
	changeReview func(context.Context, changes.Change, bool) (changes.Review, error)
	changeDraft  func(changes.Change) (changes.Draft, error)
	enrich       func(context.Context, []changes.Change, bool) []changes.Change
	// tour reads the tour kept for an item's change, and fileAt a file a
	// step outside every hunk shows.
	tour     func(model.Ref, string) (tour.Stored, bool, error)
	fileAt   func(context.Context, changes.Change, string) ([]byte, error)
	requests chan detailRequest
	results  chan detailResult
	mu       sync.Mutex
	active   context.CancelFunc
	started  sync.Once
	cache    detailCache
	diffs    detailCache // each file of the Diff tab
}

func newDetailWorker(ctx context.Context, read func(context.Context, model.Ref) (model.Item, error), render func(string, int, string) (string, error)) *detailWorker {
	return &detailWorker{
		ctx: ctx, read: read, render: render, fileAt: changes.FileAt,
		requests: make(chan detailRequest, 1), results: make(chan detailResult, 1), cache: newDetailCache(maxRenderEntries, maxRenderBytes), diffs: newDetailCache(maxDiffEntries, maxDiffBytes),
	}
}
func (worker *detailWorker) start() {
	worker.started.Do(func() { go worker.run() })
}
func (worker *detailWorker) submit(request detailRequest) {
	worker.start()
	worker.mu.Lock()
	if worker.active != nil {
		worker.active()
	}
	select {
	case worker.requests <- request:
	default:
		select {
		case <-worker.requests:
		default:
		}
		select {
		case worker.requests <- request:
		default:
		}
	}
	worker.mu.Unlock()
}
func (worker *detailWorker) run() {
	for {
		select {
		case <-worker.ctx.Done():
			return
		case request := <-worker.requests:
			ctx, cancel := context.WithCancel(worker.ctx)
			worker.mu.Lock()
			worker.active = cancel
			worker.mu.Unlock()
			result := worker.prepare(ctx, request)
			cancel()
			worker.mu.Lock()
			worker.active = nil
			worker.mu.Unlock()
			if worker.ctx.Err() == nil {
				latestDetail(worker.results, result)
			}
		}
	}
}
func latestDetail(ch chan detailResult, value detailResult) {
	select {
	case ch <- value:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- value:
		default:
		}
	}
}
func (worker *detailWorker) prepare(ctx context.Context, request detailRequest) detailResult {
	identity := identityFor(request.row, true)
	result := detailResult{generation: request.generation, identity: identity}
	if request.change != nil {
		diff := request.tab == tabChangeDiff
		detail, err := worker.loadChange(ctx, *request.change, request.fresh, diff)
		if err := ctx.Err(); err != nil {
			result.err = textutil.Line(err.Error())
			return result
		}
		if err != nil {
			// The change's own facts still show, with why its files don't,
			// and a commit's message as the forge sent it.
			detail = changes.Detail{Reason: err.Error()}
			if request.change.IsCommit() {
				detail.Commits = request.change.Commits
			}
		}
		// Every tab reads the review and its draft, which gate c and r.
		var review *changes.Review
		var draft *changes.Draft
		if request.change.Number > 0 {
			r, err := worker.loadReview(ctx, *request.change, request.fresh)
			if err != nil {
				r = changes.Review{Stale: "pull request detail unavailable: " + err.Error()}
				r.Blocked = r.Stale
			}
			review = &r
			if worker.changeDraft != nil {
				d, err := worker.changeDraft(*request.change)
				if err != nil {
					result.draftErr = textutil.Line(err.Error())
				} else {
					draft = &d
				}
			}
		}
		var page rendered
		if diff && (err == nil || request.change.IsCommit()) {
			var view *tourView
			if err == nil {
				view = worker.loadTour(ctx, request, *request.change, &detail)
			}
			page = worker.diffPage(ctx, *request.change, detail, view, request)
		} else {
			page = changeViewPage(*request.change, detail, review, draft, result.draftErr, request, worker.render)
		}
		if err := ctx.Err(); err != nil {
			result.err = textutil.Line(err.Error())
			return result
		}
		result.lines, result.entries, result.ids = page.lines, page.entries, page.ids
		result.files, result.hunks, result.truncated = page.files, page.hunks, page.truncated
		result.rows, result.head = page.rows, request.change.Head
		result.steps, result.tour = page.steps, page.tour
		if request.tab == tabChangeCommits {
			result.commits = detail.Commits
		}
		result.review, result.draft = review, draft
		for _, f := range detail.Files {
			if f.Status == changes.FileUncommitted {
				result.dirty = append(result.dirty, f.Path)
			}
		}
		return result
	}
	if request.tab == tabChanges {
		// Git counts ahead, behind, and dirty files each time, so the tab
		// isn't cached; it needs only the row.
		enrich := worker.enrich
		if enrich == nil {
			enrich = func(ctx context.Context, cs []changes.Change, fresh bool) []changes.Change {
				return changes.Enrich(ctx, cs, nil, fresh)
			}
		}
		page := changeLines(enrich(ctx, request.row.Changes, request.fresh), request.theme, cmp.Or(request.now, time.Now()), max(1, request.width))
		if err := ctx.Err(); err != nil {
			result.err = textutil.Line(err.Error())
			return result
		}
		result.lines, result.entries, result.changes = page.lines, page.entries, page.changes
		return result
	}
	version := request.row.Version
	dependency := detailDependency(request.row, request.tab)
	if request.tab == tabHistory && worker.position != nil {
		dependency = worker.position(request.row.Ref) + " " + dependency
	}
	key := renderKey{Ref: request.row.Ref, Version: version, Width: request.width, Theme: request.theme.key(), Tab: request.tab, Expanded: request.expanded, Dependency: dependency}
	if request.tab == tabHistory {
		key.Open = request.open
	}
	if value, ok := worker.cache.render(key); ok && !request.fresh {
		result.lines, result.entries, result.targets, result.ids, result.hints, result.truncated = value.lines, value.entries, value.targets, value.ids, value.hints, value.truncated
		return result
	}
	item, ok := worker.cache.item(itemKey{Ref: request.row.Ref, Version: version})
	if !ok || request.fresh {
		if worker.read == nil {
			result.err = "detail reading is unavailable"
			return result
		}
		var err error
		item, err = worker.read(ctx, request.row.Ref)
		if err != nil {
			result.err = textutil.Line(err.Error())
			return result
		}
		if err := ctx.Err(); err != nil {
			result.err = textutil.Line(err.Error())
			return result
		}
		if item.Version == "" {
			item.Version = version
		}
		version = item.Version
		worker.cache.putItem(itemKey{Ref: request.row.Ref, Version: version}, item)
	}
	version = item.Version
	if version == "" {
		version = request.row.Version
	}
	key.Version = version
	if value, ok := worker.cache.render(key); ok && !request.fresh {
		result.identity.Version = version
		result.lines, result.entries, result.targets, result.ids, result.hints, result.truncated = value.lines, value.entries, value.targets, value.ids, value.hints, value.truncated
		return result
	}
	var page rendered
	var err error
	if request.tab == tabHistory {
		page = historyLines(worker.loadHistory(ctx, item, request.row.Changes, key, request.fresh), request, max(1, request.width))
	} else {
		page, err = detailPage(ctx, item, request, worker.render)
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		result.err = textutil.Line(err.Error())
		return result
	}
	if len(page.lines) == 0 {
		page.lines = []string{request.theme.muted.render("Nothing here yet.")}
	}
	result.identity.Version = version
	result.lines, result.entries, result.targets, result.ids, result.hints, result.truncated = page.lines, page.entries, page.targets, page.ids, page.hints, page.truncated
	worker.cache.putRender(key, page)
	return result
}

// loadHistory composes an item's history once per version and log position,
// so expanding a row or resizing only redraws it.
func (worker *detailWorker) loadHistory(ctx context.Context, item model.Item, linked []changes.Change, key renderKey, fresh bool) history.Result {
	key.Width, key.Theme, key.Expanded, key.Open = 0, "", false, ""
	if worker.recent.key == key && !fresh {
		return worker.recent.result
	}
	if worker.history == nil {
		return history.Result{Providers: []history.Status{{Provider: "history", State: history.Unavailable, Reason: "not configured"}}}
	}
	result := worker.history(ctx, item, linked)
	if ctx.Err() == nil {
		worker.recent = historyCache{key, result}
	}
	return result
}

// loadChange reads a change's files and commits each time, as the Changes
// tab does, except one without a worktree is kept until its head or update
// time moves, so a pull request isn't fetched again on every tab. The Diff
// tab asks for patches, which a kept detail without them can't serve.
func (worker *detailWorker) loadChange(ctx context.Context, c changes.Change, fresh, patch bool) (changes.Detail, error) {
	if worker.changeDetail == nil {
		return changes.Detail{}, errors.New("change detail is unavailable")
	}
	key := ""
	if c.Worktree == "" {
		key = strings.Join([]string{c.ID, c.Stage, c.State, c.Head, c.Updated.String()}, "\x00")
		if cached := worker.recentChange; cached.key == key && !fresh && (cached.detail.Patched || !patch) {
			return worker.recentChange.detail, nil
		}
	}
	detail, err := worker.changeDetail(ctx, c, patch)
	if err == nil && key != "" && ctx.Err() == nil {
		worker.recentChange = changeCache{key, detail}
	}
	return detail, err
}

// loadReview reads a pull request change's review; the forge keeps it for a
// minute per head.
func (worker *detailWorker) loadReview(ctx context.Context, c changes.Change, fresh bool) (changes.Review, error) {
	if worker.changeReview == nil {
		return changes.Review{}, errors.New("no forge is configured")
	}
	return worker.changeReview(ctx, c, fresh)
}

const (
	historyLimit     = 200
	historyMoreLimit = 2000
)

// historyLines draws non-ok providers first, then one row per entry, newest
// last: time, source label, summary, then member count and actor. Rows that
// head a group, carry detail, or are cut off expand in place; any group kind
// renders the same way.
func historyLines(r history.Result, request detailRequest, width int) rendered {
	t := request.theme
	open := map[string]bool{}
	for id := range strings.SplitSeq(request.open, "\n") {
		open[id] = id != ""
	}
	var page rendered
	for _, s := range r.Providers {
		if s.State != history.OK {
			page.lines = append(page.lines, t.muted.render(ansi.Truncate(textutil.Line(s.Provider+": "+s.State+" ("+s.Reason+")"), width, "…")))
		}
	}
	members := map[string]int{}
	var shown []history.Entry
	for _, e := range r.Entries {
		if e.Group != "" {
			members[e.Group]++
		}
		if e.Group == "" || open[e.Group] {
			shown = append(shown, e)
		}
	}
	if len(shown) == 0 {
		page.lines = append(page.lines, t.muted.render("No history."))
	}
	limit := historyLimit
	if request.expanded {
		limit = historyMoreLimit
	}
	if len(shown) > limit {
		page.lines = append(page.lines, t.muted.render("… "+strconv.Itoa(len(shown)-limit)+" earlier entries"))
		shown, page.truncated = shown[len(shown)-limit:], true
	}
	now := cmp.Or(request.now, time.Now())
	// The label column goes first when the pane is too narrow for it.
	labels := width >= 48
	for _, e := range shown {
		indent := ""
		if e.Group != "" {
			indent = "  "
		}
		stamp := t.muted.render(historyTime(e.Time, now)) + "  "
		if labels {
			label := history.Label(e)
			stamp += historyLabel(t, e).render(label) + blank(history.LabelWidth-ansi.StringWidth(label)) + "  "
		}
		tail := ""
		if members[e.ID] > 0 {
			tail = "  " + strconv.Itoa(members[e.ID])
		}
		if e.Actor != "" && e.Span == nil {
			tail += "  " + textutil.Line(e.Actor)
		}
		hint := ""
		if e.Resumable {
			hint = "! resume"
		}
		column := len(indent) + 2 + ansi.StringWidth(ansi.Strip(stamp))
		// A row with a hint keeps room for it, so selecting it hides nothing.
		room := max(1, width-column-ansi.StringWidth(tail)-hintWidth(hint))
		summary := textutil.Line(e.Summary)
		cut := ansi.StringWidth(summary) > room
		marker := " "
		if members[e.ID] > 0 || e.Detail != "" || cut {
			marker = "▸"
			if open[e.ID] {
				marker = "▾"
			}
		}
		// An open row shows its whole summary, wrapped under its first line.
		parts := []string{ansi.Truncate(summary, room, "…")}
		if open[e.ID] && cut {
			parts = strings.Split(ansi.Wrap(summary, room, ""), "\n")
		}
		page.entries, page.ids, page.hints = append(page.entries, len(page.lines)), append(page.ids, e.ID), append(page.hints, hint)
		for i, part := range parts {
			if e.Span != nil {
				part = t.bold.render(part)
			}
			if i == 0 {
				page.lines = append(page.lines, indent+t.faint.render(marker)+" "+stamp+part+t.muted.render(tail))
			} else {
				page.lines = append(page.lines, blank(column)+part)
			}
		}
		if open[e.ID] && e.Detail != "" {
			// Narrow panes give the detail the width the columns would take.
			if width-column < 32 {
				column = len(indent) + 4
			}
			page.lines = append(page.lines, historyDetail(t, e, column, width)...)
		}
	}
	for _, line := range page.lines {
		page.size += len(line) + 1
	}
	return page
}

// hintWidth is the room a row's key hint takes at its right end.
func hintWidth(hint string) int {
	if hint == "" {
		return 0
	}
	return ansi.StringWidth(hint) + 2
}

// historyTime is a timestamp in 12 cells, like ls -l: the time for the last
// six months, the year before that.
func historyTime(at, now time.Time) string {
	if at.IsZero() {
		return blank(12)
	}
	at = at.Local()
	if now.Sub(at) < 182*24*time.Hour && at.Before(now.Add(time.Hour)) {
		return at.Format("Jan _2 15:04")
	}
	return at.Format("Jan _2  2006")
}

// historyLabel colors the sources a reader looks for: agents, commits,
// comments, and remote activity. Edits and claims, the bulk, stay muted.
func historyLabel(t theme, e history.Entry) paint {
	switch {
	case e.Kind == "session" || e.Kind == "session.handoff":
		return t.accent
	case e.Kind == "commit":
		return t.yellow
	case e.Kind == "comment":
		return t.magenta
	case e.Kind == "activity":
		return t.cyan
	}
	return t.muted
}

// fieldLine matches the key of a "key: value" detail line, whose value hangs
// under its first character.
var fieldLine = regexp.MustCompile(`^[a-z][a-z ]{0,15}: +`)

// historyDetail wraps an entry's detail from column, each line hanging under
// its own indent, marker, or field key. An edit's detail names each field on its own
// line, then marks its changes: "+ " added, "- " removed, "[x] " checked.
func historyDetail(t theme, e history.Entry, column, width int) []string {
	var out []string
	for raw := range strings.SplitSeq(textutil.Sanitize(e.Detail), "\n") {
		line := textutil.Line(raw)
		lead := len(line) - len(strings.TrimLeft(line, " "))
		style := t.muted
		if field := fieldLine.FindString(line); field != "" {
			lead = len(field)
		}
		if e.Kind == "item.updated" {
			body := line[lead:]
			switch {
			case lead == 0:
				style = paint{}
			case strings.HasPrefix(body, "+ "):
				style, lead = t.green, lead+2
			case strings.HasPrefix(body, "- "):
				style, lead = t.red, lead+2
			case strings.HasPrefix(body, "[x] "):
				style, lead = t.green, lead+4
			case strings.HasPrefix(body, "[ ] "):
				lead += 4
			}
		}
		lead = min(lead, max(0, width-column-8))
		parts := strings.Split(ansi.Wrap(line[lead:], max(1, width-column-lead), ""), "\n")
		for i, part := range parts {
			prefix := blank(lead)
			if i == 0 {
				prefix = line[:lead]
			}
			out = append(out, blank(column)+style.render(prefix+part))
		}
	}
	return out
}

func detailDependency(row engine.Row, tab detailTab) string {
	switch tab {
	case tabOverview:
		return row.ReadinessVersion
	case tabRelations:
		return row.RelationVersion
	case tabHistory:
		return changesVersion(row.Changes)
	default:
		return ""
	}
}
func truncateUTF8(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

type cappedBody struct {
	builder strings.Builder
	limit   int
	full    bool
}

func (body *cappedBody) write(value string) {
	if body.full || value == "" {
		return
	}
	remaining := body.limit - body.builder.Len()
	if len(value) <= remaining {
		body.builder.WriteString(value)
		return
	}
	body.builder.WriteString(truncateUTF8(value, max(0, remaining)))
	body.full = true
}

// detailPage builds one tab. Fields and graphs are laid out directly; prose is
// rendered as Markdown in a single renderer call, capped before rendering.
func detailPage(ctx context.Context, item model.Item, request detailRequest, render func(string, int, string) (string, error)) (rendered, error) {
	t, width := request.theme, max(1, request.width)
	limit := bodyLimit
	if request.expanded {
		limit = bodyMoreLimit
	}
	body := cappedBody{limit: limit}
	var page rendered
	switch request.tab {
	case tabOverview:
		page.lines = overviewLines(item, request)
		if item.Description == "" {
			page.lines = append(page.lines, "", t.muted.render("No description."))
		}
		body.write(item.Description)
	case tabCriteria:
		page.lines, page.entries = criteriaLines(item, t, width)
	case tabNotes:
		for _, section := range []struct{ name, text string }{{"Plan", item.Plan}, {"Notes", item.Notes}, {"Summary", item.Summary}} {
			if section.text == "" {
				continue
			}
			body.write("## " + section.name + "\n\n")
			body.write(section.text)
			body.write("\n\n")
		}
		if item.Plan == "" && item.Notes == "" && item.Summary == "" {
			page.lines = []string{t.muted.render("No plan or notes.")}
		}
	case tabComments:
		if len(item.Comments) == 0 {
			page.lines = []string{t.muted.render("No comments.")}
		}
		for _, comment := range item.Comments {
			if body.full {
				break
			}
			header := "### " + comment.Author
			if !comment.Time.IsZero() {
				header += " · " + comment.Time.Format("2006-01-02 15:04")
			}
			body.write(header + "\n\n")
			body.write(comment.Body)
			body.write("\n\n")
		}
	case tabRelations:
		page.lines, page.entries, page.targets = relationLines(request.row, statusNames(request.statuses), t, width)
	}
	page.truncated = body.full
	if text := body.builder.String(); text != "" {
		out, err := render(text, width, t.name)
		if err != nil {
			return rendered{}, err
		}
		if ctx.Err() != nil {
			return rendered{}, ctx.Err()
		}
		out = strings.TrimRight(out, "\n ")
		if len(out) > renderLimit {
			out = truncateUTF8(out, renderLimit) + "\n… rendered output capped"
			page.truncated = true
		}
		if request.tab == tabOverview {
			page.lines = append(page.lines, "")
		}
		for _, line := range strings.Split(out, "\n") {
			page.lines = append(page.lines, strings.TrimRight(line, " "))
		}
	}
	for _, line := range page.lines {
		page.size += len(line) + 1
	}
	return page, nil
}

const propertyWidth = 11

func property(t theme, label, value string) string {
	return t.muted.render(cell(label, propertyWidth)) + value
}

// overviewLines is a property sheet: only fields with values, aligned labels.
func overviewLines(item model.Item, request detailRequest) []string {
	t, row := request.theme, request.row
	var lines []string
	add := func(label, value string) {
		if value != "" {
			lines = append(lines, property(t, label, value))
		}
	}
	join := func(values []string, prefix string) string {
		parts := make([]string, len(values))
		for i, v := range values {
			parts[i] = prefix + textutil.Line(cmp.Or(item.Names[v], v))
		}
		return strings.Join(parts, "  ")
	}
	add("Assignees", join(item.Assignees, ""))
	add("Labels", t.muted.render(join(item.Labels, "#")))
	if item.Milestone != "" {
		add("Milestone", textutil.Line(milestoneName(item.Milestone, request.milestones)))
	}
	add("Sprint", textutil.Line(cmp.Or(item.Names[item.Sprint], sprintName(item.Sprint, request.sprints), item.Sprint)))
	add("Due", textutil.Line(item.Due))
	add("Type", textutil.Line(item.Type))
	if item.Parent != nil {
		add("Parent", textutil.Line(item.Parent.ID))
	}
	if row.Readiness != "" && row.Category != model.Done && row.Category != model.Canceled {
		add("Readiness", readinessText(t, row))
	}
	if !item.Updated.IsZero() {
		add("Updated", age(item.Updated, request.now)+t.muted.render(" · "+item.Updated.Local().Format("2006-01-02 15:04")))
	}
	if !item.Created.IsZero() {
		add("Created", age(item.Created, request.now)+t.muted.render(" · "+item.Created.Local().Format("2006-01-02 15:04")))
	}
	if item.URL != "" {
		add("URL", t.link.render(textutil.Line(item.URL)))
	}
	return lines
}

func readinessGlyph(t theme, r engine.Readiness) string {
	switch r {
	case engine.Ready:
		return t.green.render("✓")
	case engine.Blocked:
		return t.red.render("⊘")
	case engine.Waiting:
		return t.yellow.render("◷")
	case engine.Ineligible:
		return t.muted.render("–")
	}
	return t.muted.render("?")
}
func readinessText(t theme, row engine.Row) string {
	text := readinessGlyph(t, row.Readiness) + " " + string(row.Readiness)
	if row.Readiness != engine.Ready && row.ReadinessReason != "" {
		text += t.muted.render(" · " + textutil.Line(row.ReadinessReason))
	}
	return text
}

// progress draws a fixed-width bar so completion is visible at a glance.
func progress(t theme, done, total, width int) string {
	if total <= 0 || width <= 0 {
		return ""
	}
	filled := done * width / total
	if done > 0 && filled == 0 {
		filled = 1
	}
	bar := t.green.render(strings.Repeat("▰", filled)) + t.faint.render(strings.Repeat("▱", width-filled))
	return bar
}

func criteriaLines(item model.Item, t theme, width int) ([]string, []int) {
	var lines []string
	var criteria []int
	section := func(name string, list []model.Criterion) {
		done := 0
		for _, c := range list {
			if c.Checked {
				done++
			}
		}
		head := t.bold.render(name)
		if len(list) > 0 {
			head += "  " + t.muted.render(strconv.Itoa(done)+"/"+strconv.Itoa(len(list))) + "  " + progress(t, done, len(list), 10)
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, head)
		if len(list) == 0 {
			lines = append(lines, t.muted.render("  None"))
			return
		}
		for _, c := range list {
			box, style := "☐", paint{}
			if c.Checked {
				box, style = t.green.render("✓"), t.muted
			}
			text := ansi.Wrap(textutil.Line(c.Text), max(1, width-4), "")
			for i, part := range strings.Split(text, "\n") {
				if i == 0 {
					criteria = append(criteria, len(lines))
					lines = append(lines, "  "+box+" "+style.render(part))
				} else {
					lines = append(lines, "    "+style.render(part))
				}
			}
			if c.Evidence != "" {
				evidence := ansi.Wrap(textutil.Line(c.Evidence), max(1, width-6), "")
				for i, part := range strings.Split(evidence, "\n") {
					prefix := "    ↳ "
					if i > 0 {
						prefix = "      "
					}
					lines = append(lines, t.muted.render(prefix+part))
				}
			}
		}
	}
	section("Acceptance criteria", item.Criteria)
	section("Definition of done", item.DoD)
	return lines, criteria
}

// relationLines draws the dependency flow (blockers → item → blocked) and the
// hierarchy, each relation with live status from the projection. Each relation
// is one line, an entry the cursor can open.
func relationLines(row engine.Row, names map[string]string, t theme, width int) ([]string, []int, []engine.Relation) {
	byKind := map[string][]engine.Relation{}
	for _, r := range row.Relations {
		byKind[r.Kind] = append(byKind[r.Kind], r)
	}
	var lines []string
	var entries []int
	var targets []engine.Relation
	entry := func(r engine.Relation, connector string) string {
		icon := t.categoryIcon(r.Category)
		status := textutil.Line(cmp.Or(names[r.Status], r.StatusName, r.Status))
		if r.Status == "missing" {
			icon = t.red.render("?")
		}
		head := t.faint.render(connector) + " " + icon + " " + t.muted.render(textutil.Line(r.Ref.ID)) + "  "
		used := ansi.StringWidth(ansi.Strip(head))
		tail := "  " + status
		room := max(1, width-used-ansi.StringWidth(tail))
		title := ansi.Truncate(textutil.Line(r.Title), room, "…")
		style := paint{}
		if r.Category == model.Done || r.Category == model.Canceled {
			style = t.muted
		}
		return head + style.render(title) + t.muted.render(textutil.Line(tail))
	}
	group := func(label string, relations []engine.Relation, summary string) {
		lines = append(lines, t.bold.render(label)+summary)
		for i, r := range relations {
			connector := "├─"
			if i == len(relations)-1 {
				connector = "└─"
			}
			entries, targets = append(entries, len(lines)), append(targets, r)
			lines = append(lines, entry(r, connector))
		}
	}
	closed := func(rs []engine.Relation) int {
		n := 0
		for _, r := range rs {
			if r.Category == model.Done || r.Category == model.Canceled {
				n++
			}
		}
		return n
	}
	blockers, blocking := byKind["blocked by"], byKind["blocking"]
	if len(blockers) > 0 || len(blocking) > 0 {
		arrow := func() {
			lines = append(lines, t.faint.render("   │"), t.faint.render("   ▼"))
		}
		if len(blockers) > 0 {
			open := len(blockers) - closed(blockers)
			summary := "  " + t.green.render("all done")
			if open > 0 {
				summary = "  " + t.red.render(strconv.Itoa(open)+" open")
			}
			group("Blocked by", blockers, summary)
			arrow()
		}
		self := t.accent.render("◆") + " " + t.bold.render(textutil.Line(row.Ref.ID)) + "  " + ansi.Truncate(textutil.Line(row.Title), max(1, width-ansi.StringWidth(row.Ref.ID)-4), "…")
		lines = append(lines, self)
		if len(blocking) > 0 {
			arrow()
			group("Blocks", blocking, "")
		}
	}
	if parents := byKind["parent"]; len(parents) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		group("Parent", parents, "")
	}
	if children := byKind["children"]; len(children) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		done := closed(children)
		summary := "  " + t.muted.render(strconv.Itoa(done)+"/"+strconv.Itoa(len(children))) + "  " + progress(t, done, len(children), 10)
		group("Children", children, summary)
	}
	for _, kind := range []struct{ key, label string }{{"duplicate of", "Duplicate of"}, {"related", "Related"}} {
		if relations := byKind[kind.key]; len(relations) > 0 {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			group(kind.label, relations, "")
		}
	}
	if len(lines) == 0 {
		lines = []string{t.muted.render("No relations.")}
	}
	return lines, entries, targets
}

// sprintName names a sprint the item does not yet carry, as after an edit.
func sprintName(key string, sprints []model.Sprint) string {
	for _, sprint := range sprints {
		if key != "" && sprint.Key == key {
			return sprint.Name
		}
	}
	return ""
}

func milestoneName(key string, milestones []project.Milestone) string {
	for _, milestone := range milestones {
		if milestone.Key == key && milestone.Name != "" {
			return key + " — " + milestone.Name
		}
	}
	return key
}
