// Package proctree starts a command so that its whole process tree can be
// killed: a process group on Unix, a Job Object on Windows.
package proctree

import "os/exec"

// Start starts cmd as the root of a new tree. A command created with
// exec.CommandContext kills the whole tree when its context ends.
func Start(cmd *exec.Cmd) (*Tree, error) {
	t := &Tree{cmd: cmd}
	if cmd.Cancel != nil {
		cmd.Cancel = func() error { t.Kill(); return nil }
	}
	return t, t.start()
}

// Kill kills every process in the tree. It is safe to call more than once
// and on a nil Tree.
func (t *Tree) Kill() {
	if t != nil && t.cmd.Process != nil {
		t.kill()
	}
}
