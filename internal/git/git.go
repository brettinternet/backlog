// Package git reads a repository's refs, worktrees, and reflogs from .git
// without starting Git (design.md D5), and runs Git safely where reading files
// can't answer: no pager, external diff, textconv, fsmonitor, optional locks,
// or lazy fetches, in D21's minimal environment.
package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/project"
)

// Run runs git in dir and returns its standard output. Commands that diff
// still pass --no-ext-diff and --no-textconv themselves.
func Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	base := []string{"--no-pager", "--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false", "-c", "log.showSignature=false", "-C", dir}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Env = env()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		name := "git"
		if len(args) > 0 {
			name += " " + args[0]
		}
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// RepoFilter reports whether the repository's own config, rather than the
// user's, defines a clean or process filter. Git runs one while refreshing the
// index, as git status does, so that would run a command the repository names.
func RepoFilter(ctx context.Context, dir string) (bool, error) {
	out, err := Run(ctx, dir, "config", "--show-scope", "--name-only", "--get-regexp", `^filter\..*\.(clean|process)$`)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if scope, _, _ := strings.Cut(line, "\t"); scope == "local" || scope == "worktree" {
			return true, nil
		}
	}
	return false, nil
}

// env is D21's minimal environment, with lazy fetches of a partial clone's
// missing objects and credential prompts turned off, since bl never fetches
// (D9).
func env() []string {
	var out []string
	for _, key := range []string{"PATH", "HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+value)
		}
	}
	return append(out, "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
}

// Head resolves HEAD without running git, or returns "" when it can't.
func Head(d project.Discovery) string {
	data, err := os.ReadFile(filepath.Join(d.GitDir, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(data))
	ref, symbolic := strings.CutPrefix(head, "ref: ")
	if !symbolic {
		return head
	}
	return resolve(d.GitDir, d.CommonDir, ref)
}

func resolve(gitDir, commonDir, ref string) string {
	for _, dir := range []string{gitDir, commonDir} {
		if data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ref))); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	packed, err := os.ReadFile(filepath.Join(commonDir, "packed-refs"))
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

// ErrReftable reports a repository whose refs Git keeps in a reftable, which
// bl doesn't read.
var ErrReftable = errors.New("reftable repositories are not supported")

// Refs lists the commit of every ref under the given prefixes, such as
// refs/heads/, reading loose refs over packed ones.
func Refs(commonDir string, prefixes ...string) (map[string]string, error) {
	if _, err := os.Stat(filepath.Join(commonDir, "reftable")); err == nil {
		return nil, ErrReftable
	}
	refs := map[string]string{}
	wanted := func(name string) bool {
		for _, prefix := range prefixes {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		}
		return false
	}
	if packed, err := os.ReadFile(filepath.Join(commonDir, "packed-refs")); err == nil {
		for line := range strings.SplitSeq(string(packed), "\n") {
			commit, name, ok := strings.Cut(strings.TrimSpace(line), " ")
			if ok && len(commit) >= 40 && wanted(name) {
				refs[name] = commit
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, prefix := range prefixes {
		root := filepath.Join(commonDir, filepath.FromSlash(prefix))
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			rel, err := filepath.Rel(commonDir, path)
			if err != nil {
				return nil
			}
			// A symbolic ref such as refs/remotes/origin/HEAD is left out;
			// SymbolicTarget reads it.
			if value := strings.TrimSpace(string(data)); len(value) >= 40 && !strings.HasPrefix(value, "ref: ") {
				refs[filepath.ToSlash(rel)] = value
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return refs, nil
}

// SymbolicTarget is the ref a symbolic ref names, such as
// refs/remotes/origin/main for refs/remotes/origin/HEAD, or "".
func SymbolicTarget(commonDir, ref string) string {
	data, err := os.ReadFile(filepath.Join(commonDir, filepath.FromSlash(ref)))
	if err != nil {
		return ""
	}
	target, _ := strings.CutPrefix(strings.TrimSpace(string(data)), "ref: ")
	if !strings.HasPrefix(target, "refs/") {
		return ""
	}
	return target
}

// Worktree is a checkout of the repository on a branch. Main marks the
// primary checkout; Created is when it was added, if known.
type Worktree struct {
	Path, Branch, Head string
	Main               bool
	Created            time.Time
}

// Worktrees lists the checkouts on a branch: the primary one, unless the
// repository is bare, then each linked worktree whose directory still exists.
// Detached checkouts have no branch to link and are left out.
func Worktrees(commonDir string) []Worktree {
	var out []Worktree
	if filepath.Base(commonDir) == ".git" {
		if w, ok := worktree(commonDir, commonDir, filepath.Dir(commonDir)); ok {
			w.Main = true
			out = append(out, w)
		}
	}
	entries, err := os.ReadDir(filepath.Join(commonDir, "worktrees"))
	if err != nil {
		return out
	}
	for _, entry := range entries {
		admin := filepath.Join(commonDir, "worktrees", entry.Name())
		data, err := os.ReadFile(filepath.Join(admin, "gitdir"))
		if err != nil {
			continue
		}
		path := filepath.Dir(strings.TrimSpace(string(data)))
		if !filepath.IsAbs(path) {
			path = filepath.Join(admin, path)
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			continue
		}
		if w, ok := worktree(admin, commonDir, path); ok {
			if w.Created.IsZero() {
				if info, err := os.Stat(filepath.Join(admin, "commondir")); err == nil {
					w.Created = info.ModTime().UTC()
				}
			}
			out = append(out, w)
		}
	}
	return out
}

func worktree(gitDir, commonDir, path string) (Worktree, bool) {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return Worktree{}, false
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "ref: refs/heads/")
	if !ok {
		return Worktree{}, false
	}
	return Worktree{Path: path, Branch: ref, Head: resolve(gitDir, commonDir, "refs/heads/"+ref), Created: FirstSeen(filepath.Join(gitDir, "logs", "HEAD"))}, true
}

// FirstSeen is the time of a reflog's first entry, such as when a branch was
// created, or zero when the reflog is missing.
func FirstSeen(reflog string) time.Time {
	f, err := os.Open(reflog)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	if !scanner.Scan() {
		return time.Time{}
	}
	// old new Name <email> 1698765432 -0700<TAB>message
	line, _, _ := strings.Cut(scanner.Text(), "\t")
	end := strings.LastIndex(line, "> ")
	if end < 0 {
		return time.Time{}
	}
	fields := strings.Fields(line[end+2:])
	if len(fields) == 0 {
		return time.Time{}
	}
	seconds, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}
