package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestReferenceChild(t *testing.T) {
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "reference-child" {
		main()
		os.Exit(0)
	}
}

// Catches drift between the frozen schema and the actual reference wire format,
// particularly the shared model's capitalized fields and source-qualified refs.
func TestReferenceWireMatchesFrozenSchema(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "external-adapter", "v1", "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const uri = "https://backlog.test/adapter-v1.json"
	if err := compiler.AddResource(uri, doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(uri)
	if err != nil {
		t.Fatal(err)
	}
	requests := []string{
		`{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolMajors":[1],"sourceId":"example"}}`,
		`{"jsonrpc":"2.0","id":"2","method":"capabilities","params":{"sourceId":"example","deadline":"2099-01-01T00:00:00Z"}}`,
		`{"jsonrpc":"2.0","id":"3","method":"list","params":{"sourceId":"example","deadline":"2099-01-01T00:00:00Z","cursor":"","maxItems":100,"maxBytes":786432}}`,
		`{"jsonrpc":"2.0","id":"4","method":"read","params":{"sourceId":"example","deadline":"2099-01-01T00:00:00Z","id":"REF-1"}}`,
	}
	for _, line := range requests {
		var value any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(value); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReferenceChild$", "--", "reference-child")
	cmd.Stdin = strings.NewReader(strings.Join(requests, "\n") + "\n")
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	count := 0
	for scanner.Scan() {
		var value map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(value); err != nil {
			t.Fatalf("%s: %v", scanner.Text(), err)
		}
		if value["error"] != nil {
			t.Fatal(value)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if count != len(requests) {
		t.Fatalf("responses %d, want %d", count, len(requests))
	}
}
