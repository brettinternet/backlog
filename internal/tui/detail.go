package tui

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/textutil"
)

type detailTab uint8

const (
	tabOverview detailTab = iota
	tabDescription
	tabCriteria
	tabPlanNotes
	tabComments
	tabRelations
)

var detailTabNames = [...]string{"Overview", "Description", "Criteria", "Plan/Notes", "Comments", "Relations"}

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
}
type detailResult struct {
	generation uint64
	identity   detailIdentity
	lines      []string
	truncated  bool
	err        string
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
	render   func(string, int) (string, error)
	theme    string
	requests chan detailRequest
	results  chan detailResult
	mu       sync.Mutex
	active   context.CancelFunc
	started  sync.Once
	cache    detailCache
}

func newDetailWorker(ctx context.Context, read func(context.Context, model.Ref) (model.Item, error), render func(string, int) (string, error), theme string) *detailWorker {
	return &detailWorker{
		ctx: ctx, read: read, render: render, theme: theme,
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
	key := renderKey{Ref: request.row.Ref, Version: version, Width: request.width, Theme: worker.theme, Tab: request.tab, Expanded: request.expanded, Dependency: dependency}
	if value, ok := worker.cache.render(key); ok {
		result.lines, result.truncated = value.lines, value.truncated
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
		result.lines, result.truncated = value.lines, value.truncated
		return result
	}
	body, truncated := detailBody(item, request.row, request.tab, request.milestones, request.expanded)
	text, err := worker.render(body, max(1, request.width))
	if err != nil {
		result.err = textutil.Line(err.Error())
		return result
	}
	if ctx.Err() != nil {
		result.err = textutil.Line(ctx.Err().Error())
		return result
	}
	text = textutil.Sanitize(text)
	if len(text) > renderLimit {
		text = truncateUTF8(text, renderLimit) + "\n… rendered output capped"
		truncated = true
	}
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || len(lines) == 1 && lines[0] == "" {
		lines = []string{"(no content)"}
	}
	result.identity.Version = version
	result.lines, result.truncated = lines, truncated
	worker.cache.putRender(key, rendered{lines: lines, truncated: truncated, size: len(text)})
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
func (body *cappedBody) line(values ...string) {
	for _, value := range values {
		body.write(value)
	}
	body.write("\n")
}
func (body *cappedBody) list(label string, values []string) {
	if len(values) == 0 {
		return
	}
	body.write(label)
	for index, value := range values {
		if index > 0 {
			body.write(", ")
		}
		body.write(value)
		if body.full {
			return
		}
	}
	body.write("\n")
}
func detailBody(item model.Item, row engine.Row, tab detailTab, milestones []project.Milestone, expanded bool) (string, bool) {
	limit := bodyLimit
	if expanded {
		limit = bodyMoreLimit
	}
	body := cappedBody{limit: limit}
	switch tab {
	case tabOverview:
		body.line("## ", row.Ref.ID, " · ", item.Title)
		body.line("- Type: ", item.Type)
		body.line("- Status: ", item.Status)
		body.line("- Category: ", string(item.Category))
		body.line("- Priority: ", string(item.Priority))
		body.list("- Assignees: ", item.Assignees)
		body.list("- Labels: ", item.Labels)
		if item.Milestone != "" {
			body.line("- Milestone: ", milestoneName(item.Milestone, milestones))
		}
		if item.Due != "" {
			body.line("- Due: ", item.Due)
		}
		if item.URL != "" {
			body.line("- URL: ", item.URL)
		}
		body.line("- Readiness: ", string(row.Readiness))
		body.line("- Reason: ", row.ReadinessReason)
	case tabDescription:
		body.write(item.Description)
		if item.Description == "" {
			body.write("(No description.)")
		}
	case tabCriteria:
		body.line("## Acceptance criteria")
		writeCriteria(&body, item.Criteria)
		body.line("## Definition of done")
		writeCriteria(&body, item.DoD)
	case tabPlanNotes:
		body.line("## Plan")
		body.write(item.Plan)
		if item.Plan == "" {
			body.write("(No plan.)")
		}
		body.write("\n\n## Notes\n")
		body.write(item.Notes)
		if item.Notes == "" {
			body.write("(No notes.)")
		}
	case tabComments:
		if len(item.Comments) == 0 {
			body.write("(No comments.)")
		}
		for _, comment := range item.Comments {
			if body.full {
				break
			}
			if comment.Time.IsZero() {
				body.line("## ", comment.Author)
			} else {
				body.line("## ", comment.Time.Format("2006-01-02 15:04"), " · ", comment.Author)
			}
			body.write(comment.Body)
			body.write("\n\n")
		}
	case tabRelations:
		if len(row.Relations) == 0 {
			body.write("(No relations.)")
		}
		lastKind := ""
		for _, relation := range row.Relations {
			if body.full {
				break
			}
			if relation.Kind != lastKind {
				body.line("## ", strings.ToUpper(relation.Kind))
				lastKind = relation.Kind
			}
			body.line("- ", relation.Ref.ID, " · ", relation.Status, " — ", relation.Title)
		}
	}
	return body.builder.String(), body.full
}
func writeCriteria(body *cappedBody, criteria []model.Criterion) {
	if len(criteria) == 0 {
		body.line("(None.)")
		return
	}
	for _, criterion := range criteria {
		if body.full {
			return
		}
		checked := " "
		if criterion.Checked {
			checked = "x"
		}
		body.line("- [", checked, "] ", criterion.Text)
		if criterion.Evidence != "" {
			body.line("  Evidence: ", criterion.Evidence)
		}
	}
}
func milestoneName(key string, milestones []project.Milestone) string {
	for _, milestone := range milestones {
		if milestone.Key == key && milestone.Name != "" {
			return key + " — " + milestone.Name
		}
	}
	return key
}
