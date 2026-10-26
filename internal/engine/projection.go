package engine

import (
	"context"
	"fmt"
	"hash/fnv"
	"path/filepath"
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
	Project, Source                           string
	Claim                                     string
	Ref                                       model.Ref
	Version, Title, Status, Type, Milestone   string
	Due, URL                                  string
	Updated                                   time.Time
	Category                                  model.StatusCategory
	Priority                                  model.Priority
	Assignees, Labels                         []string
	Names                                     map[string]string
	Readiness                                 Readiness
	ReadinessReason                           string
	ReadinessVersion                          string
	Relations                                 []Relation
	RelationVersion                           string
	Pending                                   bool
	Error                                     string
	ErrorLocalVersion, ErrorRemoteVersion     string
	OpenBlockers, ChildrenDone, ChildrenTotal int
	CriteriaDone, CriteriaTotal, Comments     int
}

// SetCounts tallies the item's checked criteria and its comments, so detail
// tabs can label them before the full item is read.
func (r *Row) SetCounts(item model.Item) {
	r.CriteriaDone, r.CriteriaTotal = 0, 0
	for _, list := range [][]model.Criterion{item.Criteria, item.DoD} {
		for _, criterion := range list {
			r.CriteriaTotal++
			if criterion.Checked {
				r.CriteriaDone++
			}
		}
	}
	r.Comments = len(item.Comments)
}

type Relation struct {
	Kind          string
	Ref           model.Ref
	Title, Status string
	StatusName    string // provider display name of an opaque Status
	Category      model.StatusCategory
	Version       string
}
type RowGroup struct {
	Value string
	Rows  []Row
}
type Projection struct {
	NextClaimExpiry     time.Time
	Request, Generation uint64
	Groups              []RowGroup
	Error               error
	Warning             error
	Completions         []query.Candidate
	Ungrouped           bool
	Catalog             *Catalog
}
type projectionRequest struct {
	project       string
	id            uint64
	refreshClaims bool
	snapshot      *Snapshot
	text          string
	ctx           context.Context
	complete      bool
	field, prefix string
}

// Projector runs one latest-wins worker, never a goroutine per keystroke.
type Projector struct {
	now      func() time.Time
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
	return NewProjectorWithClock(ctx, cfg, time.Now)
}
func NewProjectorWithClock(ctx context.Context, cfg project.Config, now func() time.Time) *Projector {
	ctx, cancel := context.WithCancel(ctx)
	p := &Projector{now: now, ctx: ctx, cancel: cancel, cfg: cfg, requests: make(chan projectionRequest, 1), results: make(chan Projection, 1), done: make(chan struct{})}
	go p.run()
	return p
}
func (p *Projector) Request(snapshot *Snapshot, text string) uint64 {
	return p.RequestQuery(snapshot, ProjectionQuery{Text: text})
}

// ProjectionQuery is one request. Project scopes a workspace. Complete adds
// candidates for Prefix in Field, as typed in an editor. RefreshClaims does
// one bulk claim refresh first.
type ProjectionQuery struct {
	Text, Project string
	Complete      bool
	Field, Prefix string
	RefreshClaims bool
}

// RequestQuery runs every projection, completion and claim refresh on the same
// bounded latest-wins worker.
func (p *Projector) RequestQuery(snapshot *Snapshot, q ProjectionQuery) uint64 {
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
	latest(p.requests, projectionRequest{project: q.Project, id: p.serial, snapshot: snapshot, text: q.Text, ctx: ctx, complete: q.Complete, field: q.Field, prefix: q.Prefix, refreshClaims: q.RefreshClaims})
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

// Reverse indexes inbound relations for off-loop row preparation.
type Reverse struct {
	BlockingBy, ChildrenBy, RelatedBy map[model.Ref][]model.Ref
}

// ReverseRelations indexes inbound relations for off-loop row preparation.
func ReverseRelations(ctx context.Context, snapshot *Snapshot) (Reverse, bool) {
	reverse := Reverse{BlockingBy: map[model.Ref][]model.Ref{}, ChildrenBy: map[model.Ref][]model.Ref{}, RelatedBy: map[model.Ref][]model.Ref{}}
	index := 0
	for _, record := range snapshot.records {
		if index%256 == 0 && ctx.Err() != nil {
			return Reverse{}, false
		}
		index++
		item := record.item
		for _, ref := range item.BlockedBy {
			ref = normalizedRef(ref, item.Ref)
			reverse.BlockingBy[ref] = append(reverse.BlockingBy[ref], item.Ref)
		}
		for _, ref := range item.Related {
			ref = normalizedRef(ref, item.Ref)
			reverse.RelatedBy[ref] = append(reverse.RelatedBy[ref], item.Ref)
		}
		if item.Parent != nil {
			ref := normalizedRef(*item.Parent, item.Ref)
			reverse.ChildrenBy[ref] = append(reverse.ChildrenBy[ref], item.Ref)
		}
	}
	return reverse, true
}

// derivedBlocks returns what item blocks. Linear bumps only the target of a
// created or retyped relation (BL-90), so a loaded target's BlockedBy is
// authoritative; item.Blocks contributes only targets that aren't loaded.
func derivedBlocks(item model.Item, blockingBy map[model.Ref][]model.Ref, loaded func(model.Ref) bool) []model.Ref {
	refs := slices.Clone(blockingBy[item.Ref])
	for _, ref := range item.Blocks {
		if ref = normalizedRef(ref, item.Ref); !loaded(ref) {
			refs = append(refs, ref)
		}
	}
	slices.SortFunc(refs, compareRef)
	return slices.Compact(refs)
}

// deriveBlocks replaces each item's Blocks with derivedBlocks, so queries see
// the same view as relation rows.
func deriveBlocks(items []model.Item) {
	loaded := make(map[model.Ref]bool, len(items))
	blockingBy := map[model.Ref][]model.Ref{}
	for _, item := range items {
		loaded[item.Ref] = true
		for _, ref := range item.BlockedBy {
			ref = normalizedRef(ref, item.Ref)
			blockingBy[ref] = append(blockingBy[ref], item.Ref)
		}
	}
	for index := range items {
		items[index].Blocks = derivedBlocks(items[index], blockingBy, func(ref model.Ref) bool { return loaded[ref] })
	}
}

// RelationRows prepares canonical detail relations using a shared reverse index.
func RelationRows(item model.Item, snapshot *Snapshot, reverse Reverse) []Relation {
	if len(item.BlockedBy) == 0 && len(item.Blocks) == 0 && len(item.Children) == 0 && len(item.Related) == 0 && item.Parent == nil && item.DuplicateOf == nil && len(reverse.BlockingBy[item.Ref]) == 0 && len(reverse.ChildrenBy[item.Ref]) == 0 && len(reverse.RelatedBy[item.Ref]) == 0 {
		return nil
	}
	rows := make([]Relation, 0, len(item.BlockedBy)+len(item.Blocks)+len(item.Children)+len(item.Related)+2)
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
				relation.StatusName = target.item.Names[target.item.Status]
				relation.Category = target.item.Category
				relation.Version = target.item.Version
			}
			rows = append(rows, relation)
		}
	}
	blocking := derivedBlocks(item, reverse.BlockingBy, func(ref model.Ref) bool { return snapshot.records[ref] != nil })
	appendRelations("blocked by", item.BlockedBy)
	appendRelations("blocking", blocking)
	if item.Parent != nil {
		appendRelations("parent", []model.Ref{*item.Parent})
	}
	appendRelations("children", append(slices.Clone(item.Children), reverse.ChildrenBy[item.Ref]...))
	if item.DuplicateOf != nil {
		appendRelations("duplicate of", []model.Ref{*item.DuplicateOf})
	}
	// Related is symmetric. A blocks relation supersedes it, since after a
	// related->blocks retype only the target is fresh (BL-90).
	related := append(slices.Clone(item.Related), reverse.RelatedBy[item.Ref]...)
	related = slices.DeleteFunc(related, func(ref model.Ref) bool {
		ref = normalizedRef(ref, item.Ref)
		return slices.Contains(blocking, ref) || slices.ContainsFunc(item.BlockedBy, func(blocker model.Ref) bool { return normalizedRef(blocker, item.Ref) == ref })
	})
	appendRelations("related", related)
	return rows
}

// Catalog prepares the row of any item in one snapshot in time proportional to
// the item, so the UI can open an item outside its list without a scan (D14).
type Catalog struct {
	snapshot     *Snapshot
	reverse      Reverse
	explanations map[model.Ref]Explanation
}

func (c *Catalog) Row(ref model.Ref) (Row, bool) {
	r := c.snapshot.records[ref]
	if r == nil {
		return Row{}, false
	}
	return newRow(*r.item, r, c.explanations[ref], RelationRows(*r.item, c.snapshot, c.reverse)), true
}

// newRow sets the fields every row has; projections add project, claim and
// list counts.
func newRow(i model.Item, r *record, explanation Explanation, relations []Relation) Row {
	localVersion, remoteVersion := "", ""
	if r.edit.Local != nil {
		localVersion = r.edit.Local.Version
	}
	if r.edit.Remote != nil {
		remoteVersion = r.edit.Remote.Version
	}
	reason := readinessReason(explanation)
	row := Row{
		Ref: i.Ref, Version: i.Version, Title: i.Title, Status: i.Status,
		Type: i.Type, Milestone: i.Milestone, Due: i.Due, URL: i.URL,
		Updated: i.Updated, Category: i.Category, Priority: i.Priority,
		Assignees: i.Assignees, Labels: i.Labels, Names: i.Names, Pending: r.edit.Pending,
		Error: r.edit.Error, ErrorLocalVersion: localVersion,
		ErrorRemoteVersion: remoteVersion, Readiness: explanation.Readiness,
		ReadinessReason:  reason,
		ReadinessVersion: string(explanation.Readiness) + "\x00" + reason,
		Relations:        relations, RelationVersion: RelationVersion(relations),
	}
	row.SetCounts(i)
	return row
}

// RelationVersion fingerprints the prepared relations for detail-cache invalidation.
func RelationVersion(relations []Relation) string {
	if len(relations) == 0 {
		return ""
	}
	hash := fnv.New64a()
	for _, relation := range relations {
		for _, value := range []string{relation.Kind, relation.Ref.Source, relation.Ref.ID, relation.Title, relation.Status, relation.StatusName, string(relation.Category), relation.Version} {
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
		if reason.Code != "candidate" {
			parts = append(parts, reason.text())
		}
	}
	if len(parts) == 0 {
		return "no readiness explanation available"
	}
	return strings.Join(parts, "; ")
}

func (p *Projector) run() {
	defer close(p.done)
	var cached *Snapshot
	var workspaceOriginal, workspaceView *Snapshot
	var workspaceScope string
	var claims []model.ClaimInfo
	var claimsLoaded bool
	var claimExpiry time.Time
	var claimLabels map[model.Ref]string
	var claimErr error
	var base query.Snapshot
	var explanations map[model.Ref]Explanation
	var reverse Reverse
	var catalog *Catalog
	for {
		select {
		case <-p.ctx.Done():
			return
		case req := <-p.requests:
			result := Projection{Request: req.id, Generation: req.snapshot.Generation}
			if p.cfg.Workspace != nil {
				if workspaceOriginal != req.snapshot || workspaceScope != req.project {
					workspaceOriginal, workspaceScope = req.snapshot, req.project
					workspaceView = workspaceSnapshot(p.cfg, req.snapshot, req.project)
				}
				req.snapshot = workspaceView
			}
			coordinator, caller := Coordination(req.ctx)
			coordination := p.cfg.Workspace == nil && p.cfg.Coordination.Enabled()
			now := p.now()
			refresh := coordination && (!claimsLoaded || req.refreshClaims)
			if refresh {
				var nextClaims []model.ClaimInfo
				var nextErr error
				if coordinator == nil {
					nextErr = fmt.Errorf("coordination unavailable")
				} else {
					nextClaims, nextErr = coordinator.List(req.ctx)
				}
				// A source update must not wait for another provider call after
				// canceling a poll. Retain the last completed overlay until the
				// next explicit refresh; an unfinished startup lookup still retries.
				if req.ctx.Err() != nil {
					continue
				}
				claims, claimErr = nextClaims, nextErr
				claimsLoaded = true
			}
			expired := !claimExpiry.IsZero() && !claimExpiry.After(now)
			if cached != req.snapshot || refresh || expired || p.cfg.Workspace != nil && req.refreshClaims {
				// A canceled rebuild must not leave the old cache key pointing at a
				// partially replaced base when the user switches back to that snapshot.
				cached = nil
				// Only this worker owns the query scratch. Reuse its item array
				// across rebuilds, including canceled ones; projections copy results.
				items := base.Items
				clear(items)
				items = items[:0]
				if cap(items) < req.snapshot.Len() {
					items = make([]model.Item, 0, req.snapshot.Len())
				}
				base = query.Snapshot{Settings: p.cfg.Settings, Now: now, Items: items, States: make(map[model.Ref]query.State, req.snapshot.Len()), Sprints: req.snapshot.Sprints(), SprintsLoading: req.snapshot.SprintsPending()}
				if p.cfg.Me.Default != "" {
					base.Me = []string{p.cfg.Me.Default}
				}
				for ref, r := range req.snapshot.records {
					if req.ctx.Err() != nil {
						break
					}
					base.Items = append(base.Items, *r.item)
					base.States[ref] = query.State{Pending: r.edit.Pending, Archived: r.archived}
				}
				if req.ctx.Err() != nil {
					continue
				}
				staleRefs := map[model.Ref]bool{}
				for ref, r := range req.snapshot.records {
					if r.stale {
						staleRefs[ref] = true
					}
				}
				archivedRefs := map[model.Ref]bool{}
				for ref, state := range base.States {
					if state.Archived {
						archivedRefs[ref] = true
					}
				}
				base.Coordination = coordination
				claimStates := map[model.Ref]Claim{}
				claimLabels = map[model.Ref]string{}
				claimExpiry = time.Time{}
				if coordination && claimErr == nil && coordinator != nil {
					claimStates, claimLabels = OverlayStates(coordinator, caller, base.Items, claims, now)
					for _, claim := range claims {
						if claim.ExpiresAt.After(now) && (claimExpiry.IsZero() || claim.ExpiresAt.Before(claimExpiry)) {
							claimExpiry = claim.ExpiresAt
						}
					}
				}
				for ref, claim := range claimStates {
					state := base.States[ref]
					state.Claim = string(claim)
					if claim == ClaimMine {
						state.Claim = "me"
					}
					base.States[ref] = state
				}
				ranking := Evaluate(Input{Items: base.Items, Config: p.cfg.Ready, Me: base.Me, Archived: archivedRefs, Stale: staleRefs, Coordination: coordination, Claims: claimStates, Sprints: base.Sprints})
				if p.cfg.Workspace != nil {
					var warnings []string
					ranking, claimStates, warnings = workspaceRanking(req.ctx, p.cfg, base.Items, archivedRefs, staleRefs, base.Sprints)
					for _, project := range p.cfg.Workspace.Projects {
						base.Coordination = base.Coordination || project.Coordination.Enabled()
					}
					if len(warnings) > 0 {
						result.Warning = fmt.Errorf("%s", strings.Join(warnings, "; "))
					}
					for ref, claim := range claimStates {
						state := base.States[ref]
						state.Claim = string(claim)
						if claim == ClaimMine {
							state.Claim = "me"
						}
						base.States[ref] = state
					}
				}
				if req.ctx.Err() != nil {
					continue
				}
				var ok bool
				reverse, ok = ReverseRelations(req.ctx, req.snapshot)
				if !ok {
					continue
				}
				for index := range base.Items {
					base.Items[index].Blocks = derivedBlocks(base.Items[index], reverse.BlockingBy, func(ref model.Ref) bool { return req.snapshot.records[ref] != nil })
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
				catalog = &Catalog{snapshot: req.snapshot, reverse: reverse, explanations: explanations}
			}
			result.Catalog = catalog
			base.Now = now
			result.NextClaimExpiry = claimExpiry
			if claimErr != nil {
				result.Warning = claimErr
			}
			if req.complete {
				var err error
				result.Completions, err = base.CompleteContext(req.ctx, req.field, req.prefix, query.Options{})
				if err != nil {
					continue
				}
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
						for n, i := range g.Items {
							if n%256 == 0 && req.ctx.Err() != nil {
								break
							}
							r := req.snapshot.records[i.Ref]
							relations := RelationRows(i, req.snapshot, reverse)
							projectConfig, localSource := p.cfg.SourceProject(i.Ref.Source)
							projectName := i.Project
							if projectName != "" {
								projectName = projectConfig.Project.Name
								if projectName == "" {
									projectName = filepath.Base(i.Project)
								}
							}
							row := newRow(i, r, explanations[i.Ref], relations)
							row.Project, row.Source, row.Claim = projectName, localSource, claimLabels[i.Ref]
							closed := func(ref model.Ref) bool {
								r := req.snapshot.records[normalizedRef(ref, i.Ref)]
								return r != nil && !r.stale && (r.item.Category == model.Done || r.item.Category == model.Canceled)
							}
							for _, ref := range i.BlockedBy {
								if !closed(ref) {
									row.OpenBlockers++
								}
							}
							// Children include reverse parent links, matching the Relations tab.
							for _, relation := range relations {
								if relation.Kind == "children" {
									row.ChildrenTotal++
									if closed(relation.Ref) {
										row.ChildrenDone++
									}
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
