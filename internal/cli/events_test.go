package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/cli"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

func readEvents(t *testing.T, root string) []hooks.Event {
	t.Helper()
	code, out, stderr := writeRun(root, "", "events")
	if code != 0 || stderr != "" {
		t.Fatalf("events: %d %s %s", code, out, stderr)
	}
	var response struct {
		Data struct {
			Events []hooks.Event `json:"events"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	return response.Data.Events
}
func eventConfig(t *testing.T, root string) project.Config {
	t.Helper()
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
func eventItem(t *testing.T, root, id, fields string) {
	t.Helper()
	put(t, filepath.Join(root, "items", id+".md"), fmt.Sprintf("---\nid: %s\ntitle: Item %s\nstatus: todo\ncreated: 2026-10-21T00:00:00Z\nupdated: 2026-10-21T00:00:00Z\n%s---\n", id, id, fields))
}

// Catches missing reverse-graph readiness events (including a container), and
// events incorrectly emitted by a claim-only refresh or unchanged load.
func TestEventReadiness(t *testing.T) {
	root := writeFixture(t)
	eventItem(t, root, "BL-1", "")
	eventItem(t, root, "BL-2", "blocked_by: [BL-1]\n")
	eventItem(t, root, "BL-3", "blocked_by: [BL-2]\n")
	eventItem(t, root, "BL-4", "")
	eventItem(t, root, "BL-5", "parent: BL-4\n")
	cfg := eventConfig(t, root)
	readEvents(t, root)
	writeOK(t, root, "", "done", "1")
	events := readEvents(t, root)
	ready := map[string]hooks.Change{}
	for _, e := range events {
		if e.Type == "item.readiness" {
			ready[e.Ref] = e.Changes["readiness"]
		}
	}
	if len(ready) != 2 || ready["tasks:BL-2"].From != "blocked" || ready["tasks:BL-2"].To != "ready" || ready["tasks:BL-1"].To != "ineligible" {
		t.Fatalf("wrong transitions: %+v", ready)
	}
	before := len(events)
	store, err := engine.OpenProjectStore(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	store.Refresh()
	store.Close()
	if got := len(readEvents(t, root)); got != before {
		t.Fatalf("unchanged refresh appended %d events", got-before)
	}
	writeOK(t, root, "", "done", "5")
	ready = map[string]hooks.Change{}
	for _, e := range readEvents(t, root)[before:] {
		if e.Type == "item.readiness" {
			ready[e.Ref] = e.Changes["readiness"]
		}
	}
	if ready["tasks:BL-4"].From != "ineligible" || ready["tasks:BL-4"].To != "ready" {
		t.Fatalf("container readiness omitted: %+v", ready)
	}
	eventItem(t, root, "BL-6", "blocked_by: [BL-12]\n")
	eventItem(t, root, "BL-7", "parent: BL-4\n")
	eventItem(t, root, "BL-8", "")
	before = len(readEvents(t, root))
	writeOK(t, root, "", "done", "12")
	writeOK(t, root, "", "set", "7", "parent=8")
	ready = map[string]hooks.Change{}
	for _, e := range readEvents(t, root)[before:] {
		if e.Type == "item.readiness" {
			ready[e.Ref] = e.Changes["readiness"]
		}
	}
	if ready["tasks:BL-6"].To != "ready" || ready["tasks:BL-4"].To != "ready" || ready["tasks:BL-8"].To != "ineligible" {
		t.Fatalf("padded blocker or moved-child transition omitted: %+v", ready)
	}
}

// Catches depth-resetting automation subprocesses and a malformed depth
// causing a durable write before event validation refuses the command.
func TestEventCause(t *testing.T) {
	root := writeFixture(t)
	path := filepath.Join(root, "items/BL-012.md")
	before := get(t, path)
	t.Setenv("BL_EVENT_ID", "01K8JHW1J00000000000000000")
	t.Setenv("BL_EVENT_ORIGIN", "hook")
	t.Setenv("BL_HOOK_DEPTH", "-2")
	code, output, _ := writeRun(root, "", "note", "12", "Causal note")
	if code != 5 || get(t, path) != before {
		t.Fatalf("invalid depth wrote an item: %d %s", code, output)
	}
	t.Setenv("BL_HOOK_DEPTH", "2")
	writeOK(t, root, "", "note", "12", "Causal note")
	events := readEvents(t, root)
	if len(events) != 1 || events[0].Origin != "hook" || events[0].Depth != 3 || events[0].Cause == nil || *events[0].Cause != "01K8JHW1J00000000000000000" {
		t.Fatalf("lost causal context: %+v", events)
	}
}

// Catches false deletion events and valid mutations being refused merely
// because an unrelated file cannot currently be parsed.
func TestEventPartialCoverage(t *testing.T) {
	root := writeFixture(t)
	eventItem(t, root, "BL-1", "")
	readEvents(t, root)
	put(t, filepath.Join(root, "items/BL-1.md"), "---\nid: [broken\n")
	writeOK(t, root, "", "note", "12", "A valid note")
	cfg := eventConfig(t, root)
	events, err := hooks.New(cfg.Paths.StateDir, cfg.Project.ID).Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "item.updated" || events[0].Ref != "tasks:BL-012" {
		t.Fatalf("partial coverage changed unrelated items: %+v", events)
	}
	eventItem(t, root, "BL-1", "")
	if got := readEvents(t, root); len(got) != 1 {
		t.Fatalf("duplicate after coverage recovered: %+v", got)
	}
}

// Catches independent observers consuming the cached transition before its
// events have been appended. Two engine stores are the TUI observation path.
func TestEventThreeObservers(t *testing.T) {
	measure := os.Getenv("BL_BENCH_EVENTS") == "1"
	root := writeFixture(t)
	eventItem(t, root, "BL-1", "")
	eventItem(t, root, "BL-2", "blocked_by: [BL-1]\n")
	readEvents(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	type child struct {
		cmd    *exec.Cmd
		in     io.WriteCloser
		out    *bufio.Reader
		stderr bytes.Buffer
	}
	children := make([]*child, 3)
	for n := range children {
		c := &child{}
		c.cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEventObserverProcess$")
		mode := "store"
		if n == 2 {
			mode = "follow"
		}
		c.cmd.Env = append(os.Environ(), "BL_EVENT_TEST_ROOT="+root, "BL_EVENT_TEST_MODE="+mode)
		var err error
		c.in, err = c.cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := c.cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		c.out = bufio.NewReader(stdout)
		c.cmd.Stderr = &c.stderr
		if err = c.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children[n] = c
		t.Cleanup(func() {
			c.in.Close()
			if c.cmd.ProcessState == nil {
				c.cmd.Process.Kill()
				c.cmd.Wait()
			}
		})
		for {
			line, err := c.out.ReadString('\n')
			if err != nil {
				t.Fatalf("observer startup: %v %s", err, c.stderr.String())
			}
			if strings.TrimSpace(line) == "ready" {
				break
			}
		}
	}
	started := time.Now()
	path := filepath.Join(root, "items/BL-1.md")
	text := get(t, path)
	put(t, path, strings.Replace(text, "status: todo", "status: done", 1))
	for _, c := range children {
		if _, err := c.in.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range children {
		if err := c.cmd.Wait(); err != nil {
			t.Fatalf("observer: %v %s", err, c.stderr.String())
		}
	}
	if measure {
		t.Logf("three-observer external edit delivered within %s", time.Since(started))
	}
	events := readEvents(t, root)
	counts := map[string]int{}
	for _, e := range events {
		counts[e.Type+" "+e.Ref]++
		if e.Origin != "sync" {
			t.Fatalf("external edit origin: %s", e.Origin)
		}
	}
	if len(events) != 3 || counts["item.updated tasks:BL-1"] != 1 || counts["item.readiness tasks:BL-1"] != 1 || counts["item.readiness tasks:BL-2"] != 1 {
		t.Fatalf("duplicate or missing events: %+v", events)
	}
}
func TestEventObserverProcess(t *testing.T) {
	root := os.Getenv("BL_EVENT_TEST_ROOT")
	if root == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg := eventConfig(t, root)
	if os.Getenv("BL_EVENT_TEST_MODE") == "store" {
		store, err := engine.OpenProjectStore(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		snapshots, unsubscribe := store.Subscribe()
		defer unsubscribe()
		for {
			select {
			case s := <-snapshots:
				if s.Len() == 3 {
					goto loaded
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	loaded:
		fmt.Println("ready")
		if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
			t.Fatal(err)
		}
		store.Refresh()
		for {
			select {
			case s := <-snapshots:
				if item, _, ok := s.Get(model.Ref{Source: "tasks", ID: "BL-1"}); ok && item.Status == "done" {
					return
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	} else {
		reader, writer := io.Pipe()
		done := make(chan int, 1)
		go func() {
			done <- cli.Run(ctx, []string{"bl", "-C", root, "events", "--follow", "--json"}, cli.BuildInfo{}, strings.NewReader(""), writer, os.Stderr)
			writer.Close()
		}()
		fmt.Println("ready")
		if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "item.readiness") {
				cancel()
				reader.Close()
				if code := <-done; code != 0 {
					t.Fatalf("follow exit %d", code)
				}
				return
			}
		}
		t.Fatalf("follower ended: %v", scanner.Err())
	}
}
