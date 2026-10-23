// Package autocommit batches opt-in Git commits without owning file writes.
package autocommit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

type key struct{}
type fileState struct {
	Sum     [32]byte
	Mode    os.FileMode
	Missing bool
}
type batch struct {
	paths   map[string]fileState
	blocked bool
	message string
}

// Session serializes writes and commits, including the idle callback. Report is
// called off the UI loop; it must be safe to call from a background worker.
type Session struct {
	mu         sync.Mutex
	delay      time.Duration
	timer      *time.Timer
	batches    map[string]*batch
	report     func(string)
	verb       string
	closed     bool
	generation uint64
	afterFunc  func(time.Duration, func()) *time.Timer
}

func New(delay time.Duration, verb string, report func(string)) *Session {
	return &Session{delay: delay, verb: verb, report: report, batches: map[string]*batch{}, afterFunc: time.AfterFunc}
}
func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, key{}, s)
}

// Ensure gives a standalone mutation a session. Defer cleanup before taking
// source locks so Git hooks execute only after those locks have been released.
func Ensure(ctx context.Context) (context.Context, func()) {
	if s, _ := ctx.Value(key{}).(*Session); s != nil {
		return ctx, func() {}
	}
	s := New(0, "", func(message string) { fmt.Fprintln(os.Stderr, "bl: warning: "+message) })
	return WithSession(ctx, s), s.Close
}
func Warning(ctx context.Context, message string) {
	if s, _ := ctx.Value(key{}).(*Session); s != nil {
		s.warn(message)
	} else {
		fmt.Fprintln(os.Stderr, "bl: warning: auto-commit: "+message)
	}
}
func (s *Session) warn(message string) {
	if s.report != nil {
		s.report("auto-commit: " + message)
	}
}
func git(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--literal-pathspecs", "-C", root}, args...)...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(data)))
	}
	return string(data), nil
}
func state(path string) (fileState, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return fileState{Missing: true}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	if !info.Mode().IsRegular() {
		return fileState{}, fmt.Errorf("not a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	return fileState{Sum: sha256.Sum256(data), Mode: info.Mode()}, err
}

// Write runs a file mutation even when Git is unsafe or unavailable. Paths must
// be the exact old/new paths the mutation may write, never a directory glob.
func Write(ctx context.Context, root string, paths []string, verb, id, title string, write func() error) error {
	s, _ := ctx.Value(key{}).(*Session)
	own := s == nil
	if own {
		s = New(0, verb, func(message string) { fmt.Fprintln(os.Stderr, "bl: warning: "+message) })
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return write()
	}
	b := s.batches[root]
	if b == nil {
		b = &batch{paths: map[string]fileState{}}
		s.batches[root] = b
	}
	rels := make([]string, 0, len(paths))
	for _, path := range paths {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			b.blocked = true
			s.warn("skipped path outside repository: " + path)
			continue
		}
		rels = append(rels, rel)
		if prior, ok := b.paths[rel]; ok {
			current, err := state(path)
			if err != nil || prior != current {
				b.blocked = true
				s.warn("skipped externally changed path: " + rel)
			}
		} else {
			status, err := git(root, "status", "--porcelain=v1", "--untracked-files=all", "--", rel)
			if err != nil || status != "" {
				b.blocked = true
				s.warn("skipped dirty or unverifiable path: " + rel)
			}
		}
	}
	err := write()
	if err == nil {
		for _, rel := range rels {
			current, e := state(filepath.Join(root, rel))
			if e != nil {
				b.blocked = true
				s.warn(e.Error())
			} else {
				b.paths[rel] = current
			}
		}
		if s.verb != "" {
			verb = s.verb
		}
		// Keep the subject on one line, including titles supplied through an editor.
		b.message = "docs(backlog): " + strings.Join(strings.Fields(verb+" "+id+" "+title), " ")
		if s.delay > 0 {
			if s.timer != nil {
				s.timer.Stop()
			}
			s.generation++
			generation := s.generation
			s.timer = s.afterFunc(s.delay, func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				if !s.closed && generation == s.generation {
					s.flush()
				}
			})
		}
	}
	s.mu.Unlock()
	if own {
		s.Close()
	}
	return err
}

func unsafe(root string) error {
	if _, err := git(root, "symbolic-ref", "-q", "HEAD"); err != nil {
		return fmt.Errorf("skipped detached HEAD or unavailable repository")
	}
	for _, name := range []string{"MERGE_HEAD", "rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD", "sequencer"} {
		path, err := git(root, "rev-parse", "--git-path", name)
		if err != nil {
			return err
		}
		path = strings.TrimSpace(path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if _, err = os.Stat(path); err == nil {
			return fmt.Errorf("skipped %s in progress", name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Native mutations use this same common-directory lock. Do not wait: a source
// worker may already hold it while waiting to record its next batch edit.
func writeLock(root string) (*os.File, error) {
	common, err := git(root, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	common = strings.TrimSpace(common)
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	dir := filepath.Join(common, "backlog")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("skipped busy repository: %w", err)
	}
	return f, nil
}
func (s *Session) flush() {
	for root, b := range s.batches {
		delete(s.batches, root)
		if b.blocked || len(b.paths) == 0 || b.message == "" {
			continue
		}
		lock, err := writeLock(root)
		if err != nil {
			s.warn(err.Error())
			continue
		}
		s.commit(root, b)
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}
}
func (s *Session) commit(root string, b *batch) {

	if err := unsafe(root); err != nil {
		s.warn(err.Error())
		return
	}
	paths := make([]string, 0, len(b.paths))
	valid := true
	for rel, expected := range b.paths {
		current, err := state(filepath.Join(root, rel))
		if err != nil || current != expected {
			s.warn("skipped externally changed path: " + rel)
			valid = false
			break
		}
		// The path was clean before our first write. Index changes made since then
		// belong to somebody else, so refuse instead of replacing them.
		if output, err := git(root, "diff", "--cached", "--name-only", "--", rel); err != nil || output != "" {
			s.warn("skipped staged path: " + rel)
			valid = false
			break
		}
		paths = append(paths, rel)
	}
	if !valid {
		return
	}
	slices.Sort(paths)
	// --only preserves unrelated staged entries. New files need an index entry;
	// rollback only those entries on failure, never the user's other staging.
	added := []string{}
	committed := paths[:0]
	for _, rel := range paths {
		if _, err := git(root, "ls-files", "--error-unmatch", "--", rel); err != nil {
			if b.paths[rel].Missing {
				// Never tracked and not written: a pathspec for it would fail the commit.
				continue
			}
			if _, err = git(root, "add", "--intent-to-add", "--", rel); err != nil {
				s.warn(err.Error())
				valid = false
				break
			}
			added = append(added, rel)
		}
		committed = append(committed, rel)
	}
	paths = committed
	if valid && len(paths) > 0 {
		if output, err := git(root, append([]string{"status", "--porcelain=v1", "--untracked-files=all", "--"}, paths...)...); err == nil && output == "" {
			return
		}
		if _, err := git(root, append([]string{"commit", "--only", "-m", b.message, "--"}, paths...)...); err != nil {
			s.warn(err.Error())
		} else {
			added = nil
		}
	}
	if len(added) > 0 {
		if _, err := git(root, append([]string{"update-index", "--force-remove", "--"}, added...)...); err != nil {
			s.warn(err.Error())
		}
	}
}
func (s *Session) Flush() { s.mu.Lock(); defer s.mu.Unlock(); s.flush() }

// Close must run outside the UI update loop, after mutation workers stop.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.flush()
	s.closed = true
}
