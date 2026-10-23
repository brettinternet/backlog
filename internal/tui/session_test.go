package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/external"
	"github.com/brettinternet/backlog/internal/source/remote"
)

type oauthDiagnosticSource struct{}

func (*oauthDiagnosticSource) Info() source.Info { return source.Info{ID: "remote"} }
func (*oauthDiagnosticSource) Load(context.Context, source.Cursor) (source.Delta, error) {
	return source.Delta{}, oauth.Failure(oauth.CodeSSORequired, time.Time{})
}
func (*oauthDiagnosticSource) Read(context.Context, string) (model.Item, error) {
	return model.Item{}, errors.New("unused")
}
func (*oauthDiagnosticSource) Apply(context.Context, model.Mutation) (model.Item, error) {
	return model.Item{}, errors.New("unused")
}

func TestOAuthDiagnosticVisibleInTUI(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := engine.OpenStore(ctx, project.Config{}, source.Registry{"remote": &oauthDiagnosticSource{}})
	defer store.Close()
	snapshots, unsubscribe := store.Subscribe()
	defer unsubscribe()
	m := New(Options{Context: ctx, Store: store, Sources: []string{"remote"}, NoColor: true})
	m.Bind(snapshots)
	for {
		msg := m.waitSnapshot()()
		if msg == nil {
			t.Fatal("timeout waiting for OAuth source error")
		}
		next, _ := m.Update(msg)
		m = next.(Model)
		if store.Current().Source("remote").Error != "" {
			if !strings.Contains(m.View().Content, "sso-required") {
				t.Fatalf("OAuth code not shown in TUI: %s", m.View().Content)
			}
			return
		}
	}
}

func TestExternalSilentChild(t *testing.T) {
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "silent-adapter" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
}

// A hung adapter must reach the visible source error badge, not leave the UI
// indefinitely in its loading state or block the healthy source's paint.
func TestExternalTimeoutErrorBadge(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	backend, err := external.New(external.Options{ID: "adapter", Run: []string{os.Args[0], "-test.run=^TestExternalSilentChild$", "--", "silent-adapter"}, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	store := engine.OpenStore(ctx, project.Config{}, source.Registry{"adapter": backend})
	defer store.Close()
	snapshots, unsubscribe := store.Subscribe()
	defer unsubscribe()
	m := New(Options{Context: ctx, Store: store, Sources: []string{"adapter"}, NoColor: true})
	m.Bind(snapshots)
	for {
		msg := m.waitSnapshot()()
		if msg == nil {
			t.Fatal("timeout waiting for adapter failure")
		}
		next, _ := m.Update(msg)
		m = next.(Model)
		if store.Current().Source("adapter").Error != "" && strings.Contains(m.View().Content, "! adapter:") {
			break
		}
	}
}

type shellSource struct{ loads atomic.Int32 }

func (s *shellSource) Info() source.Info { return source.Info{ID: "tasks"} }
func (s *shellSource) Load(context.Context, source.Cursor) (source.Delta, error) {
	n := s.loads.Add(1)
	return source.Delta{Coverage: source.Complete, Items: []model.Item{{Ref: model.Ref{Source: "tasks", ID: "BL-1"}, Title: fmt.Sprintf("loaded %d", n), Status: "todo", Category: model.Todo, Version: fmt.Sprint(n)}}}, nil
}
func (*shellSource) Read(context.Context, string) (model.Item, error) {
	return model.Item{}, errors.New("unused")
}
func (*shellSource) Apply(context.Context, model.Mutation) (model.Item, error) {
	return model.Item{}, errors.New("unused")
}

// Catches a dead subscription/command chain or R failing to request fresh data.
func TestShellLiveRefresh(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := project.Config{Settings: project.Settings{Statuses: []project.StatusConfig{{Key: "todo", Category: "todo"}}, Ready: project.ReadyConfig{Statuses: []string{"todo"}}}}
	backend := &shellSource{}
	store := engine.OpenStore(ctx, cfg, source.Registry{"tasks": backend})
	defer store.Close()
	projector := engine.NewProjector(ctx, cfg)
	defer projector.Close()
	ch, unsub := store.Subscribe()
	defer unsub()
	m := New(Options{Context: ctx, Store: store, Projector: projector, Sources: []string{"tasks"}, NoColor: true})
	m.Bind(ch)
	var transcript strings.Builder
	fmt.Fprintf(&transcript, "== first paint ==\n%s\n", m.View().Content)
	if !strings.Contains(m.View().Content, "Loading") {
		t.Fatal("no first loading paint")
	}
	load := func(want string) {
		t.Helper()
		for {
			msg := m.waitSnapshot()()
			if msg == nil {
				t.Fatal("snapshot timed out")
			}
			next, _ := m.Update(msg)
			m = next.(Model)
			p := m.waitProjection()()
			if p == nil {
				t.Fatal("projection timed out")
			}
			// Execute the preparation explicitly; no extra test goroutines or timers.
			projection := p.(projectionMsg)
			if projection.Error != nil {
				t.Fatal(projection.Error)
			}
			next, _ = m.Update(projection)
			m = next.(Model)
			next, _ = m.Update(m.prepare()())
			m = next.(Model)
			if strings.Contains(m.View().Content, want) {
				break
			}
		}
	}
	load("loaded 1")
	fmt.Fprintf(&transcript, "== loaded ==\n%s\n", m.View().Content)
	if !strings.Contains(m.View().Content, "✓ tasks") {
		t.Fatal("badge not updated")
	}
	m = send(m, "R")
	load("loaded 2")
	fmt.Fprintf(&transcript, "== refreshed ==\n%s\n", m.View().Content)
	golden(t, "live-refresh", transcript.String())
}

// This fake blocks only network refresh, not the real SQLite initial load.
type networkSource struct {
	shellSource
	gate    <-chan struct{}
	started chan struct{}
}

func (*networkSource) Info() source.Info { return source.Info{ID: "remote"} }
func (s *networkSource) Load(ctx context.Context, _ source.Cursor) (source.Delta, error) {
	if s.started != nil {
		close(s.started)
	}
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return source.Delta{}, ctx.Err()
		}
	}
	return source.Delta{Coverage: source.Complete, Cursor: source.Cursor{Value: "remote-cursor"}, Items: []model.Item{{Ref: model.Ref{Source: "remote", ID: "opaque-uuid"}, Title: "warm cached remote", Status: "todo", Category: model.Todo, Version: "remote-v1"}}}, nil
}

// Catches warm paint waiting on a network response and a slow source preventing
// another source from reaching the actual TUI subscription/projection/view chain.
func TestRemoteWarmPaintAndSourceIsolation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := project.Config{Settings: project.Settings{Statuses: []project.StatusConfig{{Key: "todo", Category: "todo"}}, Ready: project.ReadyConfig{Statuses: []string{"todo"}}}}
	cached := cache.New(t.TempDir(), "warm-test", "remote", "account/repo")
	backend := &networkSource{}
	wrapped := remote.New(backend, cached, &remote.Secrets{})
	if _, err := wrapped.Load(ctx, source.Cursor{}); err != nil {
		t.Fatal(err)
	}
	backend.gate, backend.started = make(chan struct{}), make(chan struct{})
	store := engine.OpenStore(ctx, cfg, source.Registry{"remote": wrapped, "tasks": &shellSource{}})
	defer store.Close()
	projector := engine.NewProjector(ctx, cfg)
	defer projector.Close()
	ch, unsubscribe := store.Subscribe()
	defer unsubscribe()
	m := New(Options{Context: ctx, Store: store, Projector: projector, Sources: []string{"remote", "tasks"}, NoColor: true})
	m.Bind(ch)
	// Cached rows are intentionally not ready; All is the warm-start view.
	m = send(m, "g", "t", "g", "t", "g", "t")
	for {
		msg := m.waitSnapshot()()
		if msg == nil {
			t.Fatalf("snapshot timed out before warm paint: %s\nitems=%+v", m.View().Content, store.Current().Items())
		}
		next, _ := m.Update(msg)
		m = next.(Model)
		projection := m.waitProjection()()
		if projection == nil {
			t.Fatal("projection timed out before warm paint")
		}
		next, _ = m.Update(projection)
		m = next.(Model)
		next, _ = m.Update(m.prepare()())
		m = next.(Model)
		view := m.View().Content
		if strings.Contains(view, "warm cached remote") && strings.Contains(view, "loaded 1") {
			break
		}
	}
	select {
	case <-backend.started:
	case <-ctx.Done():
		t.Fatal("network refresh never started")
	}
	if store.Current().Source("remote").Coverage == source.Complete {
		t.Fatal("cached rows presented as synced while network is blocked")
	}
	if !strings.Contains(m.View().Content, "✓ tasks") {
		t.Fatal("fast source update delayed by slow source")
	}
}
