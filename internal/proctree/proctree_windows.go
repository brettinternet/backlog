package proctree

import (
	"fmt"
	"os/exec"
	"runtime"
	"syscall"

	"golang.org/x/sys/windows"
)

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// Tree is a started command and every process it spawns.
type Tree struct {
	cmd *exec.Cmd
	job windows.Handle
}

// start creates the child suspended and resumes it only once it is in the
// job, so no grandchild can escape before assignment.
func (t *Tree) start() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	t.job = job
	runtime.AddCleanup(t, func(h windows.Handle) { _ = windows.CloseHandle(h) }, job)
	if t.cmd.SysProcAttr == nil {
		t.cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	t.cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err = t.cmd.Start(); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(t.cmd.Process.Pid))
	if err != nil {
		_ = t.cmd.Process.Kill()
		return err
	}
	defer windows.CloseHandle(process) //nolint:errcheck
	if err = windows.AssignProcessToJobObject(job, process); err == nil {
		if status, _, _ := ntResumeProcess.Call(uintptr(process)); status != 0 {
			err = fmt.Errorf("resume process: NTSTATUS %#x", status)
		}
	}
	if err != nil {
		_ = t.cmd.Process.Kill()
	}
	return err
}

// kill also kills the root directly: a cancel can race start before the
// suspended root joins the job.
func (t *Tree) kill() {
	_ = windows.TerminateJobObject(t.job, 1)
	_ = t.cmd.Process.Kill()
}
