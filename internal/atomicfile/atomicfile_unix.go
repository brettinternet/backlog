//go:build unix

package atomicfile

import "os"

func rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
