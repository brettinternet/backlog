package native_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"golang.org/x/sys/windows"
)

// holdOpen opens path without FILE_SHARE_DELETE, as an editor or scanner
// might, so a rename over it fails until the handle closes.
func holdOpen(t *testing.T, path string) windows.Handle {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// breakWrites holds the target open past the bounded rename retry.
func breakWrites(t *testing.T, path string) { holdOpen(t, path) }

func wantWriteFailure(t *testing.T, err error) { reason(t, err, "unavailable") }

// D8: a target held open briefly is written once the holder lets go.
func TestWriteRetriesHeldTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := openSource(t, root, filepath.Join(root, "git-common"))
	item := create(t, s)
	h := holdOpen(t, filepath.Join(root, "backlog", item.Ref.ID+".md"))
	time.AfterFunc(100*time.Millisecond, func() { _ = windows.CloseHandle(h) })
	changed, err := s.Apply(context.Background(), model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "title", Value: "Changed"})
	if err != nil || changed.Title != "Changed" {
		t.Fatalf("held write: %#v %v", changed, err)
	}
}
