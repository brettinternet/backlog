package hooks

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// Without a console, closing the invoking one neither signals nor ends it.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}
