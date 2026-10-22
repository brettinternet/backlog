package project_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

func TestUserConfig(t *testing.T) {
	home := testkit.Isolate(t)
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.ConfigFile, paths.StateDir, paths.CacheDir} {
		rel, err := filepath.Rel(home, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("path escapes isolated home: %q", path)
		}
	}
	cfg, err := project.Load(paths, nil)
	if err != nil || cfg.UI.HighContrast {
		t.Fatalf("defaults: %+v, %v", cfg, err)
	}
	for _, path := range []string{filepath.Dir(paths.ConfigFile), paths.StateDir, paths.CacheDir} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("read-only load created %s: %v", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile, []byte("ui:\n  highContrast: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = project.Load(paths, nil)
	if err != nil || !cfg.UI.HighContrast {
		t.Fatalf("user layer: %+v, %v", cfg, err)
	}
	override := false
	cfg, err = project.Load(paths, &override)
	if err != nil || cfg.UI.HighContrast {
		t.Fatalf("explicit false must override user true: %+v, %v", cfg, err)
	}
}

func TestRejectInvalidConfig(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"ui:\n  highContrast: [true]\n",
		"ui:\n  highContrast: true\n  highContrast: false\n",
		"ui:\n  highContast: true\n",
		"ui: {}\n---\nui: {highContrast: true}\n",
		"ui: [",
	} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := project.Load(project.Paths{ConfigFile: path}, nil)
			if err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("expected error naming %s, got %v", path, err)
			}
		})
	}
}

// Each removal must expose the next D16 layer, not confuse false with absent.
func TestLayerPrecedence(t *testing.T) {
	testkit.Isolate(t)
	root := repo(t)
	git(t, root, "remote", "add", "origin", "git@github.com:acme/app.git")
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	projectFile := filepath.Join(root, ".config", "backlog.yaml")
	userBase := "ui: {highContrast: true}\n"
	put(t, paths.ConfigFile, userBase+"projects:\n  github.com/acme/app:\n    ui: {highContrast: true}\n")
	put(t, projectFile, "ui: {highContrast: false}\n")
	t.Setenv("BL_HIGH_CONTRAST", "false")
	flag := true
	check := func(flag *bool, want bool, layer, file string) {
		t.Helper()
		cfg, err := project.Resolve(paths, root, flag)
		if err != nil {
			t.Fatal(err)
		}
		p := cfg.Provenance["ui.highContrast"]
		if cfg.UI.HighContrast != want || p.Layer != layer || p.File != file || p.Line < 1 {
			t.Fatalf("got %t %+v; want %t %s %s", cfg.UI.HighContrast, p, want, layer, file)
		}
	}
	check(&flag, true, "flag", "")
	check(nil, false, "environment", "")
	t.Setenv("BL_HIGH_CONTRAST", "")
	check(nil, true, "user-project", paths.ConfigFile)
	put(t, paths.ConfigFile, userBase)
	check(nil, false, "project", projectFile)
	put(t, projectFile, "{}\n")
	check(nil, true, "user", paths.ConfigFile)
	put(t, paths.ConfigFile, "{}\n")
	check(nil, false, "built-in", "")
}

// Config inspection must never grant trust to committed programs, discard
// milestone descriptions, or expose a credential-helper argv in JSON.
func TestCatalogsAndTrustBoundary(t *testing.T) {
	t.Parallel()
	root := repo(t)
	paths := project.Paths{ConfigFile: filepath.Join(t.TempDir(), "user.yaml")}
	put(t, paths.ConfigFile, "accounts: {github: [{credential: [helper, private-token]}]}\n")
	put(t, filepath.Join(root, ".config", "backlog.yaml"), `version: 1
milestones: [{key: m-1, name: First, description: Work, archived: true}]
labels: [bug, feature]
sources: [{id: tasks, type: native, path: backlog}, {id: eng, type: linear, team: ENG}]
hooks: [{name: notify, on: item.updated, run: [must-not-run]}]
actions: [{name: work, run: [must-not-run]}]
`)
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Milestones) != 1 || cfg.Milestones[0].Description != "Work" || !cfg.Milestones[0].Archived || len(cfg.Labels) != 2 {
		t.Fatalf("catalogs lost: %+v", cfg.Settings)
	}
	if len(cfg.Privileged) != 3 {
		t.Fatalf("missing boundaries: %+v", cfg.Privileged)
	}
	for _, p := range cfg.Privileged {
		if p.Trusted || p.Origin.Layer != "project" {
			t.Fatalf("trusted committed declaration: %+v", p)
		}
	}
	put(t, paths.ConfigFile, "sources: [{id: private, type: external, run: [helper]}]\nhooks: [{name: user, on: item.updated, run: [helper]}]\n")
	put(t, filepath.Join(root, ".config", "backlog.yaml"), "{}\n")
	cfg, err = project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cfg.Privileged {
		if !p.Trusted || p.Origin.Layer != "user" {
			t.Fatalf("user declaration not trusted: %+v", p)
		}
	}
}

func TestConfigLocationsAndMilestoneDuplicates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input, key string
		line       string
	}{
		{"ui:\n  highContast: true\n", "ui.highContast", "line 2"},
		{"ui:\n  highContrast: yes\n", "ui.highContrast", "line 2"},
		{"milestones:\n  - {key: one, name: A}\n  - {key: one, name: B}\n", "milestones.key", "line 3"},
		{"root: elsewhere\n", "root", "line 1"},
		{"project: {credential: [secret]}\n", "project.credential", "line 1"},
	} {
		t.Run(tc.key+tc.line, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			put(t, path, tc.input)
			_, err := project.Load(project.Paths{ConfigFile: path}, nil)
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.line) || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("diagnostic: %v", err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "large.yaml")
	put(t, path, strings.Repeat(" ", (1<<20)+1))
	if _, err := project.Load(project.Paths{ConfigFile: path}, nil); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func FuzzConfig(f *testing.F) {
	for _, seed := range []string{"ui: {highContrast: true}", "ui: [", "a: &a [*a]", "milestones: [{key: x, name: X}]"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 1<<20 {
			return
		}
		path := filepath.Join(t.TempDir(), "config.yaml")
		put(t, path, input)
		_, _ = project.Load(project.Paths{ConfigFile: path}, nil)
	})
}

func TestXDGDefaultsAndRelativePaths(t *testing.T) {
	home := testkit.Isolate(t)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, "")
	}
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	want := project.Paths{
		ConfigFile: filepath.Join(home, ".config", "backlog", "config.yaml"),
		StateDir:   filepath.Join(home, ".local", "state", "backlog"),
		CacheDir:   filepath.Join(home, ".cache", "backlog"),
	}
	if paths != want {
		t.Fatalf("got %+v, want %+v", paths, want)
	}
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "relative")
			if _, err := project.UserPaths(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("relative %s accepted: %v", name, err)
			}
		})
	}
}
