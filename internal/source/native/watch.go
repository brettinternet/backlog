package native

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"syscall"
	"time"
)

// DirectoryWatch watches the directory itself, never each item. Wait returns
// after a signal, or when its context is canceled; Close releases all handles.
type DirectoryWatch interface {
	Wait(context.Context) error
	Close() error
}
type WatchOptions struct {
	SweepInterval, PollInterval time.Duration
	Open                        func(string) (DirectoryWatch, error)
	OnPolling                   func(error)
}

func (s *Source) Watch(ctx context.Context, changed func()) error {
	return s.WatchWithOptions(ctx, changed, WatchOptions{})
}

// WatchWithOptions exposes resource-exhaustion injection and sweep cadence for
// tests. Signals are hints; Load remains authoritative and validates file stats.
func (s *Source) WatchWithOptions(ctx context.Context, changed func(), o WatchOptions) error {
	s.watching.Store(true)
	defer s.watching.Store(false)
	if o.SweepInterval <= 0 {
		o.SweepInterval = 2 * time.Second
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 50 * time.Millisecond
	}
	if o.Open == nil {
		o.Open = openDirectoryWatch
	}
	watcher, err := o.Open(s.options.Path)
	polling := false
	if err != nil {
		if !errors.Is(err, syscall.EMFILE) && !errors.Is(err, syscall.ENFILE) && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errPollingOnly) {
			return err
		}
		polling = true
		if o.OnPolling != nil {
			o.OnPolling(err)
		}
	}
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	signals := make(chan error, 1)
	done := make(chan struct{})
	if watcher != nil {
		defer func() { cancel(); <-done; _ = watcher.Close() }()
		go func() {
			defer close(done)
			for {
				e := watcher.Wait(watchCtx)
				select {
				case signals <- e:
				case <-watchCtx.Done():
					return
				}
				if e != nil {
					return
				}
			}
		}()
	}
	baseline, err := s.watchStats()
	if err != nil {
		return err
	}
	// Reconcile once after installing the watch: no baseline/install gap can lose
	// an edit that raced the engine's initial load.
	changed()
	interval := o.SweepInterval
	if polling {
		interval = o.PollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-signals:
			if e != nil {
				if ctx.Err() != nil {
					return nil
				}
				if !errors.Is(e, syscall.EMFILE) && !errors.Is(e, syscall.ENFILE) {
					return e
				}
				polling = true
				if o.OnPolling != nil {
					o.OnPolling(e)
				}
				ticker.Reset(o.PollInterval)
			}
			changed()
		case <-ticker.C:
			next, e := s.watchStats()
			if e != nil {
				return e
			}
			if !sameStats(baseline, next) {
				baseline = next
				changed()
			}
		}
	}
}

var errPollingOnly = errors.New("directory notifications unavailable")

type watchStat struct{ info os.FileInfo }

func (s *Source) watchStats() (map[string]watchStat, error) {
	paths, err := s.paths(false)
	if err != nil {
		return nil, err
	}
	out := make(map[string]watchStat, len(paths))
	for _, path := range paths {
		stat, e := os.Lstat(path)
		if errors.Is(e, fs.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out[path] = watchStat{stat}
	}
	return out, nil
}
func sameStats(a, b map[string]watchStat) bool {
	if len(a) != len(b) {
		return false
	}
	for path, stat := range a {
		if other, ok := b[path]; !ok || stat.info.Size() != other.info.Size() || stat.info.ModTime() != other.info.ModTime() || stat.info.Mode() != other.info.Mode() || !os.SameFile(stat.info, other.info) {
			return false
		}
	}
	return true
}
