package project

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// TrustDigest hashes only effective project-origin privileged declarations.
// Encoding typed values makes YAML mapping order and formatting irrelevant while
// retaining every declared field and the order of argv and declaration lists.
func (cfg Config) TrustDigest() (string, error) {
	subset := struct {
		Git     *GitConfig     `json:"git,omitempty"`
		Hooks   []HookConfig   `json:"hooks"`
		Actions []ActionConfig `json:"actions"`
		Sources []SourceConfig `json:"sources"`
	}{}
	if cfg.Git.AutoCommit && cfg.Provenance["git.autoCommit"].Layer == "project" {
		subset.Git = &cfg.Git
	}
	for i, hook := range cfg.Hooks {
		if cfg.Provenance[fmt.Sprintf("hooks[%d]", i)].Layer == "project" {
			subset.Hooks = append(subset.Hooks, hook)
		}
	}
	for i, action := range cfg.Actions {
		if cfg.Provenance[fmt.Sprintf("actions[%d]", i)].Layer == "project" {
			subset.Actions = append(subset.Actions, action)
		}
	}
	for i, source := range cfg.Sources {
		if cfg.Provenance[fmt.Sprintf("sources[%d]", i)].Layer == "project" && source.Type != "native" && source.Type != "backlog-md" {
			subset.Sources = append(subset.Sources, source)
		}
	}
	data, err := json.Marshal(subset)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

type trustRecord struct {
	Version int    `json:"version"`
	Root    string `json:"root"`
	Project string `json:"project"`
	Digest  string `json:"digest"`
}

func (cfg Config) trustRecord() (trustRecord, string, error) {
	root := cfg.Discovery.ControlRoot
	if root == "" || !filepath.IsAbs(root) {
		return trustRecord{}, "", fmt.Errorf("trust requires an absolute project control root")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return trustRecord{}, "", err
	}
	if !filepath.IsAbs(cfg.Paths.StateDir) {
		return trustRecord{}, "", fmt.Errorf("trust requires an absolute state directory")
	}
	digest, err := cfg.TrustDigest()
	if err != nil {
		return trustRecord{}, "", err
	}
	record := trustRecord{Version: 1, Root: root, Project: cfg.Project.ID, Digest: digest}
	identity, err := json.Marshal([]string{root, cfg.Project.ID})
	if err != nil {
		return trustRecord{}, "", err
	}
	key := sha256.Sum256(identity)
	return record, filepath.Join(cfg.Paths.StateDir, "trust", fmt.Sprintf("%x.json", key)), nil
}

// GrantTrust records this effective configuration, never the original YAML.
func (cfg *Config) GrantTrust() error {
	record, path, err := cfg.trustRecord()
	if err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := writeAtomic(path, data, 0600); err != nil {
		return err
	}
	cfg.Privileged = privileged(*cfg)
	for i := range cfg.Privileged {
		cfg.Privileged[i].Trusted = true
	}
	return nil
}

func (cfg *Config) resolveTrust() {
	want, path, err := cfg.trustRecord()
	if err != nil {
		return
	}
	// Bound local state reads just like configuration and UI state reads.
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	var got trustRecord
	decoder := json.NewDecoder(f)
	// Records are tiny. Reject oversized files before decoding.
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return
	}
	decoder.DisallowUnknownFields()
	if decoder.Decode(&got) != nil || got != want {
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return
	}
	for i := range cfg.Privileged {
		cfg.Privileged[i].Trusted = true
	}
}

// RequireTrusted gates a declaration key such as sources[0] or hooks[1].
// Local, nonprivileged entries do not require a grant.
func (cfg Config) RequireTrusted(key string) error {
	for _, entry := range cfg.Privileged {
		if entry.Key == key && !entry.Trusted {
			return fmt.Errorf("%s is untrusted; run bl trust", key)
		}
	}
	return nil
}

// AutoCommitBlocked explains why opt-in Git execution must remain inert.
func (cfg Config) AutoCommitBlocked() string {
	if err := cfg.RequireTrusted("git.autoCommit"); err != nil {
		return err.Error()
	}
	return ""
}

// Untrusted reports inert declarations without reading state on the UI loop.
func (cfg Config) Untrusted() []string {
	var keys []string
	for _, entry := range cfg.Privileged {
		if !entry.Trusted {
			keys = append(keys, entry.Key)
		}
	}
	return keys
}
