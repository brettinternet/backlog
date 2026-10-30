//go:build unix

package oauth

import (
	"os"
	"syscall"
)

const noFollow = syscall.O_NOFOLLOW

func makePrivateDir(path string) error { return os.MkdirAll(path, 0o700) }

func privateDir(_ string, info os.FileInfo) bool { return info.Mode().Perm()&0o077 == 0 }

func privateFile(_ *os.File, info os.FileInfo) bool { return info.Mode().Perm()&0o077 == 0 }

func protectFile(file *os.File) error { return file.Chmod(0o600) }
