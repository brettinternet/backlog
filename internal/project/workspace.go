package project

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Workspace retains independently resolved project contexts. Source keys are
// qualified only in the composed view; providers always receive local IDs.
type Workspace struct {
	Name     string
	Project  string
	Projects []Config
	Sources  map[string]WorkspaceSource
}
type WorkspaceSource struct {
	Project int
	ID      string
}

// OpenWorkspace selects a named workspace, the first containing this project,
// or the default outside a repository. No matching implicit workspace is fine.
func OpenWorkspace(cfg Config, name string, highContrast *bool) (Config, error) {
	explicit := name != ""
	if name == "" && cfg.Discovery.CheckoutRoot == "" && cfg.DefaultWorkspace == "" {
		return cfg, nil
	}
	if name == "" && cfg.Discovery.CheckoutRoot == "" {
		name = cfg.DefaultWorkspace
	}
	for _, declaration := range cfg.Workspaces {
		if name != "" && declaration.Name != name {
			continue
		}
		projects := make([]Config, 0, len(declaration.Projects))
		contains := false
		seen := map[string]bool{}
		for _, directory := range declaration.Projects {
			if directory == "~" || strings.HasPrefix(directory, "~/") {
				home, err := os.UserHomeDir()
				if err != nil {
					return Config{}, err
				}
				directory = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(directory, "~"), "/"))
			}
			if !filepath.IsAbs(directory) {
				directory = filepath.Join(filepath.Dir(cfg.Paths.ConfigFile), directory)
			}
			p, err := Resolve(cfg.Paths, directory, highContrast)
			if err != nil {
				return Config{}, fmt.Errorf("workspace %s: %w", declaration.Name, err)
			}
			if p.Discovery.CheckoutRoot == "" {
				return Config{}, fmt.Errorf("workspace %s: %s is not a repository", declaration.Name, directory)
			}
			if seen[p.Project.ID] {
				return Config{}, fmt.Errorf("workspace %s: duplicate project identity %s", declaration.Name, p.Project.ID)
			}
			seen[p.Project.ID] = true
			contains = contains || p.Project.ID == cfg.Project.ID
			projects = append(projects, p)
		}
		if name == "" && !contains {
			continue
		}
		if len(projects) == 0 {
			return Config{}, fmt.Errorf("workspace %s is empty", declaration.Name)
		}
		anchor := projects[0]
		for _, p := range projects {
			if p.Project.ID == cfg.Project.ID {
				anchor = p
				break
			}
		}
		w := &Workspace{Name: declaration.Name, Project: anchor.Project.ID, Projects: projects, Sources: map[string]WorkspaceSource{}}
		anchor.Sources = nil
		anchor.Privileged = nil
		anchor.Actions = nil
		actions := map[string]bool{}
		for n, p := range projects {
			anchor.Privileged = append(anchor.Privileged, p.Privileged...)
			for _, action := range p.Actions {
				if !actions[action.Name] {
					actions[action.Name] = true
					anchor.Actions = append(anchor.Actions, action)
				}
			}
			for _, s := range p.Sources {
				id := url.QueryEscape(p.Project.ID) + "/" + s.ID
				w.Sources[id] = WorkspaceSource{n, s.ID}
				s.ID = id
				anchor.Sources = append(anchor.Sources, s)
			}
		}
		anchor.Workspace = w
		return anchor, nil
	}
	if explicit || name != "" {
		return Config{}, fmt.Errorf("workspace %q is not configured", name)
	}
	return cfg, nil
}

// SourceProject routes a composed source key back to its original context.
func (cfg Config) SourceProject(id string) (Config, string) {
	if cfg.Workspace != nil {
		if s, ok := cfg.Workspace.Sources[id]; ok {
			return cfg.Workspace.Projects[s.Project], s.ID
		}
	}
	return cfg, id
}
