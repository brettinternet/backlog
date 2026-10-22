//go:build linux

package native

import (
	"context"

	"golang.org/x/sys/unix"
)

type directoryWatch struct{ fd int }

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
	return &directoryWatch{fd}, nil
}
func (w *directoryWatch) Wait(ctx context.Context) error {
	buffer := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := unix.Poll([]unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLIN}}, 20)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		_, err = unix.Read(w.fd, buffer)
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		return err
	}
}
func (w *directoryWatch) Close() error { return unix.Close(w.fd) }
