package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
	"github.com/brettinternet/backlog/internal/textutil"
	urfave "github.com/urfave/cli/v3"
	"go.yaml.in/yaml/v3"
)

var initPrefixPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*$`)

type checkFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
}

type checkResult struct {
	Findings []checkFinding `json:"findings"`
}

type initResult struct {
	Config string `json:"config"`
	Path   string `json:"path"`
	Prefix string `json:"prefix"`
}

func maintenanceCommands(directory *string, out, errOut io.Writer) []*urfave.Command {
	return []*urfave.Command{
		{Name: "init", Usage: "Initialize a native backlog", Flags: []urfave.Flag{
			&urfave.StringFlag{Name: "prefix", Value: "BL", Usage: "Uppercase ID prefix"},
			&urfave.StringFlag{Name: "path", Value: "backlog", Usage: "Native item directory"},
		}, Action: func(_ context.Context, cmd *urfave.Command) error {
			result, err := initialize(*directory, cmd.String("prefix"), cmd.String("path"))
			if err != nil {
				return err
			}
			if cmd.Bool("json") {
				return success(out, "init", result)
			}
			_, err = fmt.Fprintf(out, "Initialized native backlog at %s (prefix %s)\n", result.Path, result.Prefix)
			return err
		}},
		{Name: "check", Usage: "Validate the native backlog", Flags: []urfave.Flag{
			&urfave.BoolFlag{Name: "fix", Usage: "Apply documented safe repairs"},
		}, Action: func(ctx context.Context, cmd *urfave.Command) error {
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			backends, err := maintenanceSources(cfg)
			if err != nil {
				return err
			}
			findings, err := native.Check(ctx, backends, cmd.Bool("fix"))
			if err != nil {
				return maintenanceFailure(err)
			}
			result := checkResult{Findings: make([]checkFinding, 0, len(findings))}
			errorCount := 0
			for _, finding := range findings {
				path := finding.Path
				if relative, relErr := filepath.Rel(cfg.Discovery.ControlRoot, path); relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					path = filepath.ToSlash(relative)
				}
				result.Findings = append(result.Findings, checkFinding{finding.Code, finding.Severity, path, finding.Line, finding.Message})
				if finding.Severity == "error" {
					errorCount++
				}
			}
			if cmd.Bool("json") {
				if errorCount > 0 {
					return &Error{Reason: "rejected", Message: fmt.Sprintf("check found %d error(s)", errorCount), Data: result}
				}
				return success(out, "check", result)
			}
			for _, finding := range result.Findings {
				// Messages can quote item content such as an unknown status.
				if _, err := fmt.Fprintln(out, textutil.Line(fmt.Sprintf("%s:%d: %s: %s: %s", finding.Path, finding.Line, finding.Severity, finding.Code, finding.Message))); err != nil {
					return err
				}
			}
			if len(result.Findings) == 0 {
				if _, err := fmt.Fprintln(out, "No findings."); err != nil {
					return err
				}
			}
			if errorCount > 0 {
				return &Error{Reason: "rejected", Message: fmt.Sprintf("check found %d error(s)", errorCount)}
			}
			return nil
		}},
		{Name: "renumber", Usage: "Rename an ID and rewrite its references", ArgsUsage: "FROM TO", Action: func(ctx context.Context, cmd *urfave.Command) error {
			if cmd.Args().Len() != 2 {
				return &Error{Reason: "usage", Message: "renumber requires FROM and TO"}
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			backends, err := maintenanceSources(cfg)
			if err != nil {
				return err
			}
			args := cmd.Args().Slice()
			result, err := native.Renumber(ctx, backends, args[0], args[1])
			if err != nil {
				return maintenanceFailure(err)
			}
			if cmd.Bool("json") {
				return success(out, "renumber", result)
			}
			_, err = fmt.Fprintf(out, "Renumbered %s:%s to %s (%d file(s) changed)\n", result.Source, result.From, result.To, result.Files)
			return err
		}},
	}
}

func initialize(directory, prefix, sourcePath string) (initResult, error) {
	if !initPrefixPattern.MatchString(prefix) {
		return initResult{}, &Error{Reason: "usage", Message: "--prefix must match [A-Z][A-Z0-9]*"}
	}
	if strings.TrimSpace(sourcePath) == "" {
		return initResult{}, &Error{Reason: "usage", Message: "--path must not be empty"}
	}
	discovery, err := project.Discover(directory)
	if err != nil {
		return initResult{}, &Error{Reason: "invalid-config", Message: err.Error()}
	}
	if discovery.ControlRoot == "" {
		return initResult{}, &Error{Reason: "invalid-config", Message: "bl init requires a Git repository"}
	}
	root := discovery.ControlRoot
	configPath := filepath.Join(root, ".config", "backlog.yaml")
	if _, err := os.Lstat(configPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		return initResult{}, &Error{Reason: "rejected", Message: "refusing to overwrite existing project config: " + configPath}
	}
	id, hostLocal, err := project.Identity(discovery)
	if err != nil {
		return initResult{}, &Error{Reason: "invalid-config", Message: err.Error()}
	}
	// A host-local path is not a shared identity; leave it to the runtime default
	// so the committed config does not pin one machine's checkout path.
	if hostLocal {
		id = ""
	}
	storedPath := filepath.Clean(sourcePath)
	if !filepath.IsAbs(storedPath) {
		storedPath = filepath.ToSlash(storedPath)
	}
	resolvedPath := storedPath
	if !filepath.IsAbs(resolvedPath) {
		resolvedPath = filepath.Join(root, resolvedPath)
	}
	settings := project.Settings{
		Version: 1,
		Project: project.ProjectConfig{ID: id, Name: filepath.Base(root)},
		Sources: []project.SourceConfig{{ID: "tasks", Type: "native", Path: storedPath, Prefix: prefix}},
		Statuses: []project.StatusConfig{
			{Key: "todo", Name: "To Do", Category: string(model.Todo)},
			{Key: "in-progress", Name: "In Progress", Category: string(model.Active)},
			{Key: "done", Name: "Done", Category: string(model.Done)},
		},
		Workflow: project.WorkflowConfig{Start: "in-progress", Done: "done"},
		Ready:    project.ReadyConfig{Statuses: []string{"todo"}, SatisfiedBy: []string{"done", "canceled"}, SkipContainers: true},
	}
	data, err := yaml.Marshal(settings)
	if err != nil {
		return initResult{}, &Error{Reason: "internal", Message: err.Error()}
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return initResult{}, &Error{Reason: "internal", Message: err.Error()}
	}
	if err := os.MkdirAll(resolvedPath, 0755); err != nil {
		return initResult{}, &Error{Reason: "internal", Message: err.Error()}
	}
	if err := createConfigExclusive(configPath, data); err != nil {
		if errors.Is(err, os.ErrExist) {
			return initResult{}, &Error{Reason: "rejected", Message: "refusing to overwrite existing project config: " + configPath}
		}
		return initResult{}, &Error{Reason: "internal", Message: err.Error()}
	}
	return initResult{Config: configPath, Path: resolvedPath, Prefix: prefix}, nil
}

func createConfigExclusive(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".bl-init-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0644); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func maintenanceSources(cfg project.Config) ([]*native.Source, error) {
	if len(cfg.Sources) == 0 {
		return nil, &Error{Reason: "invalid-config", Message: "no sources are configured"}
	}
	statuses := make(map[string]model.StatusCategory, len(cfg.Statuses))
	for _, status := range cfg.Statuses {
		statuses[status.Key] = model.StatusCategory(status.Category)
	}
	result := make([]*native.Source, 0, len(cfg.Sources))
	for _, configured := range cfg.Sources {
		if configured.Type != "native" {
			return nil, &Error{Reason: "unavailable", Message: fmt.Sprintf("source %s (%s) is not available for native maintenance", configured.ID, configured.Type)}
		}
		path := configured.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(cfg.Discovery.ControlRoot, path)
		}
		backend, err := native.New(native.Options{ID: configured.ID, Path: path, Prefix: configured.Prefix, CommonDir: cfg.Discovery.CommonDir, ProjectID: cfg.Project.ID, Statuses: statuses})
		if err != nil {
			return nil, &Error{Reason: "invalid-config", Message: err.Error()}
		}
		result = append(result, backend)
	}
	return result, nil
}

func maintenanceFailure(err error) error {
	var refusal *source.Error
	if errors.As(err, &refusal) {
		switch refusal.Reason {
		case "ambiguous-id", "invalid-id":
			return &Error{Reason: "usage", Message: refusal.Message}
		case "not-found":
			return &Error{Reason: "not-found", Message: refusal.Message}
		case "conflict":
			return &Error{Reason: "conflict", Message: refusal.Message}
		case "rejected":
			return &Error{Reason: "rejected", Message: refusal.Message}
		case "invalid-source":
			return &Error{Reason: "invalid-config", Message: refusal.Message}
		}
	}
	return &Error{Reason: "internal", Message: err.Error()}
}
