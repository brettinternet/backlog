// Package editor launches $EDITOR on a file for bl edit and the TUI.
package editor

import (
	"context"
	"os"
	"os/exec"
)

// Command opens path in $EDITOR. On Unix the shell interprets $EDITOR and vi
// is the default; Windows has no /bin/sh, so $EDITOR is split into arguments
// with Windows quoting rules and notepad is the default.
func Command(ctx context.Context, path string) (*exec.Cmd, error) {
	return command(ctx, os.Getenv("EDITOR"), path)
}
