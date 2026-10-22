package engine

import (
	"context"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
)

// Row is prepared off the UI loop. Rendering only needs viewport-sized work.
type Row struct {
	Ref                                       model.Ref
	Version, Title, Status, Type, Milestone   string
	Due, URL                                  string
	Category                                  model.StatusCategory
	Priority                                  model.Priority
	Assignees, Labels                         []string
	Readiness                                 Readiness
	ReadinessReason                           string
	ReadinessVersion                          string
	Relations                                 []Relation
	RelationVersion                           string
	Pending                                   bool
	Error                                     string
	OpenBlockers, ChildrenDone, ChildrenTotal int
}
type Relation struct {
	Kind          string
	Ref           model.Ref
	Title, Status string
	Version       string
}
type RowGroup struct {
	Value string
	Rows  []Row
}
type Projection struct {
	Request, Generation uint64
	Groups              []RowGroup
	Error               error
	Warning             error
	Completions         []query.Candidate
	Ungrouped           bool
}
type projectionRequest struct {
	id            uint64
	snapshot      *Snapshot
	text          string
	ctx           context.Context
	complete      bool
	field, prefix string
}

// Projector runs one latest-wins worker, never a goroutine per keystroke.
type Projector struct {
	ctx      context.Context
	cancel   context.CancelFunc
	cfg      project.Config
	requests chan projectionRequest
	results  chan Projection
	mu       sync.Mutex
	serial   uint64
	active   context.CancelFunc
	done     chan struct{}
}

func NewProjector(ctx context.Context, cfg project.Config) *Projector {
	ctx, cancel := context.WithCancel(ctx)
	p := &Projector{ctx: ctx, cancel: cancel, cfg: cfg, requests: make(chan projectionRequest, 1), results: make(chan Projection, 1), done: make(chan struct{})}
	go p.run()
	return p
}
func (p *Projector) Request(snapshot *Snapshot, text string) uint64 {
	return p.request(snapshot, text, false, "", "")
}

// RequestEditor uses the same bounded latest-wins worker for completion and projection.
func (p *Projector) RequestEditor(snapshot *Snapshot, text, field, prefix string) uint64 {
	return p.request(snapshot, text, true, field, prefix)
}
func (p *Projector) request(snapshot *Snapshot, text string, complete bool, field, prefix string) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active != nil {
		p.active()
	}
	ctx, cancel := context.WithCancel(p.ctx)
	p.active = cancel
	p.serial++
	select {
	case <-p.results:
	default:
	}
	latest(p.requests, projectionRequest{id: p.serial, snapshot: snapshot, text: text, ctx: ctx, complete: complete, field: field, prefix: prefix})
	return p.serial
}
func (p *Projector) Results() <-chan Projection { return p.results }
func (p *Projector) Close()                     { p.cancel(); <-p.done }
func normalizedRef(ref, owner model.Ref) model.Ref {
	if ref.Source == "" {
		ref.Source = owner.Source
	}
	return ref
}

func reverseRelations(ctx context.Context, snapshot *Snapshot) (map[model.Ref][]model.Ref, map[model.Ref][]model.Ref, bool) {
	blockingBy, childrenBy := map[model.Ref][]model.Ref{}, map[model.Ref][]model.Ref{}
	index := 0
	for _, record := range snapshot.records {
		if index%256 == 0 && ctx.Err() != nil {
			return nil, nil, false
		}
		index++
		item := record.item
		for _, ref := range item.BlockedBy {
			ref = normalizedRef(ref, item.Ref)
			blockingBy[ref] = append(blockingBy[ref], item.Ref)
		}
		if item.Parent != nil {
			ref := normalizedRef(*item.Parent, item.Ref)
			childrenBy[ref] = append(childrenBy[ref], item.Ref)
		}
	}
	return blockingBy, childrenBy, true
}

func relationRows(item model.Item, snapshot *Snapshot, blockingBy, childrenBy map[model.Ref][]model.Ref) []Relation {
	if len(item.BlockedBy) == 0 && len(item.Blocks) == 0 && len(item.Children) == 0 && item.Parent == nil && len(blockingBy[item.Ref]) == 0 && len(childrenBy[item.Ref]) == 0 {
		return nil
	}
	rows := make([]Relation, 0, len(item.BlockedBy)+len(item.Blocks)+len(item.Children)+2)
	appendRelations := func(kind string, refs []model.Ref) {
		refs = slices.Clone(refs)
		for index := range refs {
			refs[index] = normalizedRef(refs[index], item.Ref)
		}
		slices.SortFunc(refs, compareRef)
		refs = slices.Compact(refs)
		for _, ref := range refs {
			target := snapshot.records[ref]
			relation := Relation{Kind: kind, Ref: ref, Title: ref.ID, Status: "missing"}
			if target != nil {
				relation.Title = target.item.Title
				relation.Status = target.item.Status
				relation.Version = target.item.Version
			}
			rows = append(rows, relation)
		}
	}
	appendRelations("blocked by", item.BlockedBy)
	appendRelations("blocking", append(slices.Clone(item.Blocks), blockingBy[item.Ref]...))
	if item.Parent != nil {
		appendRelations("parent", []model.Ref{*item.Parent})
	}
	appendRelations("children", append(slices.Clone(item.Children), childrenBy[item.Ref]...))
	return rows
}

func relationVersion(relations []Relation) string {
	if len(relations) == 0 {
		return ""
	}
	hash := fnv.New64a()
	for _, relation := range relations {
		for _, value := range []string{relation.Kind, relation.Ref.Source, relation.Ref.ID, relation.Title, relation.Status, relation.Version} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	return strconv.FormatUint(hash.Sum64(), 16)
}

func readinessReason(e Explanation) string {
	if e.Readiness == Ready {
		return "all readiness checks pass"
	}
	if e.Readiness == "" {
		return "not available"
	}
	parts := make([]string, 0, len(e.Reasons))
	for _, reason := range e.Reasons {
		if reason.Code == "candidate" {
			continue
		}
		text := reason.Code
		if len(reason.Refs) > 0 {
			ids := make([]string, len(reason.Refs))
			for i, ref := range reason.Refs {
				ids[i] = ref.ID
			}
			text += ": " + strings.Join(ids, ", ")
		} else if len(reason.Values) > 0 {
			text += ": " + strings.Join(reason.Values, ", ")
		}
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return "no readiness explanation available"
	}
	return strings.Join(parts, "; ")
}

func (p *Projector) run() {
	defer close(p.done)
	var cached *Snapshot
	var base query.Snapshot
	var explanations map[model.Ref]Explanation
	var blockingBy, childrenBy map[model.Ref][]model.Ref
	for {
		select {
		case <-p.ctx.Done():
			return
		case req := <-p.requests:
			result := Projection{Request: req.id, Generation: req.snapshot.Generation}
			if cached != req.snapshot {
				// A canceled rebuild must not leave the old cache key pointing at a
				// partially replaced base when the user switches back to that snapshot.
				cached = nil
				base = query.Snapshot{Settings: p.cfg.Settings, Now: time.Now(), States: map[model.Ref]query.State{}}
				if p.cfg.Me.Default != "" {
					base.Me = []string{p.cfg.Me.Default}
				}
				for ref, r := range req.snapshot.records {
					if req.ctx.Err() != nil {
						break
					}
					base.Items = append(base.Items, r.item)
					base.States[ref] = query.State{Pending: r.edit.Pending, Archived: r.archived}
				}
				if req.ctx.Err() != nil {
					continue
				}
				archivedRefs := map[model.Ref]bool{}
				for ref, state := range base.States {
					if state.Archived {
						archivedRefs[ref] = true
					}
				}
				ranking := Evaluate(Input{Items: base.Items, Config: p.cfg.Ready, Me: base.Me, Archived: archivedRefs})
				if req.ctx.Err() != nil {
					continue
				}
				var ok bool
				blockingBy, childrenBy, ok = reverseRelations(req.ctx, req.snapshot)
				if !ok {
					continue
				}
				explanations = make(map[model.Ref]Explanation, len(ranking.Items))
				for _, e := range ranking.Items {
					base.Next = append(base.Next, e.Ref)
					state := base.States[e.Ref]
					state.Readiness = string(e.Readiness)
					base.States[e.Ref] = state
					explanations[e.Ref] = e
				}
				cached = req.snapshot
			}
			base.Now = time.Now()
			if req.complete {
				result.Completions = base.Complete(req.field, req.prefix, query.Options{})
			}
			q, err := query.Parse(req.text, query.Options{})
			if err == nil {
				hasIs, includeArchive := false, false
				for _, term := range q.Terms {
					if term.Field == "is" {
						hasIs = true
						for _, v := range term.Values {
							includeArchive = includeArchive || !term.Negated && strings.EqualFold(v.Text, "archived")
						}
					}
				}
				defaults := ""
				if !hasIs {
					defaults = "is:open"
				}
				if !includeArchive {
					defaults += " -is:archived"
				}
				extra, _ := query.Parse(defaults, query.Options{})
				q.Terms = append(q.Terms, extra.Terms...)
			}
			if err == nil {
				if errs := q.Validate(base); len(errs) > 0 {
					result.Warning = errs[0]
				}
			}
			if err == nil {
				result.Ungrouped = q.Group == "" || q.Group == "none"
				var groups []query.Group
				groups, err = q.ProjectContext(req.ctx, base, nil)
				if err == nil {
					for _, g := range groups {
						group := RowGroup{Value: g.Value, Rows: make([]Row, 0, len(g.Items))}
						for _, i := range g.Items {
							r := req.snapshot.records[i.Ref]
							explanation := explanations[i.Ref]
							reason := readinessReason(explanation)
							relations := relationRows(i, req.snapshot, blockingBy, childrenBy)
							row := Row{
								Ref: i.Ref, Version: i.Version, Title: i.Title, Status: i.Status,
								Type: i.Type, Milestone: i.Milestone, Due: i.Due, URL: i.URL,
								Category: i.Category, Priority: i.Priority,
								Assignees: i.Assignees, Labels: i.Labels, Pending: r.edit.Pending,
								Error: r.edit.Error, Readiness: explanation.Readiness,
								ReadinessReason:  reason,
								ReadinessVersion: string(explanation.Readiness) + "\x00" + reason,
								Relations:        relations, RelationVersion: relationVersion(relations),
							}
							closed := func(ref model.Ref) bool {
								r := req.snapshot.records[ref]
								return r != nil && (r.item.Category == model.Done || r.item.Category == model.Canceled)
							}
							for _, ref := range i.BlockedBy {
								if !closed(ref) {
									row.OpenBlockers++
								}
							}
							row.ChildrenTotal = len(i.Children)
							for _, ref := range i.Children {
								if closed(ref) {
									row.ChildrenDone++
								}
							}
							group.Rows = append(group.Rows, row)
						}
						result.Groups = append(result.Groups, group)
					}
				}
			}
			result.Error = err
			p.mu.Lock()
			if req.id == p.serial && req.ctx.Err() == nil {
				latest(p.results, result)
			}
			p.mu.Unlock()
		}
	}
}
