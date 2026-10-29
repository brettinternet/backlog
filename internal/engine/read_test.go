package engine

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

type observationSource struct {
	source.Source
	delta source.Delta
}

func (s observationSource) Info() source.Info { return source.Info{ID: "test"} }
func (s observationSource) Load(context.Context, source.Cursor) (source.Delta, error) {
	return s.delta, nil
}

// A partial scan is not a source failure, but unknown coverage or a malformed
// row must still fail closed instead of authorizing selection from a bad load.
func TestReadObservationCoverage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		coverage source.Coverage
		findings []source.Finding
		wantErr  bool
	}{
		{name: "complete", coverage: source.Complete},
		{name: "partial", coverage: source.Partial},
		{name: "unknown", coverage: source.Unknown, wantErr: true},
		{name: "unspecified", wantErr: true},
		{name: "invalid-partial", coverage: source.Partial, findings: []source.Finding{{Severity: "error", Code: "invalid-frontmatter"}}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := observationSource{delta: source.Delta{Coverage: tc.coverage, Findings: tc.findings}}
			if _, err := loadObservation(context.Background(), backend, source.Cursor{}); (err != nil) != tc.wantErr {
				t.Fatalf("load error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}

// Events carry no sprint catalog: under ready.sprint, event readiness must keep
// sprint-source items ineligible (never ready for out-of-sprint work) while
// leaving native items in the same project ungated.
func TestGraphReadinessSprintGate(t *testing.T) {
	var cfg project.Config
	cfg.Ready = project.ReadyConfig{Statuses: []string{"todo"}, Sprint: "current"}
	cfg.Sources = []project.SourceConfig{{ID: "tasks", Type: "native"}, {ID: "eng", Type: "linear"}}
	native, linear := model.Ref{Source: "tasks", ID: "BL-1"}, model.Ref{Source: "eng", ID: "ENG-1"}
	got := graphReadiness(cfg, map[model.Ref]graphRecord{
		native: {Ref: native, Category: model.Todo},
		linear: {Ref: linear, Category: model.Todo},
	})
	if got[native].Readiness != Ready || got[linear].Readiness != Ineligible || got[linear].Reasons[0].Code != "sprint" {
		t.Fatalf("native %+v, linear %+v", got[native], got[linear])
	}
}

// Catches a session reported done for touching a closed item, a claim-only
// session (another host) dropped, a lapsed claim shown open, and an event-log
// failure hiding the claim history beside it.
func TestItemSessionsFold(t *testing.T) {
	t.Parallel()
	at := func(m int) time.Time { return time.Unix(int64(m)*60, 0) }
	todo, done := map[string]any{"category": "todo"}, map[string]any{"category": "done"}
	events := []hooks.Event{
		{Time: at(1), Type: "item.updated", Item: todo, Session: &hooks.Session{ID: "a", Agent: "claude", AgentSession: "h", CWD: "/repo"}},
		{Time: at(2), Type: "item.updated", Item: done, Changes: map[string]hooks.Change{"category": {From: "todo", To: "done"}}, Session: &hooks.Session{ID: "a", CWD: "/wt"}},
		{Time: at(3), Type: "item.updated", Item: done, Session: &hooks.Session{ID: "c"}},
		{Time: at(4), Type: "claim.released", Item: done, Session: &hooks.Session{ID: "c"}},
	}
	claims := []model.ClaimEpoch{
		{ID: "k1", Session: "b", Agent: "me@other", AcquiredAt: at(0), ExpiresAt: at(5)},
		{ID: "k2", Session: "c", AcquiredAt: at(3), EndedAt: at(4), EndReason: "released"},
	}
	got := foldSessions(events, claims, at(10))
	summary := []string{}
	for _, s := range got {
		summary = append(summary, s.ID+"|"+s.Agent+"|"+s.CWD+"|"+strings.Join(s.Phases, ",")+"|"+s.Ended)
	}
	if want := []string{"b||||lapsed", "a|claude|/wt|todo,done|done", "c|||done|released"}; !slices.Equal(summary, want) {
		t.Fatalf("sessions:\n%q\nwant\n%q", summary, want)
	}
	if got[0].Claim == nil || got[0].Claim.Agent != "me@other" || !got[0].LastSeen.Equal(at(0)) {
		t.Fatalf("claim-only session: %+v", got[0])
	}
	cfg := project.Config{Paths: project.Paths{StateDir: filepath.Join(t.TempDir(), "file")}}
	if err := os.WriteFile(cfg.Paths.StateDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, message := ItemSessions(context.Background(), cfg, model.Item{Ref: model.Ref{Source: "tasks", ID: "BL-1"}}, claims)
	if len(sessions) != 2 || !strings.HasPrefix(message, "event history unavailable: ") {
		t.Fatalf("failed read: %+v %q", sessions, message)
	}
}
