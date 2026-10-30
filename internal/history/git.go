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
	"path/filepath"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/git"
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
	head := git.Head(d)
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
			cache.WriteFile(cachePath, data)
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

// gitLog runs git log through the shared runner, without external diff or
// textconv (design.md D21).
func gitLog(ctx context.Context, path string) ([]Entry, bool, error) {
	out, err := git.Run(ctx, filepath.Dir(path), gitArgs(path)...)
	if err != nil {
		return nil, false, err
	}
	var entries []Entry
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\x1f", 4)
		if len(fields) != 4 {
			continue
		}
		at, _ := time.Parse(time.RFC3339, fields[1])
		entries = append(entries, Entry{ID: commitID(fields[0]), Time: at.UTC(), Kind: "commit", Actor: fields[2], Summary: fields[3], Detail: fields[0]})
	}
	truncated := len(entries) > maxCommits
	if truncated {
		entries = entries[:maxCommits]
	}
	return entries, truncated, nil
}

func gitArgs(path string) []string {
	return []string{"log", "--follow", "--no-ext-diff", "--no-textconv", "--no-color", fmt.Sprintf("--max-count=%d", maxCommits+1),
		"--format=%H%x1f%aI%x1f%an%x1f%s", "--", filepath.Base(path)}
}
