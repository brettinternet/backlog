//go:build !darwin && !linux

package external

import "os/exec"

func prepareProcess(*exec.Cmd) {}

func terminateProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
