package changes

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

// Load reads one project's local changes and every forge's list once, as bl
// changes and MCP do before linking; ReadVerdicts then reads what the list
// leaves out for the pull requests linked.
func Load(ctx context.Context, o Options) Data {
	d := LoadLocal(ctx, o, Data{})
	prs, statuses := LoadForges(ctx, o.Forges)
	d.PullRequests, d.Statuses = prs, append(d.Statuses, statuses...)
	return d
}

// LoadForges lists every forge's pull requests. A forge that fails keeps
// what it last had, marked stale, so its pull requests read as stale rather
// than vanishing or looking current. One with older pull requests left to
// read is partial, its list current.
func LoadForges(ctx context.Context, forges []Forge) ([]PullRequest, []Status) {
	var prs []PullRequest
	var statuses []Status
	for _, f := range forges {
		list, err := f.PullRequests(ctx)
		status := Status{Provider: f.Name(), State: OK}
		if partial := (*PartialPulls)(nil); errors.As(err, &partial) {
			status.State, status.Reason = Partial, err.Error()
		} else if err != nil {
			status.State, status.Reason = Unavailable, err.Error()
			for i := range list {
				list[i].Stale = f.Name() + " refresh failed: " + err.Error()
			}
		}
		prs = append(prs, list...)
		statuses = append(statuses, status)
	}
	return prs, statuses
}

// ReadVerdicts sets the merge state, review, and checks of the open pull
// requests in prs that linked names, in its order, then of every other open
// one in o.PullSources' repositories, through o's forges. Others carry none
// from GitHub, whose list leaves them out. A forge whose read fails keeps
// what it read and reports why in statuses, unavailable unless already
// partial.
func ReadVerdicts(ctx context.Context, o Options, prs []PullRequest, statuses []Status, linked []PullID) {
	lower := func(k PullID) PullID { return PullID{strings.ToLower(k.Repo), k.Number} }
	index := map[PullID]int{}
	for i, pr := range prs {
		if pr.State == StateOpen && pr.Stale == "" {
			index[lower(PullID{pr.Repo, pr.Number})] = i
		}
	}
	var want []PullID
	seen := map[PullID]bool{}
	add := func(k PullID) {
		if _, ok := index[lower(k)]; ok && !seen[lower(k)] {
			seen[lower(k)] = true
			want = append(want, k)
		}
	}
	for _, k := range linked {
		add(k)
	}
	for _, pr := range prs {
		if slices.ContainsFunc(o.PullSources, func(repo string) bool { return strings.EqualFold(repo, pr.Repo) }) {
			add(PullID{pr.Repo, pr.Number})
		}
	}
	if len(want) == 0 {
		return
	}
	for _, f := range o.Forges {
		read, err := f.Verdicts(ctx, want)
		for k, v := range read {
			if i, ok := index[lower(k)]; ok {
				prs[i].MergeState, prs[i].Review, prs[i].Checks = v.MergeState, v.Review, v.Checks
			}
		}
		if err == nil {
			continue
		}
		for i := range statuses {
			if s := &statuses[i]; s.Provider == f.Name() {
				s.Reason = strings.TrimPrefix(s.Reason+"; ", "; ") + "merge state, review, and checks: " + err.Error()
				if s.State == OK {
					s.State = Unavailable
				}
			}
		}
	}
}

// State is every tracked project's changes, keyed as Track was given them. A
// new pointer means something changed.
type State struct {
	Projects map[string]Data
}

// Tracker keeps projects' changes current on its own goroutine, never the UI
// loop (D14): local refs and worktrees every few seconds, which spawns no
// process unless the default branch moved, and forges every minute, or a few
// seconds after the last load while one is partial, still reading older pull
// requests. Each forge load reads the merge state, review, and checks of the
// pull requests last given to SetLinked; one that names a pull request the
// last load didn't read loads forges again.
type Tracker struct {
	projects map[string]Options
	latest   atomic.Pointer[State]
	updates  chan struct{}
	refresh  chan struct{}
	forged   chan struct{}
	linked   atomic.Pointer[[]PullID]
	relink   chan struct{}
}

const (
	localInterval   = 5 * time.Second
	forgeInterval   = time.Minute
	partialInterval = 5 * time.Second
)

// Track starts a tracker for projects, or returns nil when none reads
// anything.
func Track(ctx context.Context, projects map[string]Options) *Tracker {
	active := false
	for _, o := range projects {
		active = active || o.Git || len(o.Forges) > 0
	}
	if !active {
		return nil
	}
	t := &Tracker{projects: projects, updates: make(chan struct{}, 1), refresh: make(chan struct{}, 1), forged: make(chan struct{}, 1), relink: make(chan struct{}, 1)}
	go t.run(ctx)
	return t
}

// Latest is the newest state, nil before the first load.
func (t *Tracker) Latest() *State { return t.latest.Load() }

// Updates receives after each new state.
func (t *Tracker) Updates() <-chan struct{} { return t.updates }

// Forged receives after each per-minute forge refresh, whether or not
// anything changed, so a shown pull request's detail is read again on the
// same cadence. Refresh doesn't send: its caller reads what it shows itself.
func (t *Tracker) Forged() <-chan struct{} { return t.forged }

// Detail reads one change's files and commits, from Git or the forge among
// the tracked projects' that serves its pull request. A nil tracker reads
// only Git.
func (t *Tracker) Detail(ctx context.Context, c Change, patch bool) (Detail, error) {
	return LoadDetail(ctx, c, t.forges(), patch)
}

// Review reads a pull request change's review from the forge serving it.
func (t *Tracker) Review(ctx context.Context, c Change, fresh bool) (Review, error) {
	return LoadReview(ctx, c, t.forges(), fresh)
}

// Check reads one check's detail on c's forge.
func (t *Tracker) Check(ctx context.Context, c Change, check Check, fresh bool) (CheckDetail, error) {
	return LoadCheck(ctx, c, check, t.forges(), fresh)
}

// Enrich adds Git's and the forges' facts to one item's changes.
func (t *Tracker) Enrich(ctx context.Context, cs []Change, fresh bool) []Change {
	return Enrich(ctx, cs, t.forges(), fresh)
}

// Draft reads c's pending review from the state directory.
func (t *Tracker) Draft(c Change) (Draft, error) {
	return LoadDraft(t.stateDir(), c.URL)
}

// SaveDraft keeps d, or removes it once it has nothing left.
func (t *Tracker) SaveDraft(d Draft) error {
	return SaveDraft(t.stateDir(), d)
}

// Submit sends c's pending review with event and keeps what is left of it,
// with the outcome, in the state directory.
func (t *Tracker) Submit(ctx context.Context, c Change, event string) (Draft, error) {
	d, err := t.Draft(c)
	if err != nil {
		return d, err
	}
	// A draft that can't be saved after an unknown outcome could be sent
	// again, so its failure is always reported.
	d, err = SubmitReview(ctx, c, t.forges(), d, event)
	return d, errors.Join(err, t.SaveDraft(d))
}

// ResolveThread resolves or unresolves a thread of c's pull request.
func (t *Tracker) ResolveThread(ctx context.Context, c Change, thread string, resolve bool) error {
	return ResolveThread(ctx, c, t.forges(), thread, resolve)
}

func (t *Tracker) stateDir() string {
	if t != nil {
		for _, o := range t.projects {
			if o.StateDir != "" {
				return o.StateDir
			}
		}
	}
	return ""
}

func (t *Tracker) forges() []Forge {
	var forges []Forge
	if t != nil {
		for _, o := range t.projects {
			forges = append(forges, o.Forges...)
		}
	}
	return forges
}

// SetLinked names the open pull requests linked to unfinished items, active
// items' first, from the latest link pass. It is cheap enough for the UI
// loop: the tracker compares them on its own goroutine.
func (t *Tracker) SetLinked(pulls []PullID) {
	if t == nil {
		return
	}
	t.linked.Store(&pulls)
	select {
	case t.relink <- struct{}{}:
	default:
	}
}

// Refresh reloads local data and every forge now.
func (t *Tracker) Refresh() {
	select {
	case t.refresh <- struct{}{}:
	default:
	}
}

func (t *Tracker) run(ctx context.Context) {
	local := map[string]Data{}
	prs := map[string][]PullRequest{}
	forges := map[string][]Status{}
	loadLocal := func() {
		for key, o := range t.projects {
			local[key] = LoadLocal(ctx, o, local[key])
		}
	}
	// again fires partialInterval after a load that left a forge partial.
	var again <-chan time.Time
	// read is the linked pull requests the last load read.
	read := map[PullID]bool{}
	linked := func() []PullID {
		if pulls := t.linked.Load(); pulls != nil {
			return *pulls
		}
		return nil
	}
	loadForges := func() {
		partial := false
		pulls := linked()
		for key, o := range t.projects {
			prs[key], forges[key] = LoadForges(ctx, o.Forges)
			for _, status := range forges[key] {
				partial = partial || status.State == Partial
			}
			ReadVerdicts(ctx, o, prs[key], forges[key], pulls)
		}
		clear(read)
		for _, k := range pulls {
			read[PullID{strings.ToLower(k.Repo), k.Number}] = true
		}
		again = nil
		if partial {
			again = time.After(partialInterval)
		}
	}
	publish := func() {
		next := &State{Projects: make(map[string]Data, len(t.projects))}
		for key, d := range local {
			d.PullRequests = prs[key]
			d.Statuses = append(append([]Status(nil), d.Statuses...), forges[key]...)
			next.Projects[key] = d
		}
		if old := t.latest.Load(); old != nil && reflect.DeepEqual(old.Projects, next.Projects) {
			return
		}
		t.latest.Store(next)
		select {
		case t.updates <- struct{}{}:
		default:
		}
	}
	loadLocal()
	publish()
	loadForges()
	publish()
	localTick, forgeTick := time.NewTicker(localInterval), time.NewTicker(forgeInterval)
	defer localTick.Stop()
	defer forgeTick.Stop()
	for {
		ticked := false
		select {
		case <-ctx.Done():
			return
		case <-localTick.C:
			loadLocal()
		case <-forgeTick.C:
			loadForges()
			ticked = true
		case <-again:
			loadForges()
		case <-t.relink:
			if !slices.ContainsFunc(linked(), func(k PullID) bool { return !read[PullID{strings.ToLower(k.Repo), k.Number}] }) {
				continue
			}
			loadForges()
		case <-t.refresh:
			loadLocal()
			loadForges()
		}
		if ctx.Err() != nil {
			return
		}
		publish()
		if ticked {
			select {
			case t.forged <- struct{}{}:
			default:
			}
		}
	}
}
