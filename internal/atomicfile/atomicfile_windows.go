package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// retryDelays bound the wait for a transient holder to about 1.3 seconds.
var retryDelays = []time.Duration{10, 20, 40, 80, 160, 250, 250, 250, 250}

func rename(oldpath, newpath string) error {
	for _, delay := range retryDelays {
		err := os.Rename(oldpath, newpath)
		if !busy(err) {
			return err
		}
		time.Sleep(delay * time.Millisecond)
	}
	err := os.Rename(oldpath, newpath)
	if busy(err) {
		return fmt.Errorf("%w: %s", ErrBusy, newpath)
	}
	return err
}

// MoveFileEx reports a target opened without FILE_SHARE_DELETE as access
// denied or a sharing violation.
func busy(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

// NTFS journals directory metadata, and Windows cannot flush a directory
// handle, so there is nothing to sync.
func syncDir(string) error { return nil }
