package project

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/brettinternet/backlog/internal/filelock"
	"go.yaml.in/yaml/v3"
)

// SetAccountCredential adds a credential helper to a user config account that
// has no credential, env, or oauth key, leaving every other byte in place.
func SetAccountCredential(path, provider, name string, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("credential helper must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	unlock, err := filelock.LockDir(context.Background(), filepath.Dir(path))
	if err != nil {
		return err
	}
	defer unlock()
	typ := reflect.TypeFor[UserConfig]()
	root, err := readConfig(path, typ)
	if err != nil {
		return err
	}
	var entry *yaml.Node
	if list := field(field(root, "accounts"), provider); list != nil {
		for _, candidate := range list.Content {
			if n := field(candidate, "name"); n != nil && n.Value == name {
				if entry != nil {
					return fmt.Errorf("ambiguous user account name")
				}
				entry = candidate
			}
		}
	}
	if entry == nil {
		return fmt.Errorf("user account is not configured")
	}
	for _, key := range []string{"credential", "env", "oauth"} {
		if field(entry, key) != nil {
			return fmt.Errorf("user account already sets %s", key)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	eol := "\n"
	if bytes.Contains(raw, []byte("\r\n")) {
		eol = "\r\n"
	}
	helper := yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, arg := range argv {
		helper.Content = append(helper.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: arg})
	}
	encoded, err := yaml.Marshal(&helper)
	if err != nil {
		return err
	}
	value := "credential: " + strings.TrimSpace(string(encoded))
	lines := strings.SplitAfter(string(raw), "\n")
	var next string
	if entry.Style&yaml.FlowStyle != 0 {
		// Insert before the closing brace, after the last non-space byte.
		start := byteOffset(lines, entry.Line, entry.Column)
		end := flowEnd(string(raw), start)
		if end < 0 {
			return fmt.Errorf("cannot locate account mapping")
		}
		at := end - 1
		for at > start && strings.ContainsRune(" \t\r\n", rune(raw[at-1])) {
			at--
		}
		separator := ", "
		if raw[at-1] == '{' || raw[at-1] == ',' {
			separator = ""
		}
		next = string(raw[:at]) + separator + value + string(raw[at:])
	} else {
		end := nodeLastLine(entry)
		prefix := strings.Join(lines[:end], "")
		if !strings.HasSuffix(prefix, "\n") {
			prefix += eol
		}
		next = prefix + strings.Repeat(" ", entry.Column-1) + value + eol + strings.Join(lines[end:], "")
	}
	var check yaml.Node
	if err = yaml.Unmarshal([]byte(next), &check); err != nil {
		return err
	}
	if err = validateNode(check.Content[0], typ, "$"); err != nil {
		return err
	}
	if err = validateSettings(check.Content[0]); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return writeAtomic(path, []byte(next), info.Mode())
}
