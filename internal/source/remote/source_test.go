package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
)

type provider struct {
	delta   source.Delta
	err     error
	cursors []source.Cursor
	item    model.Item
	writes  int
}

func (p *provider) Info() source.Info {
	return source.Info{ID: "remote", Capabilities: model.Capabilities{model.Set: true, model.Batch: true, model.AppendComment: true}}
}
func (p *provider) Load(_ context.Context, c source.Cursor) (source.Delta, error) {
	p.cursors = append(p.cursors, c)
	return p.delta, p.err
}
func (p *provider) Read(context.Context, string) (model.Item, error) { return p.item, p.err }
func (p *provider) Apply(_ context.Context, m model.Mutation) (model.Item, error) {
	p.writes++
	if p.item.Version != m.ExpectedVersion {
		return model.Item{}, source.Refuse("conflict", "provider rejected version")
	}
	if m.Op == model.AppendComment {
		comment := m.Comment
		if comment.Body == "" {
			comment.Body = m.Text
		}
		p.item.Comments = append(p.item.Comments, comment)
	} else {
		p.item.Title = m.Value.(string)
	}
	p.item.Version = "v2"
	return p.item, p.err
}
func row(id, status string) model.Item {
	return model.Item{Ref: model.Ref{Source: "remote", ID: id}, Title: id, Status: status, Category: model.StatusCategory(status), Version: "v1"}
}
func remoteStore(t *testing.T) cache.Store {
	t.Helper()
	return cache.New(t.TempDir(), "test-project", "remote", "https://host/account/repo")
}
func find(delta source.Delta, id string) bool {
	for _, item := range delta.Items {
		if item.Ref.ID == id {
			return true
		}
	}
	return false
}

// Catches incomplete enumeration evicting cached rows, a failed load advancing
// the cursor, and a restart forgetting rows or using the wrong cursor.
func TestRemoteCoverageCursorAndWarmCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := remoteStore(t)
	p := &provider{delta: source.Delta{Items: []model.Item{row("A", "done"), row("B", "todo")}, Coverage: source.Complete, Cursor: source.Cursor{Value: "cursor-1"}}}
	s := New(p, store, &Secrets{})
	if _, err := s.Load(ctx, source.Cursor{}); err != nil {
		t.Fatal(err)
	}
	p.delta = source.Delta{Items: []model.Item{row("B", "todo")}, Coverage: source.Partial, Cursor: source.Cursor{Value: "cursor-2"}}
	partial, err := s.Load(ctx, source.Cursor{})
	if err != nil || !find(partial, "A") || !partial.Stale[row("A", "done").Ref] {
		t.Fatalf("lost or fresh omitted row: %+v %v", partial, err)
	}
	// A successful change window preserves fresh and stale rows across restart,
	// even though the overall enumeration remains partial.
	p.delta = source.Delta{Coverage: source.Partial, Incremental: true, RetainUnseen: true, Cursor: source.Cursor{Value: "cursor-2"}}
	retained, err := New(p, store, &Secrets{}).Load(ctx, source.Cursor{})
	if err != nil || len(retained.Items) != 2 || !retained.Stale[row("A", "done").Ref] || retained.Stale[row("B", "todo").Ref] {
		t.Fatalf("change window altered cached freshness: %+v %v", retained, err)
	}
	p.err = errors.New("failed scan")
	p.delta.Cursor.Value = "uncommitted"
	if _, err = s.Load(ctx, source.Cursor{}); err == nil {
		t.Fatal("failed load succeeded")
	}
	restarted := New(p, store, &Secrets{})
	warm, err := restarted.LoadInitial(ctx)
	if err != nil || len(warm.Items) != 2 || len(warm.Stale) != 2 || warm.Cursor.Value != "cursor-2" || warm.Coverage == source.Complete {
		t.Fatalf("unsafe warm cache: %+v %v", warm, err)
	}
	p.err = nil
	p.delta = source.Delta{Items: []model.Item{row("B", "todo")}, Coverage: source.Complete, Cursor: source.Cursor{Value: "cursor-3"}}
	complete, err := restarted.Load(ctx, source.Cursor{})
	if err != nil || find(complete, "A") || p.cursors[len(p.cursors)-1].Value != "cursor-2" {
		t.Fatalf("cursor/deletion: %+v cursors=%+v %v", complete, p.cursors, err)
	}
	// A complete incremental response validates retained rows but cannot evict them.
	p.delta = source.Delta{Coverage: source.Complete, Incremental: true, Cursor: source.Cursor{Value: "cursor-4"}}
	incremental, err := s.Load(ctx, source.Cursor{})
	if err != nil || !find(incremental, "B") || incremental.Stale[row("B", "todo").Ref] || incremental.Coverage != source.Complete {
		t.Fatalf("incremental lost rows: %+v %v", incremental, err)
	}
}

// Catches warm starts decoding the entire backlog instead of bounded batches.
func TestRemoteWarmCacheBounded(t *testing.T) {
	t.Parallel()
	p := &provider{delta: source.Delta{Coverage: source.Complete, Cursor: source.Cursor{Value: "large-cursor"}}}
	for index := range 1000 {
		p.delta.Items = append(p.delta.Items, row(fmt.Sprint(index), "todo"))
	}
	s := New(p, remoteStore(t), &Secrets{})
	if _, err := s.Load(context.Background(), source.Cursor{}); err != nil {
		t.Fatal(err)
	}
	initial, err := s.LoadInitial(context.Background())
	if err != nil || len(initial.Items) != 256 || len(initial.Stale) != 256 || initial.Cursor.Value != "large-cursor" {
		t.Fatalf("unbounded or unusable initial snapshot: items=%d stale=%d cursor=%s err=%v", len(initial.Items), len(initial.Stale), initial.Cursor.Value, err)
	}
}

func BenchmarkRemoteWarmCache10k(b *testing.B) {
	p := &provider{delta: source.Delta{Coverage: source.Complete}}
	for index := range 10_000 {
		item := row(fmt.Sprint(index), "todo")
		item.Description = strings.Repeat("Remote backlog item body with labels and dependencies. ", 40)
		p.delta.Items = append(p.delta.Items, item)
	}
	s := New(p, cache.New(b.TempDir(), "benchmark", "remote", "account/repo"), &Secrets{})
	if _, err := s.Load(context.Background(), source.Cursor{}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if delta, err := s.LoadInitial(context.Background()); err != nil || len(delta.Items) != 256 {
			b.Fatalf("initial load: items=%d %v", len(delta.Items), err)
		}
	}
}

// Catches a missing provider match changing query results and a damaged cache
// becoming required for correctness. The text seam is the exercised pushdown.
func TestRemotePushdownParityAndDisposableCache(t *testing.T) {
	t.Parallel()
	p := &provider{delta: source.Delta{Coverage: source.Complete, Items: []model.Item{row("match", "todo"), row("other", "todo")}}}
	store := remoteStore(t)
	s := New(p, store, &Secrets{})
	for _, omit := range []bool{false, true} {
		p.delta.TextSearch = func(context.Context, string) (map[model.Ref]bool, error) {
			refs := map[model.Ref]bool{row("other", "todo").Ref: true}
			if !omit {
				refs[row("match", "todo").Ref] = true
			}
			return refs, nil
		}
		delta, err := s.Load(context.Background(), source.Cursor{})
		if err != nil {
			t.Fatal(err)
		}
		matches, err := delta.TextSearch(context.Background(), "match")
		if err != nil {
			t.Fatal(err)
		}
		q, err := query.Parse("text:match", query.Options{})
		if err != nil {
			t.Fatal(err)
		}
		snapshot := query.Snapshot{Items: delta.Items}
		if matches != nil {
			snapshot.TextMatches = map[string]map[model.Ref]bool{"match": matches}
		}
		for _, item := range delta.Items {
			if q.Match(item, snapshot, nil) != (item.Ref.ID == "match") {
				t.Fatal("pushdown changed local result")
			}
		}
	}
	if err := os.WriteFile(store.Path, []byte("damaged disposable cache"), 0600); err != nil {
		t.Fatal(err)
	}
	delta, err := s.Load(context.Background(), source.Cursor{})
	if err != nil || len(delta.Items) != 2 || p.cursors[len(p.cursors)-1].Value != "" {
		t.Fatalf("damaged cache prevented fresh enumeration: %+v %v", delta, err)
	}
}

// A malicious/erroring provider can reflect credentials anywhere in its model.
// Verify the actual outgoing event/hook payload and on-disk cache, not mocks.
func TestCredentialCanaryAcrossSourceSinks(t *testing.T) {
	t.Parallel()
	const canary = "credential-canary-7bd9f1"
	secrets := &Secrets{}
	secrets.add(canary)
	item := row("A", "todo")
	item.Title, item.Description, item.URL = canary, canary, "https://host/"+canary
	item.Fields = map[string]any{"raw": []any{canary, map[string]any{"nested": canary}}}
	store := remoteStore(t)
	p := &provider{item: item, delta: source.Delta{Items: []model.Item{item}, Coverage: source.Complete, Findings: []source.Finding{{Message: canary, Path: canary}}}}
	s := New(p, store, secrets)
	delta, err := s.Load(context.Background(), source.Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(delta.Items[0]) // event.Item and hook stdin use this same model
	if strings.Contains(string(payload), canary) || strings.Contains(fmt.Sprint(delta.Findings), canary) {
		t.Fatal("credential reached an event/hook payload")
	}
	if err := filepath.WalkDir(filepath.Dir(store.Path), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if strings.Contains(string(data), canary) {
			return fmt.Errorf("credential persisted")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p.err = fmt.Errorf("provider echoed %s", canary)
	_, err = s.Read(context.Background(), "A")
	if err == nil || strings.Contains(fmt.Sprint(err), canary) {
		t.Fatalf("unsafe error: %v", err)
	}
	_, err = s.Load(context.Background(), source.Cursor{})
	if err == nil || strings.Contains(fmt.Sprint(err), canary) {
		t.Fatalf("unsafe load error: %v", err)
	}
	p.err = nil
	p.delta.Cursor.Value = canary
	if _, err = s.Load(context.Background(), source.Cursor{}); err == nil || strings.Contains(fmt.Sprint(err), canary) {
		t.Fatalf("credential-bearing cursor accepted: %v", err)
	}
}

type activityProvider struct {
	provider
	activity []model.Activity
	calls    int
}

func (p *activityProvider) Info() source.Info {
	return source.Info{ID: "remote", Capabilities: model.Capabilities{model.ReadActivity: true}}
}
func (p *activityProvider) Activity(context.Context, model.Item) ([]model.Activity, bool, error) {
	p.calls++
	return p.activity, false, p.err
}

// Activity is read once per item version, redacted before it is returned or
// cached and again on a cache hit for secrets registered since, and a quota
// refusal keeps its retry time for the status line.
func TestRemoteActivityCachedByVersion(t *testing.T) {
	t.Parallel()
	const canary = "credential-canary-activity"
	secrets := &Secrets{}
	secrets.add(canary)
	const later = "credential-canary-registered-later"
	p := &activityProvider{activity: []model.Activity{{ID: "1", Summary: "labeled " + canary + " " + later}}}
	store := remoteStore(t)
	s := New(p, store, secrets)
	item := row("A", "todo")
	for n := range 2 {
		activity, _, err := s.Activity(context.Background(), item)
		if err != nil || len(activity) != 1 || strings.Contains(activity[0].Summary, canary) || n > 0 && strings.Contains(activity[0].Summary, later) {
			t.Fatalf("activity %+v: %v", activity, err)
		}
		secrets.add(later)
	}
	if p.calls != 1 {
		t.Fatalf("provider read %d times for one version", p.calls)
	}
	cached, _ := filepath.Glob(strings.TrimSuffix(store.Path, ".sqlite") + ".activity/*.json")
	if len(cached) != 1 {
		t.Fatalf("activity cache files %v", cached)
	}
	if data, err := os.ReadFile(cached[0]); err != nil || strings.Contains(string(data), canary) {
		t.Fatalf("cached activity unsafe: %v", err)
	}
	item.Version = "v2"
	retry := time.Now().Add(time.Minute)
	p.err = ScheduleDiagnostic{Code: "rate-limited", RetryAt: retry}
	var limited ScheduleDiagnostic
	if _, _, err := s.Activity(context.Background(), item); !errors.As(err, &limited) || !limited.RetryAt.Equal(retry) || p.calls != 2 {
		t.Fatalf("new version: %v after %d calls", err, p.calls)
	}
	if _, _, err := New(&provider{}, store, secrets).Activity(context.Background(), item); err == nil {
		t.Fatal("source without read-activity returned activity")
	}
}

// Comments must prepare for TUI edits and before hooks without fabricating an
// inverse that could replay an append. Preserve author/time and Text fallback.
func TestRemoteCommentPreparationAndApply(t *testing.T) {
	t.Parallel()
	for _, useText := range []bool{false, true} {
		t.Run(fmt.Sprint("text=", useText), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			p := &provider{item: row("A", "todo")}
			p.item.Comments = []model.Comment{{Author: "original", Body: "Existing"}}
			s := New(p, remoteStore(t), &Secrets{})
			comment := model.Comment{Author: "alice", Body: "Progress", Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			m := model.Mutation{Ref: p.item.Ref, ExpectedVersion: p.item.Version, Op: model.AppendComment, Comment: comment, Text: "ignored fallback"}
			if useText {
				m.Comment.Body, m.Text = "", comment.Body
			}
			prepared, err := s.Prepare(ctx, m)
			if err != nil || len(prepared.Item.Comments) != 2 || prepared.Item.Comments[1] != comment || prepared.Inverse.Op != "" {
				t.Fatalf("prepare: %+v %v", prepared, err)
			}
			if p.writes != 0 || len(p.item.Comments) != 1 {
				t.Fatal("preparation mutated provider")
			}
			// A batch containing a comment must not expose a partial inverse.
			batch := m
			batch.Op, batch.Changes = model.Batch, []model.Mutation{{Op: model.Set, Field: "title", Value: "Edited"}, m}
			batched, err := s.Prepare(ctx, batch)
			if err != nil || batched.Item.Title != "Edited" || len(batched.Item.Comments) != 2 || batched.Inverse.Op != "" {
				t.Fatalf("batch preparation: %+v %v", batched, err)
			}
			after, err := s.Apply(ctx, m)
			if err != nil || len(after.Comments) != 2 || after.Comments[1] != comment || p.writes != 1 {
				t.Fatalf("append: %+v %v writes=%d", after, err, p.writes)
			}
			if _, err = s.Prepare(ctx, m); err == nil {
				t.Fatal("stale comment preparation accepted")
			}
		})
	}
}

// Catches remote writes reaching the provider with a stale version and confirms
// generic preparation yields a provider-level inverse, not native file bytes.
func TestRemoteVersionedWrite(t *testing.T) {
	t.Parallel()
	p := &provider{item: row("A", "todo")}
	s := New(p, remoteStore(t), &Secrets{})
	m := model.Mutation{Ref: p.item.Ref, ExpectedVersion: "v1", Op: model.Set, Field: "title", Value: "Edited"}
	prepared, err := s.Prepare(context.Background(), m)
	if err != nil || prepared.Item.Title != "Edited" || prepared.Inverse.Op != model.Set || prepared.Inverse.Value != "A" {
		t.Fatalf("prepare: %+v %v", prepared, err)
	}
	if _, err = s.Apply(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(context.Background(), m); err == nil || p.writes != 1 {
		t.Fatalf("stale write reached provider: %v calls=%d", err, p.writes)
	}
}

// ReadDetail skips a source only on not-found or invalid-id; masking either as
// unavailable breaks unqualified lookups across sources.
func TestRemoteReadKeepsAbsentReasons(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"not-found", "invalid-id"} {
		p := &provider{err: source.Refuse(want, "provider detail")}
		_, err := New(p, remoteStore(t), &Secrets{}).Read(context.Background(), "BL-1")
		var refusal *source.Error
		if !errors.As(err, &refusal) || refusal.Reason != want {
			t.Fatalf("%s: %v", want, err)
		}
	}
}
