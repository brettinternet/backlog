package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

// A provider attributes every comment to its token's user, so a remote comment
// needs no local author and must refuse one that names anyone else.
func TestCommentAuthorPerSource(t *testing.T) {
	t.Parallel()
	accounts := map[string][]project.AccountConfig{"github": {{Host: "github.com", User: "alice"}}}
	sources := []project.SourceConfig{{ID: "tasks", Type: "native"}, {ID: "gh", Type: "github", Account: "alice"}}
	for _, tc := range []struct {
		name, source, me, author, want, reason string
	}{
		{name: "remote default", source: "gh", want: "alice"},
		{name: "remote same user", source: "gh", author: "alice", want: "alice"},
		{name: "remote other author", source: "gh", me: "@bob", author: "@bob", reason: "rejected"},
		{name: "native me.default", source: "tasks", me: "@bob", want: "@bob"},
		{name: "native explicit", source: "tasks", me: "@bob", author: "@agent", want: "@agent"},
		{name: "native unset", source: "tasks", reason: "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := project.Config{Sources: sources, Accounts: accounts, Me: project.MeConfig{Default: tc.me}}
			changes := []model.Mutation{{Op: model.AppendComment, Comment: model.Comment{Author: tc.author, Body: "x"}}}
			got, err := ResolveChanges(context.Background(), cfg, tc.source, changes)
			if tc.reason != "" {
				if ErrorReason(err) != tc.reason {
					t.Fatalf("err = %v, want %s", err, tc.reason)
				}
				return
			}
			if err != nil || got[0].Comment.Author != tc.want {
				t.Fatalf("author = %v, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// A TUI session isn't an agent session: an agent recorded on its events would
// offer the TUI as resumable. Sync events have no caller at all.
func TestEventSessionOmitsAgentOutsideAgentCallers(t *testing.T) {
	t.Parallel()
	cfg := project.Config{Settings: project.Settings{Project: project.ProjectConfig{ID: "p"}, Sources: []project.SourceConfig{{ID: "tasks", Type: "native", Path: "items", Prefix: "BL"}}, Coordination: project.CoordinationConfig{Worklease: &struct{}{}}}}
	item := model.Item{Ref: model.Ref{Source: "tasks", ID: "BL-1"}}
	event := func(caller model.CallerContext, origin string) *hooks.Session {
		ctx := WithEventOrigin(WithCoordination(context.Background(), NewCoordinator(cfg), caller), origin)
		return makeEvent(ctx, cfg, item, "item.updated", origin, nil).Session
	}
	agent := model.CallerContext{Session: "s-1", Agent: "claude", AgentSession: "h-1"}
	if s := event(agent, "cli"); s == nil || s.Agent != "claude" || s.AgentSession != "h-1" || s.Host == "" {
		t.Fatalf("cli session: %+v", s)
	}
	tui := model.CallerContext{Session: "tui", PerItem: true, Agent: "claude", AgentSession: "h-1"}
	if s := event(tui, "tui"); s == nil || !strings.HasPrefix(s.ID, "tui:") || *s != (hooks.Session{ID: s.ID}) {
		t.Fatalf("tui session: %+v", s)
	}
	if s := event(agent, "sync"); s != nil {
		t.Fatalf("sync session: %+v", s)
	}
}

// A handoff needs a session but no claim, stays out of the item unless
// published, and a published comment must never carry the host, checkout,
// branch, or session IDs. A stale comment records nothing, and a source that
// can't comment still keeps the handoff locally.
func TestRecordHandoff(t *testing.T) {
	root := t.TempDir()
	cfg := project.Config{Settings: project.Settings{Project: project.ProjectConfig{ID: "p"}, Sources: []project.SourceConfig{{ID: "tasks", Type: "native", Path: filepath.Join(root, "items"), Prefix: "BL"},
		{ID: "lin", Type: "linear", Site: "11111111-1111-4111-8111-111111111111", Team: "22222222-2222-4222-8222-222222222222", Account: "test"}}, Statuses: []project.StatusConfig{{Key: "todo", Category: "todo"}}}}
	cfg.Accounts = map[string][]project.AccountConfig{"linear": {{Name: "test", User: "33333333-3333-4333-8333-333333333333", Env: "BL_HANDOFF_LINEAR_TOKEN"}}}
	cfg.Paths = project.Paths{StateDir: filepath.Join(root, "state")}
	cfg.Discovery = project.Discovery{ControlRoot: root, CheckoutRoot: "/secret/checkout"}
	cfg.Me.Default = "@me"
	t.Setenv("BL_HANDOFF_LINEAR_TOKEN", "unused")
	data, err := format.Serialize(model.Item{Ref: model.Ref{ID: "BL-1"}, Title: "One", Status: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "items"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "items", "BL-1.md"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := ReadDetail(context.Background(), cfg, "tasks:BL-1")
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	item := items[0]
	caller := model.CallerContext{Session: "secret-session", Agent: "claude", AgentSession: "secret-harness"}
	ctx := WithCoordination(context.Background(), nil, caller)
	if _, err := RecordHandoff(context.Background(), cfg, item, item.Version, "x"); ErrorReason(err) != "session-required" {
		t.Fatalf("no session: %v", err)
	}
	local, err := RecordHandoff(ctx, cfg, item, "ignored", "first")
	if err != nil || local.Event == "" || local.Comment != "" || local.Item.Version != item.Version {
		t.Fatalf("local: %+v %v", local, err)
	}
	cfg.History.Handoff = "comment"
	if _, err := RecordHandoff(ctx, cfg, item, "stale", "refused"); ErrorReason(err) != "conflict" {
		t.Fatalf("stale: %v", err)
	}
	posted, err := RecordHandoff(ctx, cfg, item, item.Version, "second")
	if err != nil || posted.Comment != "posted" || len(posted.Item.Comments) != 1 {
		t.Fatalf("posted: %+v %v", posted, err)
	}
	body := posted.Item.Comments[0].Body
	if !strings.HasPrefix(body, "Handoff from claude on ") || !strings.HasSuffix(body, "\n\nsecond") || strings.Contains(body, "secret") {
		t.Fatalf("comment body: %q", body)
	}
	remote := model.Item{Ref: model.Ref{Source: "lin", ID: "TEAM-1"}, Version: "v"}
	unsupported, err := RecordHandoff(ctx, cfg, remote, remote.Version, "third")
	if err != nil || unsupported.Comment != "unsupported" || unsupported.Event == "" {
		t.Fatalf("unsupported: %+v %v", unsupported, err)
	}
	events, err := hooks.New(cfg.Paths.StateDir, "p").ItemEvents(context.Background(), refString(item.Ref))
	if err != nil {
		t.Fatal(err)
	}
	sessions := foldSessions(events, nil, time.Now())
	if len(sessions) != 1 || sessions[0].Handoff != "second" || sessions[0].CWD != "/secret/checkout" {
		t.Fatalf("sessions: %+v", sessions)
	}
}
