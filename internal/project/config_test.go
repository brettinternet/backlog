package project_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

// Local overrides must merge maps, replace lists, and settle identity before
// selecting user-project overrides; provenance must still identify local values.
func TestLocalConfigLayering(t *testing.T) {
	t.Parallel()
	root := repo(t)
	paths := project.Paths{ConfigFile: filepath.Join(t.TempDir(), "user.yaml")}
	put(t, project.ProjectFile(root), "project: {id: shared, name: shared}\nlabels: [shared]\nready: {statuses: [todo], satisfiedBy: [done]}\n")
	put(t, project.LocalFile(root), "project: {id: private, name: local}\nlabels: [local]\nready: {statuses: [active]}\n")
	put(t, paths.ConfigFile, "projects:\n  private:\n    project: {name: user-project}\n")
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Project.ID != "private" || cfg.Project.Name != "user-project" || strings.Join(cfg.Labels, ",") != "local" || strings.Join(cfg.Ready.Statuses, ",") != "active" || strings.Join(cfg.Ready.SatisfiedBy, ",") != "done" {
		t.Fatalf("wrong layer ordering or merge: %+v", cfg.Settings)
	}
	p := cfg.Provenance["labels[0]"]
	if p.Layer != "local" || p.File != project.LocalFile(root) || p.Line != 2 {
		t.Fatalf("local provenance: %+v", p)
	}
	if cfg.Provenance["project.name"].Layer != "user-project" {
		t.Fatal("local config displaced user-project overrides")
	}
}

// Without a sprint-capable source, ready.sprint would silently make every
// item ineligible, so resolution must refuse it.
func TestReadySprintRequiresCapableSource(t *testing.T) {
	t.Parallel()
	root := repo(t)
	paths := project.Paths{ConfigFile: filepath.Join(t.TempDir(), "user.yaml")}
	file := project.ProjectFile(root)
	put(t, file, "project: {id: p}\nsources: [{id: tasks, type: native, path: backlog}]\nready: {sprint: current}\n")
	if _, err := project.Resolve(paths, root, nil); err == nil || err.Error() != file+": line 3: key ready.sprint: no source provides sprints" {
		t.Fatalf("native-only ready.sprint: %v", err)
	}
	// A user-wide default must not break native-only projects.
	put(t, file, "project: {id: p}\nsources: [{id: tasks, type: native, path: backlog}]\n")
	put(t, paths.ConfigFile, "ready: {sprint: current}\n")
	if _, err := project.Resolve(paths, root, nil); err != nil {
		t.Fatalf("user-level ready.sprint: %v", err)
	}
	put(t, file, "project: {id: p}\nsources: [{id: tasks, type: native, path: backlog}, {id: eng, type: linear, team: ENG}]\nready: {sprint: current}\n")
	if cfg, err := project.Resolve(paths, root, nil); err != nil || cfg.Ready.Sprint != "current" {
		t.Fatalf("capable ready.sprint: %+v, %v", cfg.Ready, err)
	}
}

// The GitHub adapter assigns categories itself, so a contradicting status entry
// would silently leave open issues ready to claim.
func TestGitHubStatusCategories(t *testing.T) {
	t.Parallel()
	root := repo(t)
	paths := project.Paths{ConfigFile: filepath.Join(t.TempDir(), "user.yaml")}
	file := project.ProjectFile(root)
	plain := "project: {id: p}\nsources: [{id: gh, type: github, repo: a/b}]\n"
	bound := func(id, category string) string {
		return fmt.Sprintf("{id: %s, type: github, repo: a/%s, githubProject: {owner: a, number: 1, id: P, fieldId: F, options: {opt: %s}}}", id, id, category)
	}
	for _, tc := range []struct{ config, want string }{
		{plain + "statuses: [{key: open, name: Open, category: backlog}]\n", "line 3: key statuses[0].category: GitHub assigns open the todo category, not backlog; set category: todo or remove the entry"},
		{plain + "statuses: [{key: unknown, name: Unknown, category: todo}]\n", "line 3: key statuses[0].category: GitHub assigns unknown the backlog category, not todo; set category: backlog or remove the entry"},
		{plain + "statuses: [{key: closed, name: Closed, category: todo}]\n", "line 3: key statuses[0].category: GitHub assigns closed the done or canceled category, not todo; set category: done or remove the entry"},
		{"project: {id: p}\nsources: [" + bound("x", "active") + "]\nstatuses: [{key: opt, name: Doing, category: todo}]\n", "line 3: key statuses[0].category: source x assigns opt the active category, not todo; set category: active or remove the entry"},
		{"project: {id: p}\nsources: [" + bound("x", "active") + ", " + bound("y", "todo") + "]\n", "line 2: key sources[1].githubProject.options.opt: source y maps opt to todo, but source x assigns it active; map it to one category"},
		{"project: {id: p}\nsources:\n  - id: x\n    type: github\n    repo: a/x\n    statusLabels: {ready: todo}\n    githubProject: {owner: a, number: 1, id: P, fieldId: F, options: {opt: todo}}\n", "line 6: key sources[0].statusLabels: source x sets both statusLabels and githubProject; keep one"},
		{plain[:len(plain)-3] + ", statusLabels: {ready: soon}}]\n", "line 2: key sources[0].statusLabels.ready: unknown category soon; use backlog, todo, active, done, or canceled"},
		{plain[:len(plain)-3] + ", statusLabels: {Ready: todo, ready: active}}]\n", "line 2: key sources[0].statusLabels.ready: labels Ready and ready are the same GitHub label; keep one"},
		{plain[:len(plain)-3] + ", statusLabels: {Closed: done}}]\n", "line 2: key sources[0].statusLabels.Closed: label Closed would shadow the closed status; use another label"},
		{plain[:len(plain)-3] + ", statusLabels: {open: todo}}]\n", "line 2: key sources[0].statusLabels.open: label open would shadow the open status; use another label"},
		{plain[:len(plain)-3] + ", statusLabels: {UNKNOWN: backlog}}]\n", "line 2: key sources[0].statusLabels.UNKNOWN: label UNKNOWN would shadow the unknown status; use another label"},
		{plain[:len(plain)-3] + ", statusLabels: {ready: todo}}]\nstatuses: [{key: ready, name: Ready, category: active}]\n", "line 3: key statuses[0].category: source gh assigns ready the todo category, not active; set category: todo or remove the entry"},
	} {
		put(t, file, tc.config)
		if _, err := project.Resolve(paths, root, nil); err == nil || err.Error() != file+": "+tc.want {
			t.Errorf("%q: %v", tc.config, err)
		}
	}
	put(t, file, "project: {id: p}\nsources: ["+bound("x", "active")+", "+bound("y", "active")+"]\nstatuses: [{key: open, name: Open, category: todo}, {key: closed, name: Not planned, category: canceled}, {key: unknown, name: Unknown, category: backlog}, {key: opt, name: Doing, category: active}, {key: open-ish, name: Other, category: backlog}]\n")
	if _, err := project.Resolve(paths, root, nil); err != nil {
		t.Fatalf("matching statuses: %v", err)
	}
	put(t, file, plain[:len(plain)-3]+", statusLabels: {ready: todo, in-progress: active}}]\nstatuses: [{key: unknown, name: Triage, category: backlog}, {key: closed, name: Closed, category: done}, {key: ready, name: Ready, category: todo}, {key: in-progress, name: In Progress, category: active}]\nworkflow: {start: in-progress, done: closed}\n")
	if _, err := project.Resolve(paths, root, nil); err != nil {
		t.Fatalf("label statuses: %v", err)
	}
	// Without a GitHub source, the same keys mean whatever the project says.
	put(t, file, "project: {id: p}\nsources: [{id: tasks, type: native, path: backlog}]\nstatuses: [{key: open, name: Open, category: backlog}]\n")
	if _, err := project.Resolve(paths, root, nil); err != nil {
		t.Fatalf("non-GitHub open status: %v", err)
	}
}

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
		"hooks: [{name: before, on: item.readiness, phase: before, run: [noop]}]\n",
		"hooks: [{name: before, on: hook.finished, phase: before, run: [noop]}]\n",
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
	projectFile := filepath.Join(root, "backlog.yaml")
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
	put(t, filepath.Join(root, "backlog.yaml"), `version: 1
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
	put(t, filepath.Join(root, "backlog.yaml"), "{}\n")
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

func TestOAuthOptInIsUserOnlyAndExclusive(t *testing.T) {
	testkit.Isolate(t)
	paths, err := project.UserPaths()
	if err != nil {
		t.Fatal(err)
	}
	put(t, paths.ConfigFile, "accounts:\n  linear:\n    - name: work\n      user: immutable-user\n      oauth: true\n")
	cfg, err := project.Load(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	account := cfg.Accounts["linear"][0]
	if account.Name != "work" || account.User != "immutable-user" || !account.OAuth {
		t.Fatalf("OAuth account did not resolve: %+v", account)
	}
	data, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(data), "immutable-user") || strings.Contains(string(data), "oauth") {
		t.Fatalf("user account leaked through config output: %s %v", data, err)
	}
	put(t, paths.ConfigFile, "accounts:\n  linear:\n    - name: work\n      user: immutable-user\n      oauth: true\n      credential: [helper, canary-token]\n")
	_, err = project.Load(paths, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("OAuth/helper combination accepted: %v", err)
	}
	root := repo(t)
	put(t, filepath.Join(root, "backlog.yaml"), "accounts: {linear: [{name: work, user: immutable-user, oauth: true}]}\n")
	put(t, paths.ConfigFile, "{}\n")
	if _, err = project.Resolve(paths, root, nil); err == nil || !strings.Contains(err.Error(), "accounts") {
		t.Fatalf("project-scoped OAuth account accepted: %v", err)
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

// Each provider stores assignees in its own form, so "mine" must use the
// source's account user there; me.default would match no remote item.
func TestIdentitiesPerSource(t *testing.T) {
	t.Parallel()
	cfg := project.Config{Me: project.MeConfig{Default: "@brett"}, Accounts: map[string][]project.AccountConfig{
		"linear": {{Name: "work", User: "11111111-1111-1111-1111-111111111111"}, {Name: "home", User: "22222222-2222-2222-2222-222222222222"}},
		"github": {{Host: "github.com", User: "brett"}, {Host: "ghe.example.com", User: "bdavis"}},
		"jira":   {{Host: "https://acme.atlassian.net", User: "acct-1"}},
	}}
	cfg.Sources = []project.SourceConfig{
		{ID: "tasks", Type: "native"},
		{ID: "lin", Type: "linear", Account: "home"},
		{ID: "gh", Type: "github", Account: "brett"},
		{ID: "ghe", Type: "github", Host: "ghe.example.com", Account: "bdavis"},
		{ID: "jira", Type: "jira", Site: "https://ACME.atlassian.net/", Account: "acct-1"},
		{ID: "missing", Type: "linear", Account: "nobody"},
	}
	got := cfg.Identities()
	want := map[string]string{"tasks": "@brett", "lin": "22222222-2222-2222-2222-222222222222", "gh": "brett", "ghe": "bdavis", "jira": "acct-1"}
	if !maps.Equal(got, want) {
		t.Fatalf("identities = %v, want %v", got, want)
	}
}
