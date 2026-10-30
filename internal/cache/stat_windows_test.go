package cache

import (
	"os"
	"path/filepath"
	"testing"
)

// Without ctime, an edit that restores size and mtime, or a replacement with
// the same content and times, must still change the identity a warm start trusts.
func TestWindowsFileIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "BL-1.md")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mtime := info.ModTime()
	before, err := FileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteAt([]byte("j"), 0); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	edited, err := FileIdentity(path)
	if err != nil || edited == before {
		t.Fatalf("in-place edit kept identity %s: %v", edited, err)
	}

	replacement := filepath.Join(dir, "next")
	if err = os.WriteFile(replacement, []byte("jello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(replacement, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	replaced, err := FileIdentity(path)
	if err != nil || replaced == edited || replaced == before {
		t.Fatalf("replacement kept identity %s: %v", replaced, err)
	}
}
