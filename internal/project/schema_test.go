package project_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/project"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

// Keep the published examples executable: drift here otherwise sends users to
// editor diagnostics for a configuration copied from our own specification.
func TestDesignConfigSchemas(t *testing.T) {
	t.Parallel()
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"project-config.json", "user-config.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "docs", "schemas", name))
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource("https://backlog.test/"+name, doc); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "design.md"))
	if err != nil {
		t.Fatal(err)
	}
	section := strings.SplitN(strings.SplitN(string(data), "## 5. Projects, configuration, and trust", 2)[1], "## 6.", 2)[0]
	blocks := strings.Split(section, "```yaml\n")
	if len(blocks) != 3 {
		t.Fatalf("expected two §5 examples, got %d", len(blocks)-1)
	}
	for i, name := range []string{"project-config.json", "user-config.json"} {
		example := strings.SplitN(blocks[i+1], "```", 2)[0]
		var document any
		if err := yaml.Unmarshal([]byte(example), &document); err != nil {
			t.Fatal(err)
		}
		schema, err := compiler.Compile("https://backlog.test/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(document); err != nil {
			t.Fatalf("%s example: %v", name, err)
		}
		// Runtime loading must accept the same examples as the editor schemas.
		path := filepath.Join(t.TempDir(), "config.yaml")
		put(t, path, example)
		if i == 0 {
			root := repo(t)
			put(t, filepath.Join(root, "backlog.yaml"), example)
			if _, err := project.Resolve(project.Paths{ConfigFile: filepath.Join(t.TempDir(), "absent.yaml")}, root, nil); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := project.Load(project.Paths{ConfigFile: path}, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
}
