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
