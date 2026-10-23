//go:build !darwin && !linux

package remote

import "os/exec"

func prepareProcess(cmd *exec.Cmd) {}
func terminateProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
