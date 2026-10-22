package project_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/project"
)

// Saving a view must not reformat unrelated configuration or erase a view
// written by another save. Cover both legal YAML sequence styles and CRLF.
func TestSaveViewPreservesConfig(t *testing.T) {
	t.Parallel()
	for _, style := range []string{"block", "flow", "empty", "missing"} {
		for _, eol := range []string{"\n", "\r\n"} {
			t.Run(style+eol, func(t *testing.T) {
				prefix := "# credentials are not rewritten\nme: {default: '@owner'} # keep quotes\n"
				suffix := "# tail stays byte identical\nkeys: {}\n"
				middle := "views:\n  - {name: Other, query: 'priority:high'} # other comment\n"
				switch style {
				case "flow":
					middle = "views: [{name: Other, query: 'priority:high'}] # sequence comment\n"
				case "empty":
					middle = "views: [] # empty\n"
				case "missing":
					middle = ""
				}
				original := strings.ReplaceAll(prefix+middle+suffix, "\n", eol)
				path := filepath.Join(t.TempDir(), "config.yaml")
				put(t, path, original)
				v := project.ViewConfig{Name: "Saved", Query: "is:open", Sort: "-updated", Group: "priority", Layout: "tree", Columns: []string{"id", "title"}}
				if err := project.SaveView(path, true, v); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(string(raw), strings.ReplaceAll(prefix, "\n", eol)) || !strings.Contains(string(raw), strings.ReplaceAll(suffix, "\n", eol)) {
					t.Fatalf("unrelated bytes changed: %s", raw)
				}
				if style == "block" && !strings.Contains(string(raw), "  - {name: Other, query: 'priority:high'} # other comment"+eol) {
					t.Fatalf("other entry changed: %s", raw)
				}
				v.Query = "is:ready"
				if err := project.SaveView(path, true, v); err != nil {
					t.Fatal(err)
				}
				cfg, err := project.Load(project.Paths{ConfigFile: path}, nil)
				if err != nil {
					t.Fatal(err)
				}
				want := 1
				if style == "flow" || style == "block" {
					want = 2
				}
				if len(cfg.SavedViews) != want {
					t.Fatalf("lost/duplicated view: %+v", cfg.SavedViews)
				}
				got := cfg.SavedViews[want-1].ViewConfig
				if got.Query != v.Query || got.Sort != v.Sort || got.Group != v.Group || got.Layout != v.Layout || strings.Join(got.Columns, ",") != "id,title" {
					t.Fatalf("save did not round trip: %+v", got)
				}
			})
		}
	}
}

// The TUI catalog must add project and user declarations even though D16
// intentionally replaces the ordinary Settings.Views list.
func TestSavedViewCatalogOrder(t *testing.T) {
	t.Parallel()
	root := repo(t)
	put(t, filepath.Join(root, ".config", "backlog.yaml"), "project: {id: example}\nviews: [{name: Shared, query: 'is:open'}]\n")
	user := filepath.Join(t.TempDir(), "config.yaml")
	put(t, user, "views: [{name: Shared, query: 'is:ready'}]\nprojects:\n  example:\n    views: [{name: Private}]\n")
	cfg, err := project.Resolve(project.Paths{ConfigFile: user}, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, v := range cfg.SavedViews {
		ids = append(ids, v.ID)
	}
	if strings.Join(ids, ",") != "project:Shared,user:Shared,user-project:Private" {
		t.Fatal(ids)
	}
	if len(cfg.Views) != 1 || cfg.Views[0].Name != "Private" {
		t.Fatalf("D16 replacement changed: %+v", cfg.Views)
	}
}
