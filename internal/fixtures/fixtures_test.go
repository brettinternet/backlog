package fixtures_test

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/brettinternet/backlog/internal/fixtures"
	"github.com/brettinternet/backlog/internal/format"
)

func TestDeterministicTree(t *testing.T) {
	t.Parallel()
	generate := func(seed uint64) string {
		t.Helper()
		root := filepath.Join(t.TempDir(), "project")
		if err := fixtures.Generate(root, 256, seed); err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		count := 0
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			fmt.Fprintf(hash, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
			hash.Write(data)
			if filepath.Ext(path) == ".md" {
				doc, err := format.Parse(data)
				if err != nil {
					return err
				}
				item := doc.Item()
				count++
				refs := item.BlockedBy
				if item.Parent != nil {
					refs = append(refs, *item.Parent)
				}
				for _, ref := range refs {
					if ref.ID == item.Ref.ID {
						return fmt.Errorf("self reference: %s", ref.ID)
					}
					if _, err := os.Stat(filepath.Join(root, "backlog", ref.ID+".md")); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if count != 256 {
			t.Fatalf("items: %d", count)
		}
		return fmt.Sprintf("%x", hash.Sum(nil))
	}
	first := generate(42)
	if second := generate(42); first != second {
		t.Fatalf("same seed: %s != %s", first, second)
	}
	if first == generate(43) {
		t.Fatal("seed did not change fixture tree")
	}
	root := filepath.Join(t.TempDir(), "project")
	if err := fixtures.Generate(root, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := fixtures.Generate(root, 1, 1); err == nil {
		t.Fatal("existing project overwritten")
	}
}
