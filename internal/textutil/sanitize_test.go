package textutil_test

import (
	"github.com/brettinternet/backlog/internal/textutil"
	"testing"
)

// Prevent provider-controlled terminal commands, including non-color string sequences.
func TestTerminalControls(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"\x1b[31mvisible\x1b[0m", "\x1b]0;hidden\avisible", "\x1b]8;;https://example.org\x1b\\visible\x1b]8;;\x1b\\",
		"\x1bPhidden\x1b\\visible", "\x1b_hidden\x1b\\visible", "\x1b^hidden\x1b\\visible", "\x00\x07visible\x08\x7f",
	} {
		if got := textutil.Sanitize(s); got != "visible" {
			t.Errorf("%q => %q", s, got)
		}
	}
	if got := textutil.Sanitize("first\nsecond\tlast"); got != "first\nsecond\tlast" {
		t.Fatalf("body layout lost: %q", got)
	}
	if got := textutil.Line("first\nsecond\tlast"); got != "first second last" {
		t.Fatalf("table injection: %q", got)
	}
}
