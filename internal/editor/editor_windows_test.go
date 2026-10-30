package editor

import (
	"context"
	"slices"
	"testing"
)

// $EDITOR is a command line, not a shell script: quoted paths with spaces and
// flags must survive, and the file is the last argument.
func TestWindowsEditorArgv(t *testing.T) {
	for editor, want := range map[string][]string{
		"": {"notepad", `C:\work\BL-1.md`},
		`"C:\Program Files\Editor\ed.exe" --wait`: {`C:\Program Files\Editor\ed.exe`, "--wait", `C:\work\BL-1.md`},
	} {
		cmd, err := command(context.Background(), editor, `C:\work\BL-1.md`)
		if err != nil || !slices.Equal(cmd.Args, want) {
			t.Fatalf("%q: %q %v", editor, cmd.Args, err)
		}
	}
}
