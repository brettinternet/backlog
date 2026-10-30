//go:build unix

package hooks

import "syscall"

// A new session has no controlling terminal to hang it up.
func detachedAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
