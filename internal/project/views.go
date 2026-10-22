package project

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"
)

// SavedView keeps same-named declarations in different layers independent.
type SavedView struct {
	ID         string `json:"id"`
	ViewConfig `json:",inline"`
}

func appendViews(dst []SavedView, scope string, entries []ViewConfig) []SavedView {
	for _, v := range entries {
		dst = append(dst, SavedView{ID: scope + ":" + v.Name, ViewConfig: v})
	}
	return dst
}

// SaveView edits only the named entry (or the views value for a flow sequence).
// It rereads under a stable lock so concurrent saves cannot lose other edits.
func SaveView(path string, user bool, view ViewConfig) error {
	if strings.TrimSpace(view.Name) == "" {
		return fmt.Errorf("view name must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// Lock the directory: it survives the rename and leaves no lock file in a
	// checkout's .config directory.
	lock, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	typ := reflect.TypeFor[Settings]()
	if user {
		typ = reflect.TypeFor[UserConfig]()
	}
	root, err := readConfig(path, typ)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	eol := "\n"
	if bytes.Contains(raw, []byte("\r\n")) {
		eol = "\r\n"
	}
	lines := strings.SplitAfter(string(raw), "\n")
	encode := func(v any) string {
		var b bytes.Buffer
		enc := yaml.NewEncoder(&b)
		enc.SetIndent(2)
		_ = enc.Encode(v)
		_ = enc.Close()
		return strings.ReplaceAll(b.String(), "\n", eol)
	}
	list := field(root, "views")
	var next string
	if list == nil {
		next = string(raw)
		if next != "" && !strings.HasSuffix(next, "\n") {
			next += eol
		}
		next += "views:" + eol + "  " + strings.ReplaceAll(strings.TrimSuffix(encode([]ViewConfig{view}), eol), eol, eol+"  ") + eol
	} else if list.Style&yaml.FlowStyle != 0 {
		var entries []ViewConfig
		if err = list.Decode(&entries); err != nil {
			return err
		}
		found := false
		for i := range entries {
			if entries[i].Name == view.Name {
				entries[i] = view
				found = true
				break
			}
		}
		if !found {
			entries = append(entries, view)
		}
		// Restrict the rewrite to the flow sequence, preserving surrounding bytes.
		start := byteOffset(lines, list.Line, list.Column)
		end := flowEnd(string(raw), start)
		if end < 0 {
			return fmt.Errorf("cannot locate views sequence")
		}
		var node yaml.Node
		_ = node.Encode(entries)
		node.Style = yaml.FlowStyle
		next = string(raw[:start]) + strings.TrimSuffix(encode(&node), eol) + string(raw[end:])
	} else {
		start, end := 0, 0
		for _, entry := range list.Content {
			if name := field(entry, "name"); name != nil && name.Value == view.Name {
				start = entry.Line - 1
				end = nodeLastLine(entry)
				break
			}
		}
		if end == 0 {
			start = nodeLastLine(list)
			end = start
		}
		indent := list.Column - 1
		// Mapping nodes start after '- ', while the sequence column is the dash.
		replacement := strings.Repeat(" ", indent) + strings.ReplaceAll(strings.TrimSuffix(encode([]ViewConfig{view}), eol), eol, eol+strings.Repeat(" ", indent)) + eol
		prefix := strings.Join(lines[:start], "")
		if prefix != "" && !strings.HasSuffix(prefix, "\n") {
			prefix += eol
		}
		next = prefix + replacement + strings.Join(lines[end:], "")
	}
	// Validate the final document, including the new view, before touching disk.
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
	mode := os.FileMode(0600)
	if info, e := os.Stat(path); e == nil {
		mode = info.Mode()
	}
	return writeAtomic(path, []byte(next), mode)
}

func byteOffset(lines []string, line, column int) int {
	n := column - 1
	for _, s := range lines[:line-1] {
		n += len(s)
	}
	return n
}
func nodeLastLine(n *yaml.Node) int {
	end := n.Line
	if n.Kind == yaml.ScalarNode {
		end += strings.Count(strings.TrimSuffix(n.Value, "\n"), "\n")
		if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			end++
		}
	}
	for _, c := range n.Content {
		end = max(end, nodeLastLine(c))
	}
	return end
}

// flowEnd understands YAML quotes and comments; brackets inside strings do not
// terminate the sequence. Anchors/aliases have already been rejected.
func flowEnd(s string, start int) int {
	depth := 0
	quote := byte(0)
	comment := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if comment {
			if c == '\n' {
				comment = false
			}
			continue
		}
		if quote != 0 {
			if c == '\\' && quote == '"' {
				i++
				continue
			}
			if c == quote {
				if quote == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '#':
			comment = true
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// writeAtomic follows the native source's temp/fsync/rename/directory-sync rule.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".bl-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
