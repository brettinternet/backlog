package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/worklease"
	urfave "github.com/urfave/cli/v3"
)

type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
}
type doctorData struct {
	Checks []doctorCheck `json:"checks"`
}

func diagnosticCommands(directory *string, out io.Writer) []*urfave.Command {
	return []*urfave.Command{
		{Name: "trust", Usage: "Trust this project's hooks, actions, and adapters", Category: categoryProject, Flags: []urfave.Flag{&urfave.BoolFlag{Name: "show", Usage: "Show what trust covers without granting it"}}, Action: func(_ context.Context, cmd *urfave.Command) error {
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			if cfg.Discovery.ControlRoot == "" {
				return &Error{Reason: "unavailable", Message: "trust requires a project control root"}
			}
			digest, err := cfg.TrustDigest()
			if err != nil {
				return err
			}
			if !cmd.Bool("show") {
				if err = cfg.GrantTrust(); err != nil {
					return &Error{Reason: "rejected", Message: err.Error()}
				}
			}
			data := struct {
				Root    string                 `json:"root"`
				Digest  string                 `json:"digest"`
				Granted bool                   `json:"granted"`
				Entries []project.Privileged   `json:"entries"`
				Hooks   []project.HookConfig   `json:"hooks,omitempty"`
				Actions []project.ActionConfig `json:"actions,omitempty"`
				Sources []project.SourceConfig `json:"sources,omitempty"`
			}{cfg.Discovery.ControlRoot, digest, !cmd.Bool("show"), cfg.Privileged, cfg.Hooks, cfg.Actions, cfg.Sources}
			if data.Granted {
				for i := range data.Entries {
					data.Entries[i].Trusted = true
				}
			}
			if cmd.Bool("json") {
				return success(out, "trust", data)
			}
			verb := "Privileged config"
			if data.Granted {
				verb = "Trusted privileged config"
			}
			if err := writeLine(out, fmt.Sprintf("%s: %s\nSHA-256: %s", verb, data.Root, digest)); err != nil {
				return err
			}
			// Show the actual argv, conditions, cwd and source declarations before consent.
			if cmd.Bool("show") {
				b, err := json.MarshalIndent(data, "", "  ")
				if err != nil {
					return err
				}
				return writeLine(out, string(b))
			}
			return nil
		}},
		{Name: "doctor", Usage: "Diagnose configuration, trust, cache, coordination, and sources", Category: categoryProject, Flags: []urfave.Flag{&urfave.BoolFlag{Name: "online", Usage: "Also probe network sources (installed adapters only)"}}, Action: func(ctx context.Context, cmd *urfave.Command) error {
			cfg, configErr := loadConfig(cmd, *directory)
			checks := []doctorCheck{{"config", "pass", "Configuration is valid", "No action needed"}}
			if configErr != nil {
				checks[0] = doctorCheck{"config", "fail", configErr.Error(), "Correct the reported configuration key or YAML syntax"}
				for _, name := range []string{"trust", "control-root", "local-config", "cache", "worklease", "sources"} {
					checks = append(checks, doctorCheck{name, "skip", "Configuration could not be loaded", "Fix configuration and rerun bl doctor"})
				}
			} else {
				checks = append(checks, trustCheck(cfg), branchCheck(ctx, cfg), localConfigCheck(ctx, cfg), cacheCheck(ctx, cfg), workleaseCheck(ctx, cfg), sourceCheck(cfg, cmd.Bool("online")))
			}
			data := doctorData{Checks: checks}
			failed := false
			for _, c := range checks {
				failed = failed || c.Status == "fail"
			}
			if !cmd.Bool("json") {
				if err := writeDoctor(out, checks); err != nil {
					return err
				}
			}
			if failed {
				e := &Error{Reason: "rejected", Message: "doctor found failing checks"}
				if cmd.Bool("json") {
					e.Data = data
				}
				return e
			}
			if cmd.Bool("json") {
				return success(out, "doctor", data)
			}
			return nil
		}},
	}
}

func trustCheck(cfg project.Config) doctorCheck {
	c := doctorCheck{"trust", "pass", "All effective privileged declarations are trusted", "No action needed"}
	if keys := cfg.Untrusted(); len(keys) != 0 {
		c.Status = "fail"
		c.Message = "Untrusted: " + strings.Join(keys, ", ")
		c.Fix = "Review bl trust --show, then run bl trust"
	}
	return c
}

func branchCheck(ctx context.Context, cfg project.Config) doctorCheck {
	c := doctorCheck{"control-root", "pass", "Control checkout is on the default branch", "No action needed"}
	if cfg.Discovery.CommonDir == "" {
		c.Status = "skip"
		c.Message = "No Git control checkout"
		c.Fix = "Run inside a Git repository"
		return c
	}
	// Only fixed, local Git metadata queries: never fetch, run hooks, or contact a remote.
	git := func(args ...string) (string, error) {
		runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		command := exec.CommandContext(runCtx, "git", append([]string{"-C", cfg.Discovery.ControlRoot}, args...)...)
		b, err := command.Output()
		return strings.TrimSpace(string(b)), err
	}
	branch, err := git("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		c.Status = "warn"
		c.Message = "Control checkout has a detached or unreadable HEAD"
		c.Fix = "Check out the repository's default branch in the control root"
		return c
	}
	defaultRef, _ := git("symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	expected := strings.TrimPrefix(defaultRef, "refs/remotes/origin/")
	if expected == "" {
		expected, _ = git("config", "--get", "init.defaultBranch")
	}
	if expected == "" {
		for _, candidate := range []string{"main", "master"} {
			if _, e := git("show-ref", "--verify", "--quiet", "refs/heads/"+candidate); e == nil {
				expected = candidate
				break
			}
		}
	}
	if expected == "" {
		c.Status = "warn"
		c.Message = "Default branch is unknown (control checkout: " + branch + ")"
		c.Fix = "Set the local origin/HEAD symbolic ref to the repository's default branch"
	} else if branch != expected {
		c.Status = "warn"
		c.Message = "Control checkout is on " + branch + ", not " + expected
		c.Fix = "Check out " + expected + " in " + cfg.Discovery.ControlRoot
	}
	return c
}

// Check both index and ignore rules: a tracked file can also match an ignore rule.
func localConfigCheck(ctx context.Context, cfg project.Config) doctorCheck {
	c := doctorCheck{"local-config", "pass", "backlog.local.yaml is untracked and ignored", "No action needed"}
	if _, err := os.Lstat(project.LocalFile(cfg.Discovery.ControlRoot)); errors.Is(err, os.ErrNotExist) {
		c.Status, c.Message = "skip", "No backlog.local.yaml"
		return c
	}
	git := func(args ...string) error {
		runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return exec.CommandContext(runCtx, "git", append([]string{"-C", cfg.Discovery.ControlRoot}, args...)...).Run()
	}
	tracked := git("ls-files", "--error-unmatch", "--", "backlog.local.yaml") == nil
	ignored := git("check-ignore", "--quiet", "--no-index", "--", "backlog.local.yaml") == nil
	if tracked || !ignored {
		c.Status = "warn"
		c.Message = "backlog.local.yaml is tracked or not ignored"
		c.Fix = "Keep backlog.local.yaml untracked and add /backlog.local.yaml to .gitignore; bl never edits ignore rules"
	}
	return c
}

func cacheCheck(ctx context.Context, cfg project.Config) doctorCheck {
	c := doctorCheck{"cache", "pass", "Configured native caches are healthy or not yet created", "No action needed"}
	for _, s := range cfg.Sources {
		if s.Type != "native" {
			continue
		}
		path := s.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(cfg.Discovery.ControlRoot, path)
		}
		store := cache.New(cfg.Paths.CacheDir, cfg.Project.ID, s.ID, path)
		if _, err := os.Stat(store.Path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			c.Status = "fail"
			c.Message = err.Error()
			c.Fix = "Make the cache readable or move it aside, then rerun bl doctor"
			return c
		}
		// Read-only mode avoids schema creation, migrations or automatic repair.
		dsn := (&url.URL{Scheme: "file", Path: store.Path, RawQuery: "mode=ro"}).String()
		db, err := sql.Open("sqlite", dsn)
		if err == nil {
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			var result string
			err = db.QueryRowContext(probeCtx, "PRAGMA quick_check").Scan(&result)
			cancel()
			_ = db.Close()
			if err == nil && result != "ok" {
				err = errors.New(result)
			}
		}
		if err != nil {
			c.Status = "fail"
			c.Message = s.ID + ": " + err.Error()
			c.Fix = "Move the disposable cache aside and rerun a read to rebuild it"
			return c
		}
	}
	return c
}

func workleaseCheck(ctx context.Context, cfg project.Config) doctorCheck {
	c := doctorCheck{"worklease", "pass", "Worklease supports schema v2 and is at least 1.10.1", "No action needed"}
	path, err := exec.LookPath("worklease")
	if err != nil {
		c.Status = "warn"
		if cfg.Coordination.Worklease != nil {
			c.Status = "fail"
		}
		c.Message = "Worklease is not installed"
		c.Fix = "Install Worklease >= 1.10.1 and add it to PATH"
		return c
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	command := exec.CommandContext(probeCtx, path, "version", "--json")
	command.Dir = cfg.Discovery.ControlRoot
	output, err := command.Output()
	var version struct {
		OK      bool   `json:"ok"`
		Schema  int    `json:"schemaVersion"`
		Version string `json:"version"`
	}
	if err == nil {
		err = json.Unmarshal(output, &version)
	}
	valid := worklease.CompatibleVersion(version.Version)
	if err != nil || !version.OK || version.Schema != 2 || !valid {
		c.Status = "fail"
		c.Message = "Worklease version probe failed or is incompatible"
		c.Fix = "Install Worklease >= 1.10.1 with schema v2 support"
	} else {
		c.Message = "Worklease " + version.Version + " (schema v2)"
		if cfg.Coordination.Worklease != nil {
			if conflict := worklease.New(cfg).HostLocalConflict(ctx); conflict != "" {
				c.Status, c.Message, c.Fix = "fail", conflict, "Set project.id in backlog.yaml, or select a local Worklease profile"
			}
		}
	}
	return c
}

func sourceCheck(cfg project.Config, online bool) doctorCheck {
	c := doctorCheck{"sources", "pass", "Sources are configured; no network checks requested", "No action needed"}
	if len(cfg.Sources) == 0 {
		c.Status = "fail"
		c.Message = "No sources configured"
		c.Fix = "Configure sources in backlog.yaml or run bl init"
		return c
	}
	for i, s := range cfg.Sources {
		if err := cfg.RequireTrusted(fmt.Sprintf("sources[%d]", i)); err != nil {
			c.Status = "fail"
			c.Message = err.Error()
			c.Fix = "Review bl trust --show, then run bl trust"
			return c
		}
		if s.Type == "jira" {
			c.Message = "Jira Cloud configured; reads verify credentials and visible JQL scope"
			continue
		}
		if s.Type == "linear" {
			c.Message = "Linear configured; reads verify credentials, organization and team bindings"
			continue
		}
		if s.Type == "github" {
			c.Message = "GitHub configured; reads verify credentials, repository and project bindings"
			continue
		}
		if s.Type == "external" {
			if len(s.Run) == 0 || s.Run[0] == "" {
				c.Status, c.Message, c.Fix = "fail", "External source requires executable argv", "Configure sources.run"
				return c
			}
			c.Message = "External adapters configured; use bl adapter check EXEC for explicit execution"
			continue
		}
		if s.Type != "native" && s.Type != "backlog-md" {
			c.Status = "fail"
			c.Message = "Source " + s.ID + " (" + s.Type + ") is not available in this build"
			c.Fix = "Use a build with this adapter or configure a native source"
			return c
		}
		path := s.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(cfg.Discovery.ControlRoot, path)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || s.Path == "" || s.Prefix == "" {
			c.Status = "fail"
			c.Message = "Source " + s.ID + " requires an existing directory and a prefix"
			c.Fix = "Correct sources.path and sources.prefix or initialize the source"
			return c
		}
	}
	if online && c.Message == "Sources are configured; no network checks requested" {
		c.Message = "File sources are local; no network probes needed"
	}
	return c
}
