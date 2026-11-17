package cli_test

import "testing"

// Catches word-walk drift that offers the wrong candidate kind: an ID where a
// field belongs, values without the shell word's prefix, or flags after "--".
func TestShellCompletion(t *testing.T) {
	root := writeFixture(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"sh"}, "show\tShow an item\n"},
		{[]string{"show", "1"}, "12\tOriginal\n"},
		{[]string{"ac", "12", "ch"}, "check\n"},
		{[]string{"set", "12", "status=d"}, "status=doing\tDoing\nstatus=done\tDone\n"},
		{[]string{"set", "12", "labels+=bug,k"}, "labels+=bug,keep\n"},
		{[]string{"ls", "-label:b"}, "-label:bug\n"},
		{[]string{"ls", "status:t"}, "status:todo\tTo Do\n"},
		{[]string{"ls", "--sort=-pri"}, "--sort=-priority\n"},
		{[]string{"new", "--source", "t"}, "tasks\tnative\n"},
		{[]string{"show", "--", ""}, ""},
	} {
		args := append(append([]string{}, tc.args...), "--generate-shell-completion")
		code, out, stderr := writeRun(root, "", args...)
		if code != 0 || stderr != "" || out != tc.want {
			t.Errorf("%q: %d %q %q, want %q", tc.args, code, out, stderr, tc.want)
		}
	}
}
