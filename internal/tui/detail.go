package tui

import (
	"container/list"
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

type detailTab uint8

const (
	tabOverview detailTab = iota
	tabCriteria
	tabNotes
	tabComments
	tabRelations
)

var detailTabNames = [...]string{"Overview", "Criteria", "Notes", "Comments", "Relations"}

type detailIdentity struct {
	Ref              model.Ref
	Version          string
	ReadinessVersion string
	RelationVersion  string
	Valid            bool
}

func identityFor(row engine.Row, ok bool) detailIdentity {
	if !ok {
		return detailIdentity{}
	}
	return detailIdentity{
		Ref: row.Ref, Version: row.Version,
		ReadinessVersion: row.ReadinessVersion, RelationVersion: row.RelationVersion,
		Valid: true,
	}
}

type detailRequest struct {
	generation uint64
	row        engine.Row
	width      int
	tab        detailTab
	expanded   bool
	milestones []project.Milestone
	statuses   []project.StatusConfig
	theme      theme
	now        time.Time
}
type detailResult struct {
	generation uint64
	identity   detailIdentity
	lines      []string
	criteria   []int // line index of each criterion, for the Criteria cursor
	counts     tabCounts
	truncated  bool
	err        string
}

// tabCounts label detail tabs from the item, such as "2/5" criteria.
type tabCounts [len(detailTabNames)]string

func itemCounts(item model.Item) tabCounts {
	var c tabCounts
	if total := len(item.Criteria) + len(item.DoD); total > 0 {
		done := 0
		for _, list := range [][]model.Criterion{item.Criteria, item.DoD} {
			for _, criterion := range list {
				if criterion.Checked {
					done++
				}
			}
		}
		c[tabCriteria] = strconv.Itoa(done) + "/" + strconv.Itoa(total)
	}
	if n := len(item.Comments); n > 0 {
		c[tabComments] = strconv.Itoa(n)
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
	Dependency string
}
type itemKey struct {
	Ref     model.Ref
	Version string
}
type rendered struct {
	lines     []string
	criteria  []int
	counts    tabCounts
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
}

const (
	maxRenderEntries = 24
	maxRenderBytes   = 1 << 20
	maxDetailItems   = 8
	maxDetailBytes   = 1 << 20
	bodyLimit        = 32 << 10
	bodyMoreLimit    = 128 << 10
	renderLimit      = 128 << 10
)

func newDetailCache() detailCache {
	return detailCache{
		renders: make(map[renderKey]*list.Element), renderOrder: list.New(),
		items: make(map[itemKey]*list.Element), itemOrder: list.New(),
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
	if value.size > maxRenderBytes {
		return
	}
	element := cache.renderOrder.PushBack(renderedEntry{key, value})
	cache.renders[key] = element
	cache.renderBytes += value.size
	for cache.renderOrder.Len() > maxRenderEntries || cache.renderBytes > maxRenderBytes {
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

type detailWorker struct {
	ctx      context.Context
	read     func(context.Context, model.Ref) (model.Item, error)
	render   func(body string, width int, theme string) (string, error)
	requests chan detailRequest
	results  chan detailResult
	mu       sync.Mutex
	active   context.CancelFunc
	started  sync.Once
	cache    detailCache
}

func newDetailWorker(ctx context.Context, read func(context.Context, model.Ref) (model.Item, error), render func(string, int, string) (string, error)) *detailWorker {
	return &detailWorker{
		ctx: ctx, read: read, render: render,
		requests: make(chan detailRequest, 1), results: make(chan detailResult, 1), cache: newDetailCache(),
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
	version := request.row.Version
	dependency := detailDependency(request.row, request.tab)
	key := renderKey{Ref: request.row.Ref, Version: version, Width: request.width, Theme: request.theme.key(), Tab: request.tab, Expanded: request.expanded, Dependency: dependency}
	if value, ok := worker.cache.render(key); ok {
		result.lines, result.criteria, result.counts, result.truncated = value.lines, value.criteria, value.counts, value.truncated
		return result
	}
	item, ok := worker.cache.item(itemKey{Ref: request.row.Ref, Version: version})
	if !ok {
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
	if value, ok := worker.cache.render(key); ok {
		result.identity.Version = version
		result.lines, result.criteria, result.counts, result.truncated = value.lines, value.criteria, value.counts, value.truncated
		return result
	}
	page, err := detailPage(ctx, item, request, worker.render)
	if err != nil {
		result.err = textutil.Line(err.Error())
		return result
	}
	if len(page.lines) == 0 {
		page.lines = []string{request.theme.muted.render("Nothing here yet.")}
	}
	page.counts = itemCounts(item)
	result.identity.Version = version
	result.lines, result.criteria, result.counts, result.truncated = page.lines, page.criteria, page.counts, page.truncated
	worker.cache.putRender(key, page)
	return result
}
func detailDependency(row engine.Row, tab detailTab) string {
	switch tab {
	case tabOverview:
		return row.ReadinessVersion
	case tabRelations:
		return row.RelationVersion
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
		page.lines, page.criteria = criteriaLines(item, t, width)
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
		page.lines = relationLines(item, request.row, statusNames(request.statuses), t, width)
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
			parts[i] = prefix + textutil.Line(v)
		}
		return strings.Join(parts, "  ")
	}
	add("Assignees", join(item.Assignees, ""))
	add("Labels", t.muted.render(join(item.Labels, "#")))
	if item.Milestone != "" {
		add("Milestone", textutil.Line(milestoneName(item.Milestone, request.milestones)))
	}
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
// hierarchy, each relation with live status from the projection.
func relationLines(item model.Item, row engine.Row, names map[string]string, t theme, width int) []string {
	byKind := map[string][]engine.Relation{}
	for _, r := range row.Relations {
		byKind[r.Kind] = append(byKind[r.Kind], r)
	}
	var lines []string
	entry := func(r engine.Relation, connector string) string {
		icon := t.categoryIcon(r.Category)
		status := r.Status
		if name, ok := names[status]; ok {
			status = name
		}
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
	var related []string
	for _, r := range item.Related {
		related = append(related, textutil.Line(r.ID))
	}
	if item.DuplicateOf != nil {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, t.bold.render("Duplicate of")+"  "+t.muted.render(textutil.Line(item.DuplicateOf.ID)))
	}
	if len(related) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, t.bold.render("Related")+"  "+t.muted.render(strings.Join(related, "  ")))
	}
	if len(lines) == 0 {
		lines = []string{t.muted.render("No relations.")}
	}
	return lines
}

func milestoneName(key string, milestones []project.Milestone) string {
	for _, milestone := range milestones {
		if milestone.Key == key && milestone.Name != "" {
			return key + " — " + milestone.Name
		}
	}
	return key
}
