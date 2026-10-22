//go:build darwin

package native

import (
	"context"
	"sync"

	"golang.org/x/sys/unix"
)

// directoryWatch blocks in kevent without a timeout, so an idle watch costs no
// wakeups. Cancellation triggers an EVFILT_USER event on the same queue.
type directoryWatch struct {
	mu               sync.Mutex
	queue, directory int
	closed           bool
}

const wakeIdent = 1

func openDirectoryWatch(path string) (DirectoryWatch, error) {
	fd, err := unix.Open(path, unix.O_EVTONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	queue, err := unix.Kqueue()
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	unix.CloseOnExec(queue)
	events := []unix.Kevent_t{
		{Ident: uint64(fd), Filter: unix.EVFILT_VNODE, Flags: unix.EV_ADD | unix.EV_CLEAR, Fflags: unix.NOTE_WRITE | unix.NOTE_DELETE | unix.NOTE_RENAME | unix.NOTE_ATTRIB},
		{Ident: wakeIdent, Filter: unix.EVFILT_USER, Flags: unix.EV_ADD | unix.EV_CLEAR},
	}
	if _, err = unix.Kevent(queue, events, nil, nil); err != nil {
		_ = unix.Close(queue)
		_ = unix.Close(fd)
		return nil, err
	}
	return &directoryWatch{queue: queue, directory: fd}, nil
}
func (w *directoryWatch) wake() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		_, _ = unix.Kevent(w.queue, []unix.Kevent_t{{Ident: wakeIdent, Filter: unix.EVFILT_USER, Fflags: unix.NOTE_TRIGGER}}, nil, nil)
	}
}
func (w *directoryWatch) Wait(ctx context.Context) error {
	stop := context.AfterFunc(ctx, w.wake)
	defer stop()
	events := make([]unix.Kevent_t, 1)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := unix.Kevent(w.queue, nil, events, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 || events[0].Filter == unix.EVFILT_USER {
			continue
		}
		if events[0].Flags&unix.EV_ERROR != 0 {
			return unix.Errno(events[0].Data)
		}
		return nil
	}
}
func (w *directoryWatch) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	a := unix.Close(w.queue)
	b := unix.Close(w.directory)
	if a != nil {
		return a
	}
	return b
}
