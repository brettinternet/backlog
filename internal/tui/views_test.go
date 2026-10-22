package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

func viewKey(m Model, code rune, text string, mod tea.KeyMod) (Model, tea.Cmd) {
	next, cmd := m.Update(tea.KeyPressMsg{Code: code, Text: text, Mod: mod})
	return next.(Model), cmd
}
func viewRows(m Model, ids ...string) Model {
	group := engine.RowGroup{Value: "todo"}
	for _, id := range ids {
		group.Rows = append(group.Rows, engine.Row{Ref: model.Ref{Source: "tasks", ID: id}, Title: id, Status: "todo"})
	}
	next, _ := m.Update(projectionMsg{Groups: []engine.RowGroup{group}})
	m = next.(Model)
	next, _ = m.Update(m.prepare()())
	return next.(Model)
}
func viewConfig(t *testing.T) project.Config {
	t.Helper()
	testkit.Isolate(t)
	root := t.TempDir()
	for _, dir := range []string{".git", ".config"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n bare = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config", "backlog.yaml"), []byte("project: {id: fixture}\nviews: [{name: Shared, query: 'is:open', layout: tree, columns: [id, title]}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConfigFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile, []byte("# keep this\nme: {default: '@owner'}\nviews: [{name: Personal, query: 'is:ready'}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
func viewOptions(cfg project.Config) Options {
	return Options{NoColor: true, Views: cfg.SavedViews, SaveView: func(v project.ViewConfig, shared bool) (project.SavedView, error) {
		path, scope := cfg.Paths.ConfigFile, "user"
		if shared {
			path = filepath.Join(cfg.Discovery.ControlRoot, ".config", "backlog.yaml")
			scope = "project"
		}
		err := project.SaveView(path, !shared, v)
		return project.SavedView{ID: scope + ":" + v.Name, ViewConfig: v}, err
	}}
}

// Catches selections restored by row number (wrong after a refresh), shared tab
// query state, and incomplete/unsafe session restoration through actual XDG I/O.
func TestViewsRestoreSession(t *testing.T) {
	cfg := viewConfig(t)
	m := viewRows(New(viewOptions(cfg)), "BL-1", "BL-2", "BL-3")
	if !strings.Contains(m.View().Content, "[Next]  Active  Mine  All  Board  Shared  Personal") {
		t.Fatal(m.View().Content)
	}
	m = send(m, "j", "j") // BL-2
	m = send(m, "g", "t")
	m = viewRows(m, "BL-1", "BL-2", "BL-3")
	m = send(m, "j") // BL-1 in Active
	m = send(m, "g", "T")
	m = viewRows(m, "BL-3", "BL-1", "BL-2")
	if row, ok := m.selectedRow(); !ok || row.Ref.ID != "BL-2" {
		t.Fatalf("selection did not follow ref: %+v", row)
	}
	m.switchView(5)
	m = viewRows(m, "BL-1", "BL-2", "BL-3")
	m = send(m, "j", "j", "j")
	// The query is the single source of sort/group, as in the editor and menus.
	m = send(m, "o", "u", "z", "p")
	m.detailOpen = true
	m.detailTab = tabDescription
	m.splitWidth = 65
	if err := m.SaveSession(cfg); err != nil {
		t.Fatal(err)
	}
	restored, warning := LoadSession(cfg)
	if warning != "" {
		t.Fatal(warning)
	}
	opts := viewOptions(cfg)
	opts.Session = restored
	// Startup first publishes an empty projection before source loading ends.
	// It must not consume the restart selection anchor.
	again := viewRows(New(opts))
	again = viewRows(again, "BL-3", "BL-2", "BL-1")
	if again.view != 5 || again.query != m.query || !strings.Contains(again.query, "sort:updated") || !strings.Contains(again.query, "group:priority") || again.detailTab != tabDescription || again.splitWidth != 65 || !again.detailOpen || again.views[5].Config.Layout != "tree" || strings.Join(again.views[5].Config.Columns, ",") != "id,title" {
		t.Fatalf("incomplete restore: %+v", restored)
	}
	if row, ok := again.selectedRow(); !ok || row.Ref.ID != "BL-3" {
		t.Fatalf("restart selection: %+v", row)
	}
	// A changed declaration (e.g. a teammate edits the project view) must win
	// over a session query captured from the old one.
	edited := viewOptions(cfg)
	edited.Session = restored
	edited.Views[0].Query = "is:open label:new"
	if got := New(edited).views[5].Config.Query; got != "is:open label:new" {
		t.Fatalf("stale session shadowed config: %q", got)
	}
	again.switchView(1)
	again = viewRows(again, "BL-3", "BL-2", "BL-1")
	if row, ok := again.selectedRow(); !ok || row.Ref.ID != "BL-1" {
		t.Fatalf("other view lost selection: %+v", row)
	}
	// Deleted selection and removed tabs must safely fall back rather than panic.
	again.switchView(5)
	again = viewRows(again, "BL-2")
	if again.selected < 0 || again.selected >= len(again.data.rows) {
		t.Fatal("missing item has no fallback")
	}
	for _, raw := range []string{"{broken", `{"version":99}`, `{"version":1,"detailTab":999}`, `{"version":1,"views":[{"id":"x","config":{"query":"unknown:field"}}]}`} {
		if err := os.WriteFile(project.UIStatePath(cfg), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		s, w := LoadSession(cfg)
		if s != nil || !strings.Contains(w, "UI state ignored") {
			t.Fatalf("corrupt state accepted: %s", raw)
		}
		opts.Session = s
		opts.Warning = w
		if !strings.Contains(New(opts).View().Content, "UI state ignored") {
			t.Fatal("warning not visible")
		}
	}
}

// Catches ctrl-s writing the wrong destination, dropping query settings, or
// adding a tab that disappears on restart. Real files, no mocked config writer.
func TestSaveViewRestart(t *testing.T) {
	cfg := viewConfig(t)
	m := viewRows(New(viewOptions(cfg)), "BL-1", "BL-2")
	m = send(m, "j", "o", "u", "z", "p")
	for _, shared := range []bool{false, true} {
		m = send(m, "ctrl+s", "ctrl+u")
		m, _ = viewKey(m, 'x', "Saved", 0)
		if shared {
			m, _ = viewKey(m, tea.KeyTab, "", 0)
		}
		var cmd tea.Cmd
		m, cmd = viewKey(m, tea.KeyEnter, "", 0)
		if cmd == nil {
			t.Fatal("save command missing")
		}
		next, _ := m.Update(cmd())
		m = next.(Model)
		if !strings.Contains(m.notice, "Saved") {
			t.Fatal(m.notice)
		}
	}
	cfg2, err := project.Resolve(cfg.Paths, cfg.Discovery.ControlRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, v := range cfg2.SavedViews {
		ids = append(ids, v.ID)
		if v.Name == "Saved" && (!strings.Contains(v.Query, "sort:updated") || !strings.Contains(v.Query, "group:priority")) {
			t.Fatalf("query lost: %+v", v)
		}
	}
	if strings.Join(ids, ",") != "project:Shared,project:Saved,user:Personal,user:Saved" {
		t.Fatal(ids)
	}
	again := New(viewOptions(cfg2))
	if !strings.Contains(again.View().Content, "Shared  Saved  Personal  Saved") {
		t.Fatal(again.View().Content)
	}
	raw, err := os.ReadFile(cfg.Paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "# keep this\nme: {default: '@owner'}\n") {
		t.Fatal("config reformatted")
	}
}
