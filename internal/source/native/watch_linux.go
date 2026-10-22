//go:build linux

package native

import (
	"context"
	"sync"

	"golang.org/x/sys/unix"
)

// directoryWatch blocks in poll without a timeout, so an idle watch costs no
// wakeups. Cancellation writes to an eventfd polled alongside inotify.
type directoryWatch struct {
	mu       sync.Mutex
	fd, wake int
	closed   bool
}

func openDirectoryWatch(path string) (DirectoryWatch, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	_, err = unix.InotifyAddWatch(fd, path, unix.IN_CREATE|unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO|unix.IN_CLOSE_WRITE|unix.IN_ATTRIB|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_ONLYDIR|unix.IN_DONT_FOLLOW)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	wake, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &directoryWatch{fd: fd, wake: wake}, nil
}
func (w *directoryWatch) signal() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		_, _ = unix.Write(w.wake, []byte{1, 0, 0, 0, 0, 0, 0, 0})
	}
}
func (w *directoryWatch) Wait(ctx context.Context) error {
	stop := context.AfterFunc(ctx, w.signal)
	defer stop()
	buffer := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fds := []unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLIN}, {Fd: int32(w.wake), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, -1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 || fds[0].Revents == 0 {
			continue
		}
		_, err = unix.Read(w.fd, buffer)
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		return err
	}
}
func (w *directoryWatch) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	a := unix.Close(w.fd)
	b := unix.Close(w.wake)
	if a != nil {
		return a
	}
	return b
}
