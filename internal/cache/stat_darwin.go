package cache

import (
	"fmt"
	"os"
	"syscall"
)

func fileIdentity(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("item is not a regular file")
	}
	stat := info.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%d:%d:%d:%d:%d", info.Size(), info.ModTime().UnixNano(), stat.Ino, stat.Ctimespec.Sec, stat.Ctimespec.Nsec), nil
}
