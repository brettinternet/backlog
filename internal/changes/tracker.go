package changes

import (
	"context"
	"reflect"
	"sync/atomic"
	"time"
)

// Load reads one project's local changes and every forge once, as bl changes
// and MCP do.
func Load(ctx context.Context, o Options) Data {
	d := LoadLocal(ctx, o, Data{})
	prs, statuses := LoadForges(ctx, o.Forges)
	d.PullRequests, d.Statuses = prs, append(d.Statuses, statuses...)
	return d
}

// LoadForges lists every forge's pull requests. A forge that fails keeps
// what it last had, marked stale, so its pull requests read as stale rather
// than vanishing or looking current.
func LoadForges(ctx context.Context, forges []Forge) ([]PullRequest, []Status) {
	var prs []PullRequest
	var statuses []Status
	for _, f := range forges {
		list, err := f.PullRequests(ctx)
		status := Status{Provider: f.Name(), State: OK}
		if err != nil {
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

// State is every tracked project's changes, keyed as Track was given them. A
// new pointer means something changed.
type State struct {
	Projects map[string]Data
}

// Tracker keeps projects' changes current on its own goroutine, never the UI
// loop (D14): local refs and worktrees every few seconds, which spawns no
// process unless the default branch moved, and forges every minute.
type Tracker struct {
	projects map[string]Options
	latest   atomic.Pointer[State]
	updates  chan struct{}
	refresh  chan struct{}
}

const (
	localInterval = 5 * time.Second
	forgeInterval = time.Minute
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
	t := &Tracker{projects: projects, updates: make(chan struct{}, 1), refresh: make(chan struct{}, 1)}
	go t.run(ctx)
	return t
}

// Latest is the newest state, nil before the first load.
func (t *Tracker) Latest() *State { return t.latest.Load() }

// Updates receives after each new state.
func (t *Tracker) Updates() <-chan struct{} { return t.updates }

// Detail reads one change's files and commits, from Git or the forge among
// the tracked projects' that serves its pull request. A nil tracker reads
// only Git.
func (t *Tracker) Detail(ctx context.Context, c Change) (Detail, error) {
	var forges []Forge
	if t != nil {
		for _, o := range t.projects {
			forges = append(forges, o.Forges...)
		}
	}
	return LoadDetail(ctx, c, forges)
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
	loadForges := func() {
		for key, o := range t.projects {
			prs[key], forges[key] = LoadForges(ctx, o.Forges)
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
		select {
		case <-ctx.Done():
			return
		case <-localTick.C:
			loadLocal()
		case <-forgeTick.C:
			loadForges()
		case <-t.refresh:
			loadLocal()
			loadForges()
		}
		if ctx.Err() != nil {
			return
		}
		publish()
	}
}
