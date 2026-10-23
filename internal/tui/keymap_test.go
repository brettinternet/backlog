package tui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Catches a documented binding drifting from dispatch (including prefix
// collisions), rather than just asserting a copy of the implementation.
func TestDefaultKeymapSpec(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../docs/tui.md")
	if err != nil {
		t.Fatal(err)
	}
	section := strings.Split(strings.Split(string(data), "<!-- keymap:start -->")[1], "<!-- keymap:end -->")[0]
	var expected strings.Builder
	expected.WriteString("\n| Action ID | Keys | Action |\n| --- | --- | --- |\n")
	for _, a := range defaultKeymap() {
		expected.WriteString("| `" + a.id + "` | `" + strings.Join(a.bindings, "`, `") + "` | " + a.label + " |\n")
	}
	if section != expected.String() {
		t.Fatalf("default keymap disagrees with docs/tui.md tables\nwant:%s", expected.String())
	}
	if err := ValidateKeys(nil); err != nil {
		t.Fatal(err)
	}
}

// Catches remaps shadowing prefix actions, leaving the old key bound, or
// executing a different action through the palette than through a key.
func TestKeyOverridesAndPalette(t *testing.T) {
	t.Parallel()
	for _, overrides := range []map[string]string{{"move-down": "k"}, {"move-down": "g"}, {"move-down": "g g g"}, {"missing": "J"}, {"move-down": "ctrl+nope"}} {
		if err := ValidateKeys(overrides); err == nil {
			t.Fatalf("accepted invalid keys: %v", overrides)
		}
	}
	m := shellFixture(10)
	m.keymap, m.keyError = buildKeymap(map[string]string{"move-down": "M", "first": "v v"})
	if m.keyError != nil {
		t.Fatal(m.keyError)
	}
	m = send(m, "j")
	if m.selected != 0 {
		t.Fatal("old key still active")
	}
	m = send(m, "M")
	if m.selected != 1 {
		t.Fatal("override not applied")
	}
	m = send(m, "v")
	if !strings.Contains(m.View().Content, "First row") {
		t.Fatal("remapped prefix popup missing")
	}
	m = send(m, "v")
	if m.selected != 0 {
		t.Fatal("remapped sequence not applied")
	}
	// Every catalog entry is present even if unbound or currently disabled.
	next, _ := m.openActions(false)
	palette := next.(Model)
	if len(palette.launcher.rows) != len(m.keymap) {
		t.Fatal("palette omitted actions")
	}
	for _, row := range palette.launcher.rows {
		if row.reason != "" {
			continue
		}
		base := m
		if row.action.id == "help" || row.action.id == "palette" {
			continue
		}
		direct, dcmd := base.executeAction(row.action)
		through := palette
		through.launcher.rows = []launcherRow{row}
		through.launcher.cursor = 0
		actual, acmd := through.updateLauncher("enter", "")
		want, got := direct.(Model), actual.(Model)
		// Serial invalidation belongs to the launcher, not action semantics.
		want.launcherSerial = got.launcherSerial
		want.launcher = got.launcher
		if (want.query != got.query || want.view != got.view || want.detailOpen != got.detailOpen || want.detailTab != got.detailTab) || want.selected != got.selected || want.prefix != got.prefix || want.count != got.count || want.picker.kind != got.picker.kind || want.capture.open != got.capture.open || want.notice != got.notice || (dcmd == nil) != (acmd == nil) {
			t.Fatalf("palette differs for %s", row.action.id)
		}
	}
	// A palette digit emits the same deferred view-switch event as a physical
	// digit; it must still distinguish a shortcut from a count motion.
	for _, fromPalette := range []bool{false, true} {
		base := m
		if fromPalette {
			next, _ := base.openActions(false)
			base = next.(Model)
			for i, r := range base.launcher.rows {
				if r.action.id == "view-2" {
					base.launcher.cursor = i
				}
			}
			next, _ = base.updateLauncher("enter", "")
			base = next.(Model)
		} else {
			base = send(base, "2")
		}
		next, _ := base.Update(viewDue(base.inputSerial))
		base = next.(Model)
		if base.view != 1 {
			t.Fatalf("view shortcut failed, palette=%v", fromPalette)
		}
	}
	// Exercise physical sequence and palette, not just the shared executor.
	direct := send(m, "M")
	next, _ = m.openActions(false)
	palette = next.(Model)
	for i, r := range palette.launcher.rows {
		if r.action.id == "move-down" {
			palette.launcher.cursor = i
		}
	}
	next, _ = palette.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	palette = next.(Model)
	if direct.selected != palette.selected {
		t.Fatal("physical key and palette differ")
	}
	next, _ = m.openActions(true)
	help := next.(Model)
	next, cmd := help.updateLauncher("", "markdown")
	help = next.(Model)
	next, _ = help.Update(cmd())
	help = next.(Model)
	if len(help.launcher.rows) == 0 || !strings.Contains(help.launcher.rows[0].label, "Markdown") {
		t.Fatal("help is not searchable")
	}
}
