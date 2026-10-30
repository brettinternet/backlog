// Package atomicfile replaces files by rename portably (design.md D8).
package atomicfile

import "errors"

// ErrBusy reports that another process kept the target open through every
// retry; the target is unchanged.
var ErrBusy = errors.New("file is held open by another process")

// Rename atomically replaces newpath with oldpath. On Windows, a target that
// an editor, indexer or antivirus briefly holds open gets a bounded retry.
func Rename(oldpath, newpath string) error { return rename(oldpath, newpath) }

// SyncDir makes a rename in dir durable where the platform needs it.
func SyncDir(dir string) error { return syncDir(dir) }
