//go:build unix

package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/hooks"
)

// Catches an agent command that runs through a shell, outside the recorded
// checkout, or with the wrong session; a refusal that hides its reason; a
// closed item that extends a finished transcript; and a failure that hides
// the agent's exit status and stderr.
func TestResumeRecordedSessions(t *testing.T) {
	root := writeFixture(t)
	enableCoordination(t, root)
	// Only session "held" still holds its claim.
	script := `#!/bin/sh
case "$2" in
profile) printf '%s\n' '{"schemaVersion":2,"ok":true,"profile":{"builtin":true,"name":"local"}}';;
handle) printf '%s\n' '{"schemaVersion":2,"ok":false,"error":{"reason":"handle-malformed","message":"handle is missing"}}'; exit 2;;
history) printf '%s\n' '{"schemaVersion":2,"ok":true,"epochs":[{"claimId":"c1","sessionId":"held","acquiredAt":"2026-10-21T00:00:00Z"}]}';;
list) printf '%s\n' '{"schemaVersion":2,"ok":true,"claims":[{"claimId":"c1","sessionId":"held","expiresAt":"2099-01-01T00:00:00Z"}]}';;
*) exit 1;;
esac
`
	bin := t.TempDir()
	put(t, filepath.Join(bin, "worklease"), script)
	if err := os.Chmod(filepath.Join(bin, "worklease"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	work, _ := filepath.EvalSymlinks(t.TempDir())
	ran := filepath.Join(t.TempDir(), "ran")
	cfg := eventConfig(t, root)
	// $0 and $1 stay literal: a shell would have expanded them in argv.
	probe := `printf '%s|%s|%s|%s|%s\n' "$1" "$(pwd -P)" "$2" "$3" "$4" >> "$0"`
	if err := os.MkdirAll(filepath.Dir(cfg.Paths.ConfigFile), 0700); err != nil {
		t.Fatal(err)
	}
	agents, _ := json.Marshal(map[string]any{"agents": map[string]any{
		"probe": map[string]any{
			"resume": []string{"/bin/sh", "-c", probe, ran, "resume", "{agentSession}", "{session}", "{host}"},
			"fork":   []string{"/bin/sh", "-c", probe, ran, "fork", "{agentSession}", "{session}", "$HOME"},
		},
		"failing": map[string]any{"resume": []string{"/bin/sh", "-c", "echo boom >&2; exit 3"}},
	}, "contact": map[string]any{
		// Session "running" holds no claim, but its agent still answers.
		"pane":        []string{"/bin/sh", "-c", `case "$0" in a-running|a-held) printf '[\173"channel":"pane","handle":"p1","status":"idle"\175]';; *) echo '[]';; esac`, "{agentSession}"},
		"herdr":       false,
		"claude-peer": false,
	}})
	put(t, cfg.Paths.ConfigFile, string(agents))
	host, _ := os.Hostname()
	sessions := []hooks.Session{
		{ID: "ok", Agent: "probe", AgentSession: "a-ok", Host: host, CWD: work},
		{ID: "gone", Agent: "probe", AgentSession: "a-gone", Host: host, CWD: filepath.Join(work, "removed")},
		{ID: "remote", Agent: "probe", AgentSession: "a-remote", Host: "elsewhere", CWD: work},
		{ID: "unknown", Agent: "other", AgentSession: "a-unknown", Host: host, CWD: work},
		{ID: "held", Agent: "probe", AgentSession: "a-held", Host: host, CWD: work},
		{ID: "running", Agent: "probe", AgentSession: "a-running", Host: host, CWD: work},
		{ID: "fail", Agent: "failing", AgentSession: "a-fail", Host: host, CWD: work},
		{ID: "tui:me", Agent: "probe", AgentSession: "a-tui", Host: host, CWD: work},
	}
	err := hooks.New(cfg.Paths.StateDir, cfg.Project.ID).Update(context.Background(), func(tx *hooks.Transaction) error {
		for _, s := range sessions {
			if _, err := tx.Append(hooks.Event{Type: "item.updated", Ref: "tasks:BL-012", Source: "tasks", Versions: []string{s.ID}, Session: &s}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	code, out, _ := writeRun(root, "", "resume", "12")
	if code != 2 || !strings.Contains(out, "several resumable sessions") || strings.Contains(out, "tui:me") {
		t.Fatalf("ambiguous: %d %s", code, out)
	}
	var ambiguous struct {
		Error struct {
			Details struct {
				Candidates []struct{ Session struct{ ID string } }
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &ambiguous); err != nil || len(ambiguous.Error.Details.Candidates) != 7 {
		t.Fatalf("candidates: %v %s", err, out)
	}
	for session, want := range map[string]string{
		"gone":    `"reason":"checkout-removed","message":"checkout no longer exists: ` + filepath.Join(work, "removed"),
		"remote":  `"reason":"other-host"`,
		"a-held":  `"reason":"live","message":"session still holds its claim; message it instead via pane p1`,
		"running": `"reason":"live","message":"agent is still running; message it instead via pane p1`,
		"unknown": `"reason":"no-agent-config"`,
	} {
		if code, out, _ := writeRun(root, "", "resume", "12", session); code != 4 || !strings.Contains(out, want) {
			t.Fatalf("%s: %d %s", session, code, out)
		}
	}
	if _, err := os.Stat(ran); !os.IsNotExist(err) {
		t.Fatal("a refused session ran")
	}

	code, out, _ = writeRun(root, "", "resume", "12", "fail")
	if code != 5 || !strings.Contains(out, "resume failed (exit 3): boom") {
		t.Fatalf("failure: %d %s", code, out)
	}
	actionOK(t, root, "resume", "12", "a-ok")
	actionOK(t, root, "resume", "12", "ok", "--fork")
	item := filepath.Join(root, "items/BL-012.md")
	put(t, item, strings.Replace(get(t, item), "status: todo", "status: done", 1))
	actionOK(t, root, "resume", "12", "ok")
	actionOK(t, root, "resume", "12", "ok", "--resume")
	want := strings.Join([]string{
		"resume|" + work + "|a-ok|ok|" + host,
		"fork|" + work + "|a-ok|ok|$HOME",
		"fork|" + work + "|a-ok|ok|$HOME",
		"resume|" + work + "|a-ok|ok|" + host,
	}, "\n") + "\n"
	if got := get(t, ran); got != want {
		t.Fatalf("agent runs:\n%s\nwant:\n%s", got, want)
	}

	var finished []string
	for _, event := range readEvents(t, root) {
		if event.Type == "action.finished" {
			if event.Session == nil || event.Session.AgentSession == "" {
				t.Fatalf("finish lacks session: %+v", event)
			}
			finished = append(finished, event.Name+":"+event.Session.ID+":"+event.Outcome)
		}
	}
	if got := strings.Join(finished, " "); got != "resume:fail:failed resume:ok:ok fork:ok:ok fork:ok:ok resume:ok:ok" {
		t.Fatalf("finished events: %s", got)
	}
}
