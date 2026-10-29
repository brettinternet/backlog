package contact_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/brettinternet/backlog/internal/contact"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

func put(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

// Contact must reach live sessions without exposing secrets: Claude Code's
// key files stay unread, user resolvers get no shell and no credentials, and
// one slow or unknown source never hides another.
func TestResolve(t *testing.T) {
	home := testkit.Isolate(t)
	bin := filepath.Join(home, "bin")
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("GITHUB_TOKEN", "leak")
	put(t, filepath.Join(bin, "herdr"), `#!/bin/sh
echo '{"result":{"agents":[{"agent":"claude","agent_session":{"value":"s-1"},"agent_status":"blocked","pane_id":"w1:p2","title":"claude · work"},{"agent_session":{"value":"other"},"agent_status":"idle","pane_id":"w1:p3"}]}}'
`, 0o755)
	put(t, filepath.Join(bin, "probe"), `#!/bin/sh
printf '[{"channel":"probe","handle":"%s|%s","status":"busy"},{"channel":"probe","handle":"bad\\u001b]0;x\\u0007"}]' "$1" "$GITHUB_TOKEN"
`, 0o755)
	put(t, filepath.Join(bin, "slow"), "#!/bin/sh\nsleep 2\n", 0o755)
	claude := filepath.Join(home, "claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	put(t, filepath.Join(claude, "sessions", "10.json"), `{"pid":10,"sessionId":"s-1","name":"backlog-2b","status":"idle","kind":"interactive","peerProtocol":1,"messagingSocketPath":"/tmp/sock"}`, 0o644)
	put(t, filepath.Join(claude, "sessions", "10.abc.key"), "secret", 0o000)
	put(t, filepath.Join(claude, "sessions", "11.json"), `{"sessionId":"s-2","name":"future","peerProtocol":2}`, 0o644)

	host, _ := os.Hostname()
	sessions := []model.ItemSession{
		{ID: "loop", Agent: "claude", AgentSession: "s-1", Host: host},
		{ID: "away", Agent: "claude", AgentSession: "s-1", Host: "elsewhere"},
		{ID: "new", Agent: "claude", AgentSession: "s-2", Host: host},
		{ID: "codex", Agent: "codex", AgentSession: "s-3", Host: host},
	}
	user := map[string]project.ContactResolver{
		"probe": {Argv: []string{"probe", "{agentSession};touch pwned"}},
		"slow":  {Argv: []string{"slow"}},
	}
	got := contact.Resolve(context.Background(), user, sessions)
	herdr := model.ContactResult{Resolver: "herdr", Status: "ok", Channels: []model.ContactChannel{{Channel: "herdr", Handle: "w1:p2", Status: "waiting", Detail: "claude · work"}}}
	peer := model.ContactResult{Resolver: "claude-peer", Status: "ok", Channels: []model.ContactChannel{{Channel: "claude-peer", Handle: "backlog-2b", Status: "idle", Detail: "interactive"}}}
	probe := func(handle string) model.ContactResult {
		return model.ContactResult{Resolver: "probe", Status: "ok", Channels: []model.ContactChannel{{Channel: "probe", Handle: handle + ";touch pwned|", Status: "unknown"}}}
	}
	slow := model.ContactResult{Resolver: "slow", Status: "unavailable", Reason: "timeout"}
	away := func(name string) model.ContactResult {
		return model.ContactResult{Resolver: name, Status: "unavailable", Reason: "other host"}
	}
	want := [][]model.ContactResult{
		{herdr, peer, probe("s-1"), slow},
		{away("herdr"), away("claude-peer"), probe("s-1"), slow},
		{{Resolver: "herdr", Status: "ok"}, {Resolver: "claude-peer", Status: "unavailable", Reason: "unsupported version (peerProtocol 2)"}, probe("s-2"), slow},
		{{Resolver: "herdr", Status: "ok"}, probe("s-3"), slow},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contact:\n got %+v\nwant %+v", got, want)
	}

	// A user entry replaces or disables a built-in by name.
	user = map[string]project.ContactResolver{"claude-peer": {Disabled: true}, "herdr": {Argv: []string{"probe", "{cwd}"}}}
	got = contact.Resolve(context.Background(), user, sessions[:1])
	if want := [][]model.ContactResult{{{Resolver: "herdr", Status: "unavailable", Reason: "refused placeholder {cwd}: value is missing"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("overridden built-ins: %+v", got)
	}
}
