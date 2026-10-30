//go:build windows

package native

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sys/windows"
)

// directoryWatch keeps one overlapped ReadDirectoryChangesW read pending on the
// directory and blocks in WaitForMultipleObjects without a timeout, so an idle
// watch costs no wakeups. Cancellation sets a second event. File name, size, and
// last-write notifications cover creates, renames, deletes, and in-place edits,
// so Windows needs no stat sweep. The records are never parsed: any completion,
// including a buffer overflow, signals a full reload. Every failure wraps
// errPollingOnly, so the caller falls back to polling.
type directoryWatch struct {
	mu                     sync.Mutex
	directory, ready, wake windows.Handle
	overlapped             windows.Overlapped
	buffer                 []byte
	pending, closed        bool
}

const watchFilter = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE

func openDirectoryWatch(path string) (DirectoryWatch, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errPollingOnly, err)
	}
	directory, err := windows.CreateFile(name, windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errPollingOnly, err)
	}
	w := &directoryWatch{directory: directory, buffer: make([]byte, 64*1024)}
	if w.ready, err = windows.CreateEvent(nil, 1, 0, nil); err == nil {
		w.overlapped.HEvent = w.ready
		if w.wake, err = windows.CreateEvent(nil, 0, 0, nil); err == nil {
			// Arm before returning: the kernel buffers changes from the first read
			// on, so nothing between open and the first Wait is lost.
			err = w.arm()
		}
	}
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("%w: %w", errPollingOnly, err)
	}
	return w, nil
}
func (w *directoryWatch) arm() error {
	if err := windows.ResetEvent(w.ready); err != nil {
		return err
	}
	if err := windows.ReadDirectoryChanges(w.directory, &w.buffer[0], uint32(len(w.buffer)), false, watchFilter, nil, &w.overlapped, 0); err != nil {
		return err
	}
	w.pending = true
	return nil
}
func (w *directoryWatch) signal() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		_ = windows.SetEvent(w.wake)
	}
}
func (w *directoryWatch) Wait(ctx context.Context) error {
	stop := context.AfterFunc(ctx, w.signal)
	defer stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !w.pending {
			if err := w.arm(); err != nil {
				return fmt.Errorf("%w: %w", errPollingOnly, err)
			}
		}
		event, err := windows.WaitForMultipleObjects([]windows.Handle{w.ready, w.wake}, false, windows.INFINITE)
		if err != nil {
			return fmt.Errorf("%w: %w", errPollingOnly, err)
		}
		if event != windows.WAIT_OBJECT_0 {
			continue
		}
		w.pending = false
		var n uint32
		// A zero-byte completion or ERROR_NOTIFY_ENUM_DIR is an overflow; it still
		// signals, and the caller's reload rescans the directory.
		if err = windows.GetOverlappedResult(w.directory, &w.overlapped, &n, false); err != nil && err != windows.ERROR_NOTIFY_ENUM_DIR {
			return fmt.Errorf("%w: %w", errPollingOnly, err)
		}
		return nil
	}
}

// Close cancels the pending read and waits for it, so the kernel never writes
// into the buffer after the handles are gone.
func (w *directoryWatch) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.pending {
		_ = windows.CancelIoEx(w.directory, &w.overlapped)
		var n uint32
		_ = windows.GetOverlappedResult(w.directory, &w.overlapped, &n, true)
		w.pending = false
	}
	err := windows.CloseHandle(w.directory)
	for _, h := range []windows.Handle{w.ready, w.wake} {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
	}
	return err
}
