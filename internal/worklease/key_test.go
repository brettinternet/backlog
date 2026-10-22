package worklease

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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
	if os.Getenv("BL_PROVIDER_TESTS") != "1" {
		t.Skip("set BL_PROVIDER_TESTS=1 for Worklease 1.9.3")
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
		t.Fatalf("requires Worklease >=1.9.3 with schema-v2: %s %v", output, err)
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
