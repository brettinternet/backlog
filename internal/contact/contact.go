// Package contact looks up how to reach a recorded agent session through the
// channels already running beside it. Contact is resolved when a session is
// read and never recorded, and bl never sends messages itself (design.md D40).
package contact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/proctree"
	"github.com/brettinternet/backlog/internal/project"
)

// Budget bounds one Resolve call. A resolver still running when it ends
// reports unavailable: timeout.
const Budget = 100 * time.Millisecond

// outputLimit bounds what bl reads from one resolver or session file.
const outputLimit = 64 << 10

// builtins run in this order, before user resolvers in name order.
var builtins = []string{"herdr", "claude-peer"}

// Resolve returns each session's contact results, one per applicable
// resolver. Resolvers are independent: each reports ok or unavailable with a
// reason, and one failure never hides another. user adds argv resolvers by
// name, replaces a built-in of the same name, or disables one with false.
func Resolve(ctx context.Context, user map[string]project.ContactResolver, sessions []model.ItemSession) [][]model.ContactResult {
	ctx, cancel := context.WithTimeout(ctx, Budget)
	defer cancel()
	host, _ := os.Hostname()
	var names []string
	for _, name := range builtins {
		if _, set := user[name]; !set {
			names = append(names, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(user)) {
		if user[name].Argv != nil {
			names = append(names, name)
		}
	}
	herdr := sync.OnceValues(func() ([]herdrAgent, error) { return listHerdr(ctx) })
	peers := sync.OnceValues(func() ([]claudePeer, error) { return listClaudePeers() })

	type slot struct{ session, resolver int }
	type answer struct {
		slot
		result model.ContactResult
	}
	var slots []slot
	for i, s := range sessions {
		for j, name := range names {
			if name == "claude-peer" && user[name].Argv == nil && s.Agent != "claude" {
				continue
			}
			slots = append(slots, slot{i, j})
		}
	}
	answers := make(chan answer, len(slots))
	for _, sl := range slots {
		go func() {
			s, name := sessions[sl.session], names[sl.resolver]
			var result model.ContactResult
			switch r := user[name]; {
			case r.Argv != nil:
				result = runUser(ctx, r.Argv, s)
			case s.Host != host:
				result = unavailable("other host")
			case name == "herdr":
				result = herdrResult(herdr, s.AgentSession)
			default:
				result = peerResult(peers, s.AgentSession)
			}
			result.Resolver = name
			answers <- answer{sl, result}
		}()
	}
	got := map[slot]model.ContactResult{}
	for len(got) < len(slots) {
		select {
		case a := <-answers:
			got[a.slot] = a.result
		case <-ctx.Done():
			for _, sl := range slots {
				if _, ok := got[sl]; !ok {
					r := unavailable("timeout")
					r.Resolver = names[sl.resolver]
					got[sl] = r
				}
			}
		}
	}
	out := make([][]model.ContactResult, len(sessions))
	for _, sl := range slots {
		out[sl.session] = append(out[sl.session], got[sl])
	}
	return out
}

func unavailable(reason string) model.ContactResult {
	return model.ContactResult{Status: "unavailable", Reason: reason}
}

func ok(channels []model.ContactChannel) model.ContactResult {
	return model.ContactResult{Status: "ok", Channels: channels}
}

type herdrAgent struct {
	Agent        string `json:"agent"`
	AgentSession struct {
		Value string `json:"value"`
	} `json:"agent_session"`
	Status string `json:"agent_status"`
	PaneID string `json:"pane_id"`
	Title  string `json:"title"`
}

var errNotInstalled = errors.New("herdr not installed")

// listHerdr asks the local Herdr server for its agents. It starts in the
// minimal environment plus the socket path of the Herdr session bl runs in.
func listHerdr(ctx context.Context) ([]herdrAgent, error) {
	path, err := exec.LookPath("herdr")
	if err != nil {
		return nil, errNotInstalled
	}
	cmd := exec.CommandContext(ctx, path, "agent", "list")
	cmd.Env = hooks.MinimalEnvironment()
	if socket, set := os.LookupEnv("HERDR_SOCKET_PATH"); set {
		cmd.Env = append(cmd.Env, "HERDR_SOCKET_PATH="+socket)
	}
	data, err := output(cmd)
	if err != nil {
		return nil, err
	}
	var listed struct {
		Result struct {
			Agents []herdrAgent `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &listed); err != nil {
		return nil, errors.New("unreadable herdr agent list")
	}
	return listed.Result.Agents, nil
}

func herdrResult(list func() ([]herdrAgent, error), agentSession string) model.ContactResult {
	agents, err := list()
	if errors.Is(err, errNotInstalled) {
		return unavailable(err.Error())
	}
	if err != nil {
		return unavailable("herdr not running")
	}
	var channels []model.ContactChannel
	for _, a := range agents {
		if a.AgentSession.Value == agentSession && a.PaneID != "" {
			status := map[string]string{"working": "working", "idle": "idle", "done": "idle", "blocked": "waiting"}[a.Status]
			channels = appendChannel(channels, model.ContactChannel{Channel: "herdr", Handle: a.PaneID, Status: status, Detail: a.Title})
		}
	}
	return ok(channels)
}

// claudePeer holds the only fields bl reads from Claude Code's undocumented
// session files. Key files and messaging sockets are never opened.
type claudePeer struct {
	SessionID    string `json:"sessionId"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Kind         string `json:"kind"`
	PeerProtocol int    `json:"peerProtocol"`
}

// peerProtocol is the Claude Code peer protocol version this resolver knows.
const peerProtocol = 1

func listClaudePeers() ([]claudePeer, error) {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".claude")
	}
	files, err := filepath.Glob(filepath.Join(dir, "sessions", "*.json"))
	if err != nil {
		return nil, err
	}
	var peers []claudePeer
	for _, file := range files {
		data, err := readLimited(file)
		if err != nil {
			continue // the session exited while being listed
		}
		var p claudePeer
		if json.Unmarshal(data, &p) == nil && p.SessionID != "" {
			peers = append(peers, p)
		}
	}
	return peers, nil
}

func peerResult(list func() ([]claudePeer, error), agentSession string) model.ContactResult {
	peers, err := list()
	if err != nil {
		return unavailable("claude sessions unreadable")
	}
	var channels []model.ContactChannel
	for _, p := range peers {
		if p.SessionID != agentSession {
			continue
		}
		if p.PeerProtocol != peerProtocol {
			return unavailable("unsupported version (peerProtocol " + strconv.Itoa(p.PeerProtocol) + ")")
		}
		if p.Name != "" {
			status := map[string]string{"busy": "working", "shell": "working", "idle": "idle", "waiting": "waiting"}[p.Status]
			channels = appendChannel(channels, model.ContactChannel{Channel: "claude-peer", Handle: p.Name, Status: status, Detail: p.Kind})
		}
	}
	return ok(channels)
}

// runUser runs a user resolver's argv without a shell in the minimal
// environment, under the shared budget, and reads channel JSON from stdout.
func runUser(ctx context.Context, argv []string, s model.ItemSession) model.ContactResult {
	argv, err := hooks.ExpandArgv(argv, map[string]string{"agent": s.Agent, "agentSession": s.AgentSession,
		"session": s.ID, "host": s.Host, "cwd": s.CWD})
	if err != nil {
		return unavailable(err.Error())
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = hooks.MinimalEnvironment()
	data, err := output(cmd)
	if ctx.Err() != nil {
		return unavailable("timeout")
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return unavailable("exit " + strconv.Itoa(exit.ExitCode()))
		}
		return unavailable(err.Error())
	}
	var printed []model.ContactChannel
	if err := json.Unmarshal(data, &printed); err != nil {
		return unavailable("output is not a channel JSON array")
	}
	var channels []model.ContactChannel
	for _, c := range printed {
		if c.Channel != "" {
			channels = appendChannel(channels, c)
		}
	}
	return ok(channels)
}

// appendChannel normalizes status and drops a channel whose fields could
// smuggle terminal control or are too long to be a handle (D21).
func appendChannel(channels []model.ContactChannel, c model.ContactChannel) []model.ContactChannel {
	for _, v := range []string{c.Channel, c.Handle, c.Detail} {
		if len(v) > 1024 || strings.ContainsFunc(v, unicode.IsControl) {
			return channels
		}
	}
	if c.Handle == "" {
		return channels
	}
	switch c.Status {
	case "working", "idle", "waiting":
	default:
		c.Status = "unknown"
	}
	return append(channels, c)
}

func output(cmd *exec.Cmd) ([]byte, error) {
	var stdout bytes.Buffer
	cmd.Stdout = &limitWriter{w: &stdout, n: outputLimit}
	// A backgrounded grandchild can hold stdout open past the deadline, and
	// must not outlive the budget.
	cmd.WaitDelay = 10 * time.Millisecond
	tree, err := proctree.Start(cmd)
	defer tree.Kill()
	if err == nil {
		err = cmd.Wait()
	}
	return stdout.Bytes(), err
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, outputLimit))
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if len(p) > l.n {
		return 0, errors.New("output exceeds 64 KiB")
	}
	l.n -= len(p)
	return l.w.Write(p)
}

// Line lists a session's channels as "channel handle status" and each
// unavailable resolver with its reason; an ok resolver with no channel adds
// nothing.
func Line(results []model.ContactResult) string {
	var parts []string
	for _, r := range results {
		if r.Status != "ok" {
			parts = append(parts, r.Resolver+" unavailable: "+r.Reason)
		}
		for _, c := range r.Channels {
			parts = append(parts, c.Channel+" "+c.Handle+" "+c.Status)
		}
	}
	return strings.Join(parts, "; ")
}
