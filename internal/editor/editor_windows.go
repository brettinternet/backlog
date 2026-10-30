package editor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"
)

func command(ctx context.Context, editor, path string) (*exec.Cmd, error) {
	if strings.TrimSpace(editor) == "" {
		editor = "notepad"
	}
	argv, err := windows.DecomposeCommandLine(editor)
	if err != nil || len(argv) == 0 {
		return nil, fmt.Errorf("invalid EDITOR %q", editor)
	}
	return exec.CommandContext(ctx, argv[0], append(argv[1:], path)...), nil
}
