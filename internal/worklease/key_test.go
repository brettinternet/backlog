package worklease

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/testkit"
)

// Vectors include non-BMP and HTML-sensitive bytes: Go's default JSON encoder
// would silently derive a different generic/Linear resource for those identities.
func TestKeyVectors(t *testing.T) {
	t.Parallel()
	vectors := readVectors(t)
	for _, v := range vectors {
		got, err := Key(v.Provider, v.Source, v.Item)
		if err != nil || got != v.Resource {
			t.Fatalf("%+v: %s %v", v, got, err)
		}
	}
}

// Source aliases, credentials and project membership must never split claims
// for the same GitHub issue across clients.
func TestGitHubResourceIdentity(t *testing.T) {
	t.Parallel()
	cfg := project.Config{Settings: project.Settings{Sources: []project.SourceConfig{{ID: "issues", Type: "github", Repo: "Owner/Repo", Account: "first"}}}}
	item := model.Item{Ref: model.Ref{Source: "issues", ID: "123"}, Title: "before", Status: "Todo"}
	first, err := resource(cfg, item)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Sources[0].ID = "renamed"
	cfg.Sources[0].Account = "second"
	cfg.Sources[0].GitHubProject = &project.GitHubProjectConfig{ID: "PVT_other"}
	item.Ref.Source = "renamed"
	item.Title, item.Status = "after", "Done"
	second, err := resource(cfg, item)
	if err != nil || first != second || first != "github:github.com%2Fowner%2Frepo#123" {
		t.Fatalf("claim identity drift: %s %s %v", first, second, err)
	}
	// A project-only source names the repository in the ID instead.
	cfg.Sources[0].Repo = ""
	item.Ref.ID = "Owner/Repo#123"
	if third, err := resource(cfg, item); err != nil || third != first {
		t.Fatalf("project-only claim identity: %s %v", third, err)
	}
	// A Projects v2 draft has no issue, so neither source may key one.
	item.Ref.ID = "PVTI_lADOdraft"
	for _, repo := range []string{"", "owner/repo"} {
		cfg.Sources[0].Repo = repo
		if key, err := resource(cfg, item); err == nil {
			t.Fatalf("draft keyed in repo %q: %s", repo, key)
		}
	}
}

type keyVector struct{ Provider, Source, Item, Resource string }

func readVectors(t *testing.T) []keyVector {
	t.Helper()
	data, err := os.ReadFile("testdata/keys.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []keyVector
	if err = json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}
func TestProviderKeyVectors(t *testing.T) {
	testkit.Unix(t, "a Worklease binary")
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1 for Worklease 1.10.1")
	}
	providerBinary := os.Getenv("BL_PROVIDER_WORKLEASE")
	testkit.Isolate(t)
	if providerBinary != "" {
		bin := t.TempDir()
		if err := os.Symlink(providerBinary, filepath.Join(bin, "worklease")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	output, err := exec.Command("worklease", "--json", "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	var version struct {
		Version       string
		SchemaVersion int
	}
	if err = json.Unmarshal(output, &version); err != nil || version.SchemaVersion != 2 || !CompatibleVersion(version.Version) {
		t.Fatalf("requires Worklease >=1.10.1 with schema-v2: %s %v", output, err)
	}
	t.Logf("provider build (vectors pinned to 1.9.3): %s", output)
	for _, v := range readVectors(t) {
		output, err = exec.Command("worklease", "--json", "key", "--provider", v.Provider, "--source", v.Source, "--item", v.Item).Output()
		if err != nil {
			t.Fatal(err)
		}
		var response struct{ Resource string }
		if err = json.Unmarshal(output, &response); err != nil || response.Resource != v.Resource {
			t.Fatalf("provider key: %+v %s %v", v, output, err)
		}
	}
}
