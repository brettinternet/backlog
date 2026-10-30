// demo runs the real TUI over BL-12 fixtures, never a user's project.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/brettinternet/backlog/internal/fixtures"
)

const item = `package pick

type Item struct {
	ID    string
	Ready bool
}
`

const pick = `package pick

// Next returns the first ready item.
func Next(items []Item) (Item, bool) {
	for _, item := range items {
		if item.Ready {
			return item, true
		}
	}
	return Item{}, false
}
`

const rankedItem = `package pick

type Item struct {
	ID       string
	Ready    bool
	Priority int // 0 none to 4 urgent
}
`

const rankedPick = `package pick

import "slices"

// Next returns the highest-priority ready item, oldest first.
func Next(items []Item) (Item, bool) {
	ready := slices.DeleteFunc(slices.Clone(items), func(item Item) bool {
		return !item.Ready
	})
	if len(ready) == 0 {
		return Item{}, false
	}
	slices.SortStableFunc(ready, func(a, b Item) int {
		return b.Priority - a.Priority
	})
	return ready[0], true
}
`

func main() {
	if err := record(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func record() error {
	binary, err := filepath.Abs("bin/bl")
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "bl-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	project := filepath.Join(root, "project")
	if err := fixtures.Generate(project, 48, 1); err != nil {
		return err
	}
	if err := appendFile(filepath.Join(project, "backlog.yaml"), "project: {id: bl-demo, name: Demo}\n"); err != nil {
		return err
	}
	var env []string
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if strings.HasPrefix(key, "BL_") || strings.HasPrefix(key, "WORKLEASE_") ||
			strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "GIT_") ||
			key == "HOME" || key == "USERPROFILE" || key == "NO_COLOR" || key == "COLORFGBG" {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "HOME="+root, "USERPROFILE="+root,
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"TERM=xterm-256color", "COLORTERM=truecolor", "TZ=UTC",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=alice@example.com",
		"GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=alice@example.com")
	runEnv := func(extra []string, name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Env = project, append(env[:len(env):len(env)], extra...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}
	run := func(name string, args ...string) (string, error) { return runEnv(nil, name, args...) }
	commit := func(date, message string, files map[string]string) error {
		for path, text := range files {
			path = filepath.Join(project, path)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(text), 0644); err != nil {
				return err
			}
		}
		if _, err := run("git", "add", "-A"); err != nil {
			return err
		}
		_, err := runEnv([]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date},
			"git", "-c", "commit.gpgsign=false", "commit", "-q", "-m", message)
		return err
	}

	// Branch bl-5-rank-by-priority links to BL-5, and its tour shows in Diff.
	if err := os.RemoveAll(filepath.Join(project, ".git")); err != nil {
		return err
	}
	if _, err := run("git", "init", "-q", "-b", "main"); err != nil {
		return err
	}
	if _, err := run(binary, "ac", "BL-5", "add", "Ready items rank by priority"); err != nil {
		return err
	}
	if err := commit("2026-10-21T09:00:00Z", "chore: add backlog",
		map[string]string{"pick/item.go": item, "pick/pick.go": pick}); err != nil {
		return err
	}
	if _, err := run("git", "switch", "-q", "-c", "bl-5-rank-by-priority"); err != nil {
		return err
	}
	if err := commit("2026-10-22T15:00:00Z", "feat(pick): rank ready items by priority",
		map[string]string{"pick/item.go": rankedItem, "pick/pick.go": rankedPick}); err != nil {
		return err
	}
	head, err := run("git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if _, err := run("git", "switch", "-q", "main"); err != nil {
		return err
	}
	tour, err := json.Marshal(map[string]any{"ref": head, "steps": []map[string]any{
		{"file": "pick/item.go", "line": 6, "title": "Priority", "description": "Items carry their priority."},
		{"file": "pick/pick.go", "selection": map[string]any{"start": map[string]int{"line": 13}, "end": map[string]int{"line": 15}},
			"title": "Rank", "criterion": 1, "description": "Stable sort keeps the oldest first within a priority."},
	}})
	if err != nil {
		return err
	}
	tourFile := filepath.Join(root, "tour.json")
	if err := os.WriteFile(tourFile, tour, 0644); err != nil {
		return err
	}
	if _, err := run(binary, "tour", "BL-5", tourFile); err != nil {
		return err
	}

	cmd := exec.Command(binary, "-C", project, "tui")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	return cmd.Run()
}

func appendFile(path, text string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(text)
	if err := file.Close(); writeErr == nil {
		writeErr = err
	}
	return writeErr
}
