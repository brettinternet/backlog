//go:build darwin

package native

import (
	"context"

	"golang.org/x/sys/unix"
)

type directoryWatch struct{ queue, directory int }

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
	event := unix.Kevent_t{Ident: uint64(fd), Filter: unix.EVFILT_VNODE, Flags: unix.EV_ADD | unix.EV_CLEAR, Fflags: unix.NOTE_WRITE | unix.NOTE_DELETE | unix.NOTE_RENAME | unix.NOTE_ATTRIB}
	if _, err = unix.Kevent(queue, []unix.Kevent_t{event}, nil, nil); err != nil {
		_ = unix.Close(queue)
		_ = unix.Close(fd)
		return nil, err
	}
	return &directoryWatch{queue, fd}, nil
}
func (w *directoryWatch) Wait(ctx context.Context) error {
	events := make([]unix.Kevent_t, 1)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		timeout := unix.NsecToTimespec(20_000_000)
		n, err := unix.Kevent(w.queue, nil, events, &timeout)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n > 0 {
			if events[0].Flags&unix.EV_ERROR != 0 {
				return unix.Errno(events[0].Data)
			}
			return nil
		}
	}
}
func (w *directoryWatch) Close() error {
	a := unix.Close(w.queue)
	b := unix.Close(w.directory)
	if a != nil {
		return a
	}
	return b
}
