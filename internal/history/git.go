package history

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/project"
)

// maxCommits bounds one item's Git history; older commits make it partial.
const maxCommits = 1000

type gitCache struct {
	Version, Head, Partial string
	Entries                []Entry
}

// gitHistory lists the commits that touched path, following renames. Its
// result is cached by item version and HEAD commit, so it runs once per
// version until HEAD moves.
func gitHistory(ctx context.Context, cacheDir, version, path string) ([]Entry, error) {
	d, err := project.Discover(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if d.GitDir == "" {
		return nil, errors.New("not in a Git repository")
	}
	head := headCommit(d)
	cachePath := ""
	if cacheDir != "" && head != "" && version != "" {
		sum := sha256.Sum256([]byte(path))
		cachePath = filepath.Join(cacheDir, "history", hex.EncodeToString(sum[:16])+".json")
		var cached gitCache
		if data, err := os.ReadFile(cachePath); err == nil && json.Unmarshal(data, &cached) == nil && cached.Version == version && cached.Head == head {
			return cached.Entries, partial(cached.Partial)
		}
	}
	entries, truncated, err := gitLog(ctx, path)
	if err != nil {
		return nil, err
	}
	reason := ""
	if truncated {
		reason = fmt.Sprintf("newest %d commits", maxCommits)
	}
	if cachePath != "" {
		if data, err := json.Marshal(gitCache{Version: version, Head: head, Partial: reason, Entries: entries}); err == nil {
			writeCache(cachePath, data)
		}
	}
	return entries, partial(reason)
}

func partial(reason string) error {
	if reason == "" {
		return nil
	}
	return PartialError(errors.New(reason))
}

// gitLog runs git log with no external diff, textconv, or fsmonitor, in the
// minimal hook environment (design.md D21).
func gitLog(ctx context.Context, path string) ([]Entry, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", gitArgs(path)...)
	cmd.Env = minimalEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, false, fmt.Errorf("git log: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var entries []Entry
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\x1f", 4)
		if len(fields) != 4 {
			continue
		}
		at, _ := time.Parse(time.RFC3339, fields[1])
		entries = append(entries, Entry{ID: "git:" + fields[0], Time: at.UTC(), Kind: "commit", Actor: fields[2], Summary: fields[3], Detail: fields[0]})
	}
	truncated := len(entries) > maxCommits
	if truncated {
		entries = entries[:maxCommits]
	}
	return entries, truncated, nil
}

func gitArgs(path string) []string {
	return []string{"--no-pager", "-c", "core.fsmonitor=false", "-c", "log.showSignature=false", "--literal-pathspecs", "-C", filepath.Dir(path),
		"log", "--follow", "--no-ext-diff", "--no-textconv", "--no-color", fmt.Sprintf("--max-count=%d", maxCommits+1),
		"--format=%H%x1f%aI%x1f%an%x1f%s", "--", filepath.Base(path)}
}

func minimalEnv() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

// headCommit resolves HEAD without running git, or returns "" when it can't.
func headCommit(d project.Discovery) string {
	data, err := os.ReadFile(filepath.Join(d.GitDir, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(data))
	ref, symbolic := strings.CutPrefix(head, "ref: ")
	if !symbolic {
		return head
	}
	for _, dir := range []string{d.GitDir, d.CommonDir} {
		if data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ref))); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	packed, err := os.ReadFile(filepath.Join(d.CommonDir, "packed-refs"))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(packed), "\n") {
		if commit, name, ok := strings.Cut(line, " "); ok && name == ref {
			return commit
		}
	}
	return ""
}

func writeCache(path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".history-*")
	if err != nil {
		return
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
}
