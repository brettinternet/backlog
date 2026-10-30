//go:build unix

package proctree

import (
	"os/exec"
	"syscall"
)

// Tree is a started command and every process it spawns.
type Tree struct{ cmd *exec.Cmd }

func (t *Tree) start() error {
	if t.cmd.SysProcAttr == nil {
		t.cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	t.cmd.SysProcAttr.Setpgid = true
	return t.cmd.Start()
}

func (t *Tree) kill() {
	if err := syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = t.cmd.Process.Kill()
	}
}
