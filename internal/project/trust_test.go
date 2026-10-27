package project_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/project"
	"go.yaml.in/yaml/v3"
)

// Grants must survive formatting changes, but never authorize changed program
// arguments, execution conditions, action flags, or provider destinations.
func TestTrustDigestAndInvalidation(t *testing.T) {
	t.Parallel()
	root := repo(t)
	paths := project.Paths{ConfigFile: filepath.Join(t.TempDir(), "user.yaml"), StateDir: t.TempDir()}
	file := filepath.Join(root, "backlog.yaml")
	put(t, file, "project: {id: example/app}\nhooks: [{name: notify, on: item.updated, run: [notify]}]\nactions: [{name: work, run: [work]}]\nsources: [{id: remote, type: linear, team: ENG}]\n")
	resolve := func(t *testing.T) project.Config {
		t.Helper()
		cfg, err := project.Resolve(paths, root, nil)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	cfg := resolve(t)
	if len(cfg.Untrusted()) != 3 || cfg.RequireTrusted("hooks[0]") == nil {
		t.Fatalf("missing trust boundary: %+v", cfg.Privileged)
	}
	digest, err := cfg.TrustDigest()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.GrantTrust(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Untrusted()) != 0 {
		t.Fatal("grant did not update markers")
	}
	put(t, file, "sources: [{team: ENG, type: linear, id: remote}]\nactions: [{run: [work], name: work}]\nhooks: [{run: [notify], on: item.updated, name: notify}]\nproject: {id: example/app}\nlabels: [harmless]\n")
	cfg = resolve(t)
	got, err := cfg.TrustDigest()
	if err != nil || got != digest || len(cfg.Untrusted()) != 0 {
		t.Fatalf("mapping order or nonprivileged change invalidated grant: %s %v %+v", got, err, cfg.Privileged)
	}
	original := cfg.Settings
	// Each field can change execution or what the user approved. Exercise all
	// current fields so adding a field cannot accidentally leave it unhashed.
	for _, group := range []string{"Hooks", "Actions", "Sources"} {
		value := reflect.ValueOf(original).FieldByName(group).Index(0)
		for i := 0; i < value.NumField(); i++ {
			t.Run(group+"/"+value.Type().Field(i).Name, func(t *testing.T) {
				settings := original
				settings.Hooks = append([]project.HookConfig(nil), original.Hooks...)
				settings.Actions = append([]project.ActionConfig(nil), original.Actions...)
				settings.Sources = append([]project.SourceConfig(nil), original.Sources...)
				field := reflect.ValueOf(&settings).Elem().FieldByName(group).Index(0).Field(i)
				switch field.Kind() {
				case reflect.String:
					field.SetString(field.String() + "changed")
					if value.Type().Field(i).Name == "Phase" {
						field.SetString("before")
					}
					if group == "Sources" && value.Type().Field(i).Name == "Type" {
						field.SetString("github")
					}
				case reflect.Bool:
					field.SetBool(!field.Bool())
				case reflect.Slice:
					field.Set(reflect.ValueOf([]string{"different-program", "arg"}))
					if group == "Sources" && value.Type().Field(i).Name == "Statuses" {
						// Provider sources report their own statuses, so the
						// field can't change on this one without being refused.
						data, err := yaml.Marshal(settings)
						if err != nil {
							t.Fatal(err)
						}
						put(t, file, string(data))
						if _, err := project.Resolve(paths, root, nil); err == nil || !strings.Contains(err.Error(), "reports its own statuses") {
							t.Fatalf("statuses on a provider source: %v", err)
						}
						return
					}
				case reflect.Pointer:
					field.Set(reflect.ValueOf(&project.GitHubProjectConfig{ID: "PVT_changed", Owner: "changed", Number: 1, FieldID: "PVTSSF_changed", Options: map[string]string{"option": "active"}, AllowWrites: true}))
				case reflect.Map:
					field.Set(reflect.ValueOf(map[string]string{"changed": "active"}))
				default:
					t.Fatalf("unhandled field %s", field.Kind())
				}
				data, err := yaml.Marshal(settings)
				if err != nil {
					t.Fatal(err)
				}
				put(t, file, string(data))
				changed := resolve(t)
				if len(changed.Untrusted()) != 3 {
					t.Fatalf("field change retained trust: %+v", changed.Privileged)
				}
			})
		}
	}
}

// A private file is still repository-controlled: hooks, actions, network
// sources and auto-commit must remain inert and changes must revoke consent.
func TestLocalConfigTrust(t *testing.T) {
	t.Parallel()
	root := repo(t)
	paths := project.Paths{ConfigFile: filepath.Join(t.TempDir(), "user.yaml"), StateDir: t.TempDir()}
	local := project.LocalFile(root)
	contents := "project: {id: private}\ngit: {autoCommit: true}\nhooks: [{name: local, on: item.updated, run: [notify]}]\nactions: [{name: local, run: [notify]}]\nsources: [{id: remote, type: github, repo: owner/repo}]\n"
	put(t, local, contents)
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Untrusted()) != 4 || cfg.RequireTrusted("hooks[0]") == nil || cfg.AutoCommitBlocked() == "" {
		t.Fatalf("local file bypassed trust: %+v", cfg.Privileged)
	}
	digest, err := cfg.TrustDigest()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.GrantTrust(); err != nil {
		t.Fatal(err)
	}
	cfg, err = project.Resolve(paths, root, nil)
	if err != nil || len(cfg.Untrusted()) != 0 {
		t.Fatalf("local grant not retained: %+v %v", cfg.Privileged, err)
	}
	put(t, local, strings.Replace(contents, "run: [notify]", "run: [changed]", 1))
	cfg, err = project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := cfg.TrustDigest()
	if err != nil || changed == digest || len(cfg.Untrusted()) != 4 {
		t.Fatalf("local hook change retained trust: %s %v %+v", changed, err, cfg.Privileged)
	}
}

// A copied grant must not authorize another clone with the same project ID;
// damaged state must not break read-only configuration inspection or grant trust.
func TestTrustRootBindingAndDamagedState(t *testing.T) {
	t.Parallel()
	paths := project.Paths{ConfigFile: filepath.Join(t.TempDir(), "user.yaml"), StateDir: t.TempDir()}
	roots := []string{repo(t), repo(t)}
	for _, root := range roots {
		put(t, filepath.Join(root, "backlog.yaml"), "project: {id: same/project}\nactions: [{name: work, run: [must-not-run]}]\n")
	}
	cfg, err := project.Resolve(paths, roots[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.GrantTrust(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(paths.StateDir, "trust"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("trust state: %v %v", entries, err)
	}
	grantPath := filepath.Join(paths.StateDir, "trust", entries[0].Name())
	grant, err := os.ReadFile(grantPath)
	if err != nil {
		t.Fatal(err)
	}
	other, err := project.Resolve(paths, roots[1], nil)
	if err != nil || len(other.Untrusted()) != 1 {
		t.Fatalf("other clone trusted: %+v %v", other.Privileged, err)
	}
	if err := other.GrantTrust(); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(filepath.Join(paths.StateDir, "trust"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("root keys collided: %v %v", entries, err)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(grantPath) {
			put(t, filepath.Join(paths.StateDir, "trust", entry.Name()), string(grant))
		}
	}
	other, err = project.Resolve(paths, roots[1], nil)
	if err != nil || len(other.Untrusted()) != 1 {
		t.Fatalf("copied grant trusted: %+v %v", other.Privileged, err)
	}
	for _, bad := range []string{"{", string(grant) + "{}", "{}"} {
		put(t, grantPath, bad)
		cfg, err = project.Resolve(paths, roots[0], nil)
		if err != nil || len(cfg.Untrusted()) != 1 {
			t.Fatalf("damaged state did not fail closed: %+v %v", cfg.Privileged, err)
		}
	}
}

// Effective user overrides are trusted, not included in the project digest,
// and must not accidentally trust project entries left in other lists.
func TestTrustUserLayering(t *testing.T) {
	t.Parallel()
	root := repo(t)
	paths := project.Paths{ConfigFile: filepath.Join(t.TempDir(), "user.yaml"), StateDir: t.TempDir()}
	put(t, filepath.Join(root, "backlog.yaml"), "project: {id: example/app}\nhooks: [{name: project, on: item.updated, run: [project]}]\nactions: [{name: project, run: [project]}]\n")
	put(t, paths.ConfigFile, "sources: [{id: private, type: external, run: [user]}]\nprojects:\n  example/app:\n    hooks: [{name: private, on: item.updated, run: [user]}]\n")
	cfg, err := project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Untrusted(), []string{"actions[0]"}) {
		t.Fatalf("layer boundaries: %+v", cfg.Privileged)
	}
	if err := cfg.RequireTrusted("sources[0]"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.GrantTrust(); err != nil {
		t.Fatal(err)
	}
	digest, err := cfg.TrustDigest()
	if err != nil {
		t.Fatal(err)
	}
	put(t, paths.ConfigFile, "sources: [{id: private, type: external, run: [new-user-program]}]\nprojects:\n  example/app:\n    hooks: [{name: changed, on: item.updated, run: [new-user-program]}]\n")
	cfg, err = project.Resolve(paths, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := cfg.TrustDigest()
	if err != nil || got != digest || len(cfg.Untrusted()) != 0 {
		t.Fatalf("user change affected grant: %s %v %+v", got, err, cfg.Privileged)
	}
}
