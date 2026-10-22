package engine

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
)

// Row is prepared off the UI loop. Rendering only needs viewport-sized work.
type Row struct {
	Ref                    model.Ref
	Version, Title, Status string
	Priority               model.Priority
	Pending                bool
	Error                  string
}
type RowGroup struct {
	Value string
	Rows  []Row
}
type Projection struct {
	Request, Generation uint64
	Groups              []RowGroup
	Error               error
}
type projectionRequest struct {
	id       uint64
	snapshot *Snapshot
	text     string
	ctx      context.Context
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
	latest(p.requests, projectionRequest{p.serial, snapshot, text, ctx})
	return p.serial
}
func (p *Projector) Results() <-chan Projection { return p.results }
func (p *Projector) Close()                     { p.cancel(); <-p.done }
func (p *Projector) run() {
	defer close(p.done)
	var cached *Snapshot
	var base query.Snapshot
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
				for _, e := range ranking.Items {
					base.Next = append(base.Next, e.Ref)
					state := base.States[e.Ref]
					state.Readiness = string(e.Readiness)
					base.States[e.Ref] = state
				}
				cached = req.snapshot
			}
			base.Now = time.Now()
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
					err = errs[0]
				}
			}
			if err == nil {
				var groups []query.Group
				groups, err = q.ProjectContext(req.ctx, base, nil)
				if err == nil {
					for _, g := range groups {
						group := RowGroup{Value: g.Value, Rows: make([]Row, 0, len(g.Items))}
						for _, i := range g.Items {
							r := req.snapshot.records[i.Ref]
							group.Rows = append(group.Rows, Row{Ref: i.Ref, Version: i.Version, Title: i.Title, Status: i.Status, Priority: i.Priority, Pending: r.edit.Pending, Error: r.edit.Error})
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
