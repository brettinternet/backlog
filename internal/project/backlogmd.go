package project

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// BacklogMDSettings reads the read-only provider catalogs and raw status names.
// path may name the project, its backlog directory, or its config file.
func BacklogMDSettings(path string) (Settings, error) {
	root, config, err := backlogMDConfig(path)
	if err != nil {
		return Settings{}, err
	}
	var values struct {
		Name     string   `yaml:"project_name"`
		Prefix   string   `yaml:"task_prefix"`
		Statuses []string `yaml:"statuses"`
		Labels   []string `yaml:"labels"`
	}
	if err := config.Decode(&values); err != nil {
		return Settings{}, err
	}
	if values.Prefix == "" {
		values.Prefix = "TASK"
	}
	if len(values.Statuses) == 0 {
		values.Statuses = []string{"To Do", "In Progress", "Done"}
	}
	result := Settings{Project: ProjectConfig{Name: values.Name}, Labels: values.Labels, Sources: []SourceConfig{{Type: "backlog-md", Path: root, Prefix: values.Prefix}}}
	for _, name := range values.Statuses {
		if strings.TrimSpace(name) == "" {
			return Settings{}, fmt.Errorf("backlog.md status must not be empty")
		}
		category := "backlog"
		switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "-", " ")) {
		case "to do", "todo":
			category = "todo"
		case "in progress", "doing", "active":
			category = "active"
		case "done", "completed":
			category = "done"
		case "canceled", "cancelled":
			category = "canceled"
		}
		result.Statuses = append(result.Statuses, StatusConfig{Key: name, Name: name, Category: category})
		if category == "active" && result.Workflow.Start == "" {
			result.Workflow.Start = name
		}
		if category == "done" && result.Workflow.Done == "" {
			result.Workflow.Done = name
		}
	}
	if !slices.ContainsFunc(result.Statuses, func(s StatusConfig) bool { return s.Key == "draft" }) {
		result.Statuses = append(result.Statuses, StatusConfig{Key: "draft", Name: "Draft", Category: "backlog"})
	}
	seen := map[string]bool{}
	for _, partition := range []string{"milestones", filepath.Join("archive", "milestones")} {
		dir := filepath.Join(root, partition)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Settings{}, err
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			file := filepath.Join(dir, entry.Name())
			data, err := backlogMDRead(file)
			if err != nil {
				return Settings{}, err
			}
			text := strings.ReplaceAll(string(data), "\r\n", "\n")
			if !strings.HasPrefix(text, "---\n") {
				return Settings{}, fmt.Errorf("%s: missing frontmatter", file)
			}
			front, body, ok := strings.Cut(text[4:], "\n---\n")
			if !ok {
				return Settings{}, fmt.Errorf("%s: missing closing frontmatter", file)
			}
			node, err := backlogMDMapping([]byte(front))
			if err != nil {
				return Settings{}, fmt.Errorf("%s: %w", file, err)
			}
			var meta struct {
				ID    string `yaml:"id"`
				Title string `yaml:"title"`
			}
			if err := node.Decode(&meta); err != nil {
				return Settings{}, err
			}
			if meta.ID == "" || meta.Title == "" || seen[meta.ID] {
				return Settings{}, fmt.Errorf("%s: missing or duplicate milestone identity", file)
			}
			seen[meta.ID] = true
			body = strings.TrimSpace(body)
			body = strings.TrimSpace(strings.TrimPrefix(body, "## Description"))
			body = strings.TrimSpace(strings.TrimPrefix(body, "<!-- SECTION:DESCRIPTION:BEGIN -->"))
			body = strings.TrimSpace(strings.TrimSuffix(body, "<!-- SECTION:DESCRIPTION:END -->"))
			result.Milestones = append(result.Milestones, Milestone{Key: meta.ID, Name: meta.Title, Description: body, Archived: partition != "milestones"})
		}
	}
	return result, nil
}

func backlogMDRead(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: expected regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("%s: exceeds 1 MiB", path)
	}
	return data, nil
}
func backlogMDMapping(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc, extra yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected YAML mapping")
	}
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("expected one YAML document")
	}
	var validate func(*yaml.Node) error
	validate = func(n *yaml.Node) error {
		if n.Kind == yaml.AliasNode || n.Anchor != "" {
			return fmt.Errorf("YAML aliases and anchors are unsupported")
		}
		seen := map[string]bool{}
		for i, child := range n.Content {
			if n.Kind == yaml.MappingNode && i%2 == 0 {
				if child.Tag != "!!str" || seen[child.Value] {
					return fmt.Errorf("invalid or duplicate YAML key %q", child.Value)
				}
				seen[child.Value] = true
			}
			if err := validate(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := validate(doc.Content[0]); err != nil {
		return nil, err
	}
	return doc.Content[0], nil
}
func backlogMDConfig(path string) (string, *yaml.Node, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	configPath := ""
	root := path
	if !info.IsDir() {
		configPath = path
		root = filepath.Dir(path)
	} else {
		for _, candidate := range []string{filepath.Join(path, "backlog.config.yml"), filepath.Join(path, "config.yml"), filepath.Join(path, "backlog", "config.yml")} {
			if isFile(candidate) {
				configPath = candidate
				root = filepath.Dir(candidate)
				break
			}
		}
	}
	if configPath == "" && info.IsDir() {
		for ancestor := filepath.Dir(path); ancestor != filepath.Dir(ancestor); ancestor = filepath.Dir(ancestor) {
			candidate := filepath.Join(ancestor, "backlog.config.yml")
			if !isFile(candidate) {
				continue
			}
			resolved, node, err := backlogMDConfig(candidate)
			if err != nil {
				return "", nil, err
			}
			if filepath.Clean(resolved) == filepath.Clean(path) {
				return resolved, node, nil
			}
		}
	}
	if configPath == "" {
		return "", nil, fmt.Errorf("%s: Backlog.md config not found", path)
	}
	data, err := backlogMDRead(configPath)
	if err != nil {
		return "", nil, err
	}
	node, err := backlogMDMapping(data)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", configPath, err)
	}
	if filepath.Base(configPath) == "backlog.config.yml" {
		directory := "backlog"
		if value := field(node, "backlog_directory"); value != nil {
			directory = value.Value
		}
		if !filepath.IsLocal(directory) {
			return "", nil, fmt.Errorf("%s: backlog_directory must be relative", configPath)
		}
		root = filepath.Join(root, directory)
	}
	return root, node, nil
}

func (cfg *Config) resolveBacklogMD() error {
	if len(cfg.Sources) == 0 {
		for _, detected := range cfg.Discovery.Detected {
			if detected.Type != "backlog-md" {
				continue
			}
			cfg.Sources = append(cfg.Sources, SourceConfig{ID: "tasks", Type: "backlog-md", Path: detected.Path})
			cfg.ReadOnlySources = map[string]bool{"tasks": true}
			cfg.Provenance["sources[0]"] = Provenance{Layer: "discovery", File: detected.Path}
			cfg.Discovery.Warnings = append(cfg.Discovery.Warnings, "Backlog.md is read-only; configure a backlog-md source in backlog.yaml to enable editing")
			break
		}
	}
	for i := range cfg.Sources {
		configured := &cfg.Sources[i]
		if configured.Type != "backlog-md" {
			continue
		}
		path := configured.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(cfg.Discovery.ControlRoot, path)
		}
		provider, err := BacklogMDSettings(path)
		if err != nil {
			return err
		}
		configured.Path = provider.Sources[0].Path
		if configured.Prefix == "" {
			configured.Prefix = provider.Sources[0].Prefix
		}
		for _, status := range provider.Statuses {
			if !slices.ContainsFunc(cfg.Statuses, func(s StatusConfig) bool { return s.Key == status.Key }) {
				cfg.Statuses = append(cfg.Statuses, status)
			}
		}
		for _, milestone := range provider.Milestones {
			if !slices.ContainsFunc(cfg.Milestones, func(m Milestone) bool { return m.Key == milestone.Key }) {
				cfg.Milestones = append(cfg.Milestones, milestone)
			}
		}
		for _, label := range provider.Labels {
			if !slices.Contains(cfg.Labels, label) {
				cfg.Labels = append(cfg.Labels, label)
			}
		}
		if cfg.Workflow.Start == "" {
			cfg.Workflow.Start = provider.Workflow.Start
		}
		if cfg.Workflow.Done == "" {
			cfg.Workflow.Done = provider.Workflow.Done
		}
	}
	return nil
}
