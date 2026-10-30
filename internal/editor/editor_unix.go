//go:build unix

package editor

import (
	"context"
	"os/exec"
)

func command(ctx context.Context, editor, path string) (*exec.Cmd, error) {
	if editor == "" {
		editor = "vi"
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", editor+` "$1"`, "bl-edit", path), nil
}
