// Package backlogmd imports Backlog.md projects into the native file format.
package backlogmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"go.yaml.in/yaml/v3"
)

// Report is the complete, JSON-serializable import plan and result. File
// mappings show each source field and body section beside its native output.
type Report struct {
	Input                string                `json:"input"`
	Target               string                `json:"target"`
	DryRun               bool                  `json:"dryRun"`
	Counts               OutputCounts          `json:"counts"`
	OutputPaths          []string              `json:"outputPaths"`
	Files                []FileReport          `json:"files"`
	Configuration        []FieldMapping        `json:"configuration"`
	Decisions            []string              `json:"decisions"`
	Skipped              []SkippedContent      `json:"skipped"`
	UnresolvedMilestones []UnresolvedMilestone `json:"unresolvedMilestones"`
}

type OutputCounts struct {
	Tasks      int `json:"tasks"`
	Drafts     int `json:"drafts"`
	Archived   int `json:"archived"`
	Milestones int `json:"milestones"`
	Files      int `json:"files"`
}

type FileReport struct {
	SourcePath string         `json:"sourcePath"`
	OutputPath string         `json:"outputPath"`
	Kind       string         `json:"kind"`
	ID         string         `json:"id,omitempty"`
	Mappings   []FieldMapping `json:"mappings"`
}

type FieldMapping struct {
	Source   string `json:"source"`
	Output   string `json:"output"`
	Original any    `json:"original"`
	Value    any    `json:"value"`
}

type SkippedContent struct {
	Path    string `json:"path"`
	Field   string `json:"field,omitempty"`
	Reason  string `json:"reason"`
	Content any    `json:"content,omitempty"`
}

type UnresolvedMilestone struct {
	SourcePath string `json:"sourcePath"`
	TaskID     string `json:"taskId"`
	Value      string `json:"value"`
}

type sourceConfig struct {
	root        string
	configPath  string
	config      map[string]any
	projectName string
	taskPrefix  string
	statuses    []string
	labels      []string
	dod         []string
}

type taskInput struct {
	doc             sourceDocument
	sourcePath      string
	id              string
	title           string
	status          string
	typeName        string
	due             string
	created         time.Time
	updated         time.Time
	createdFallback bool
	updatedFallback bool
	ordinal         *big.Int
	rank            string
	archived        bool
	draft           bool
	outputPath      string
	outputBytes     []byte
	milestone       string
	mappedStatus    string
	refMap          map[string]string
}

type milestoneInput struct {
	doc         sourceDocument
	sourcePath  string
	id          string
	name        string
	description string
	archived    bool
}

type outputPlan struct {
	files   map[string][]byte
	paths   []string
	target  string
	existed bool
}

// Import reads and validates a Backlog.md source directly, then either writes
// the resulting native project or returns the same validated plan without
// writing when dryRun is true. target must be absent or an empty directory.
func Import(ctx context.Context, input, target string, dryRun bool) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if strings.TrimSpace(input) == "" || strings.TrimSpace(target) == "" {
		return Report{}, fmt.Errorf("input and target directories are required")
	}
	inputAbs, err := filepath.Abs(input)
	if err != nil {
		return Report{}, fmt.Errorf("resolve input: %w", err)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return Report{}, fmt.Errorf("resolve target: %w", err)
	}
	existed, err := validateTarget(targetAbs)
	if err != nil {
		return Report{}, err
	}
	source, err := locateSource(inputAbs)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Input: inputAbs, Target: targetAbs, DryRun: dryRun,
		OutputPaths: []string{}, Files: []FileReport{}, Configuration: []FieldMapping{},
		Decisions: []string{
			"Task IDs are upper-cased while retaining zero padding and dotted suffixes; references use the same IDs.",
			"Source ordinals are sorted and converted to spaced lexical rank keys, preserving their order and leaving insertion gaps.",
			"Completed-partition items use the done status; draft-partition items use a draft status in the backlog category.",
			"Legacy timestamps without a timezone are interpreted as UTC and emitted as RFC 3339 UTC timestamps.",
			"Unknown task frontmatter fields are retained as native fields; unknown body sections are retained under Description.",
		},
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := collectSkipped(source.root, source.configPath, &report); err != nil {
		return report, err
	}
	taskFiles, err := scanPartitions(ctx, source.root, &report)
	if err != nil {
		return report, err
	}
	milestoneFiles, err := scanMilestones(ctx, source.root, &report)
	if err != nil {
		return report, err
	}
	tasks := make([]taskInput, 0, len(taskFiles))
	for _, candidate := range taskFiles {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		task, err := readTask(candidate)
		if err != nil {
			return report, fmt.Errorf("%s: %w", candidate.relative, err)
		}
		if task.createdFallback {
			report.Decisions = append(report.Decisions, fmt.Sprintf("%s has no created timestamp; used the updated timestamp or source file modification time.", task.sourcePath))
		}
		if task.updatedFallback {
			report.Decisions = append(report.Decisions, fmt.Sprintf("%s has no updated timestamp; used the created timestamp.", task.sourcePath))
		}
		tasks = append(tasks, task)
	}
	milestones := make([]milestoneInput, 0, len(milestoneFiles))
	for _, candidate := range milestoneFiles {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		milestone, err := readMilestone(candidate)
		if err != nil {
			return report, fmt.Errorf("%s: %w", candidate.relative, err)
		}
		milestones = append(milestones, milestone)
	}
	idMap, err := makeIDMap(tasks)
	if err != nil {
		return report, err
	}
	milestoneConfig, milestoneByKey, milestoneByTitle, err := makeMilestones(milestones, &report)
	if err != nil {
		return report, err
	}
	prefix, err := nativePrefix(source, tasks)
	if err != nil {
		return report, err
	}
	if _, configured := source.config["task_prefix"]; !configured {
		report.Decisions = append(report.Decisions, fmt.Sprintf("No task_prefix was configured; inferred native ID prefix %s.", prefix))
	}
	assignRanks(tasks)
	statuses, statusLookup, workflow, ready := makeStatuses(source, tasks)

	plan := outputPlan{files: map[string][]byte{}, target: targetAbs, existed: existed}
	for i := range tasks {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := mapTask(&tasks[i], idMap, milestoneByKey, milestoneByTitle, statusLookup, &report); err != nil {
			return report, fmt.Errorf("%s: %w", tasks[i].sourcePath, err)
		}
		if _, exists := plan.files[tasks[i].outputPath]; exists {
			return report, fmt.Errorf("output path collision: %s", tasks[i].outputPath)
		}
		plan.files[tasks[i].outputPath] = bytes.Clone(tasks[i].outputBytes)
		report.Files = append(report.Files, taskFileReport(tasks[i]))
		report.Counts.Tasks++
		if tasks[i].draft {
			report.Counts.Drafts++
		}
		if tasks[i].archived {
			report.Counts.Archived++
		}
	}
	for _, milestone := range milestones {
		var output project.Milestone
		for _, candidate := range milestoneConfig {
			if strings.EqualFold(candidate.Key, milestone.id) {
				output = candidate
				break
			}
		}
		fileReport := milestoneFileReport(milestone, output)
		report.Files = append(report.Files, fileReport)
		report.Counts.Milestones++
	}
	settings := project.Settings{
		Version:    1,
		Project:    project.ProjectConfig{Name: source.projectName},
		Sources:    []project.SourceConfig{{ID: "tasks", Type: "native", Path: "backlog", Prefix: prefix}},
		Statuses:   statuses,
		Workflow:   workflow,
		Milestones: milestoneConfig,
		Labels:     append([]string(nil), source.labels...),
		Ready:      ready,
		Defaults:   project.DefaultsConfig{DOD: append([]string(nil), source.dod...)},
	}
	configBytes, err := yaml.Marshal(settings)
	if err != nil {
		return report, fmt.Errorf("encode native project config: %w", err)
	}
	configPath := filepath.Join(".config", "backlog.yaml")
	plan.files[configPath] = configBytes
	report.Configuration = configMappings(source, statuses, milestoneConfig, ready, prefix, settings)
	if value, exists := source.config["default_status"]; exists {
		report.Skipped = append(report.Skipped, SkippedContent{Path: source.configPath, Field: "default_status", Reason: "Backlog.md default status has no native project-wide default", Content: value})
	}
	if value, exists := source.config["backlog_directory"]; exists {
		report.Skipped = append(report.Skipped, SkippedContent{Path: source.configPath, Field: "backlog_directory", Reason: "used to locate source files; not copied to native project config", Content: value})
	}
	for _, key := range sortedKeys(source.config) {
		if !knownConfigKey(key) {
			report.Skipped = append(report.Skipped, SkippedContent{Path: source.configPath, Field: key, Reason: "Backlog.md setting has no native project-config equivalent", Content: source.config[key]})
		}
	}
	if len(report.UnresolvedMilestones) > 0 {
		report.Decisions = append(report.Decisions, "Milestone values that match neither an imported key nor a unique title are kept unchanged and listed as unresolved.")
	}
	if len(report.Skipped) > 0 {
		report.Decisions = append(report.Decisions, "Files and source settings that have no native representation are listed under skipped; skipped file contents are not copied.")
	}
	for relative, data := range plan.files {
		if filepath.IsAbs(relative) || !filepath.IsLocal(filepath.Clean(relative)) {
			return report, fmt.Errorf("unsafe output path %q", relative)
		}
		if relative != configPath {
			if _, err := format.Parse(data); err != nil {
				return report, fmt.Errorf("validate native item %s: %w", relative, err)
			}
		}
		plan.paths = append(plan.paths, relative)
	}
	sort.Strings(plan.paths)
	for _, relative := range plan.paths {
		report.OutputPaths = append(report.OutputPaths, filepath.Join(targetAbs, relative))
	}
	report.Counts.Files = len(plan.files)
	sort.Slice(report.Files, func(i, j int) bool { return report.Files[i].SourcePath < report.Files[j].SourcePath })
	sort.Slice(report.Skipped, func(i, j int) bool {
		if report.Skipped[i].Path != report.Skipped[j].Path {
			return report.Skipped[i].Path < report.Skipped[j].Path
		}
		return report.Skipped[i].Field < report.Skipped[j].Field
	})
	sort.Slice(report.UnresolvedMilestones, func(i, j int) bool {
		if report.UnresolvedMilestones[i].SourcePath != report.UnresolvedMilestones[j].SourcePath {
			return report.UnresolvedMilestones[i].SourcePath < report.UnresolvedMilestones[j].SourcePath
		}
		return report.UnresolvedMilestones[i].Value < report.UnresolvedMilestones[j].Value
	})
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if !dryRun {
		if err := writePlan(ctx, plan); err != nil {
			return report, err
		}
	}
	return report, nil
}

type sourceCandidate struct {
	absolute  string
	relative  string
	partition string
	archived  bool
	draft     bool
	completed bool
}

func locateSource(input string) (sourceConfig, error) {
	info, err := os.Stat(input)
	if err != nil {
		return sourceConfig{}, fmt.Errorf("source directory %s: %w", input, err)
	}
	if !info.IsDir() {
		return sourceConfig{}, fmt.Errorf("source is not a directory: %s", input)
	}
	rootConfigPath := filepath.Join(input, "backlog.config.yml")
	if fileExists(rootConfigPath) {
		data, err := os.ReadFile(rootConfigPath)
		if err != nil {
			return sourceConfig{}, err
		}
		config, err := decodeYAMLMapping(data)
		if err != nil {
			return sourceConfig{}, fmt.Errorf("%s: %w", rootConfigPath, err)
		}
		directory := "backlog"
		if value, ok := config["backlog_directory"]; ok {
			directory, ok = value.(string)
			if !ok || strings.TrimSpace(directory) == "" || !filepath.IsLocal(directory) {
				return sourceConfig{}, fmt.Errorf("%s: backlog_directory must be a non-empty relative path", rootConfigPath)
			}
		}
		backlogRoot := filepath.Join(input, filepath.Clean(directory))
		if !within(input, backlogRoot) {
			return sourceConfig{}, fmt.Errorf("%s: backlog_directory escapes the source project", rootConfigPath)
		}
		if !isDir(backlogRoot) {
			return sourceConfig{}, fmt.Errorf("backlog.md directory does not exist: %s", backlogRoot)
		}
		source, err := makeSourceConfig(backlogRoot, rootConfigPath, config)
		if err != nil {
			return sourceConfig{}, err
		}
		if source.projectName == "" {
			source.projectName = filepath.Base(input)
		}
		return source, nil
	}
	folderConfigPath := filepath.Join(input, "config.yml")
	if fileExists(folderConfigPath) || hasBacklogPartition(input) {
		if !fileExists(folderConfigPath) {
			configPath, config, projectRoot, found, err := ancestorConfigFor(input)
			if err != nil {
				return sourceConfig{}, err
			}
			if found {
				source, err := makeSourceConfig(input, configPath, config)
				if err != nil {
					return sourceConfig{}, err
				}
				if source.projectName == "" {
					source.projectName = filepath.Base(projectRoot)
				}
				return source, nil
			}
		}
		config := map[string]any{}
		if fileExists(folderConfigPath) {
			data, err := os.ReadFile(folderConfigPath)
			if err != nil {
				return sourceConfig{}, err
			}
			config, err = decodeYAMLMapping(data)
			if err != nil {
				return sourceConfig{}, fmt.Errorf("%s: %w", folderConfigPath, err)
			}
		}
		source, err := makeSourceConfig(input, folderConfigPath, config)
		if err != nil {
			return sourceConfig{}, err
		}
		if source.projectName == "" {
			source.projectName = filepath.Base(input)
		}
		return source, nil
	}
	backlogRoot := filepath.Join(input, "backlog")
	folderConfigPath = filepath.Join(backlogRoot, "config.yml")
	if isDir(backlogRoot) {
		config := map[string]any{}
		if fileExists(folderConfigPath) {
			data, err := os.ReadFile(folderConfigPath)
			if err != nil {
				return sourceConfig{}, err
			}
			config, err = decodeYAMLMapping(data)
			if err != nil {
				return sourceConfig{}, fmt.Errorf("%s: %w", folderConfigPath, err)
			}
		}
		source, err := makeSourceConfig(backlogRoot, folderConfigPath, config)
		if err != nil {
			return sourceConfig{}, err
		}
		if source.projectName == "" {
			source.projectName = filepath.Base(input)
		}
		return source, nil
	}
	return sourceConfig{}, fmt.Errorf("%s is not a Backlog.md project or backlog directory", input)
}

func ancestorConfigFor(backlogRoot string) (string, map[string]any, string, bool, error) {
	for projectRoot := filepath.Dir(backlogRoot); ; projectRoot = filepath.Dir(projectRoot) {
		configPath := filepath.Join(projectRoot, "backlog.config.yml")
		if fileExists(configPath) {
			data, err := os.ReadFile(configPath)
			if err != nil {
				return "", nil, "", false, err
			}
			config, err := decodeYAMLMapping(data)
			if err != nil {
				return "", nil, "", false, fmt.Errorf("%s: %w", configPath, err)
			}
			directory := "backlog"
			if value, exists := config["backlog_directory"]; exists {
				var ok bool
				directory, ok = value.(string)
				if !ok || strings.TrimSpace(directory) == "" || !filepath.IsLocal(directory) {
					return "", nil, "", false, fmt.Errorf("%s: backlog_directory must be a non-empty relative path", configPath)
				}
			}
			selected := filepath.Clean(filepath.Join(projectRoot, directory))
			if within(projectRoot, selected) && selected == filepath.Clean(backlogRoot) {
				return configPath, config, projectRoot, true, nil
			}
		}
		parent := filepath.Dir(projectRoot)
		if parent == projectRoot {
			break
		}
	}
	return "", nil, "", false, nil
}

func makeSourceConfig(root, configPath string, config map[string]any) (sourceConfig, error) {
	if configPath != "" && !fileExists(configPath) {
		configPath = ""
	}
	result := sourceConfig{root: root, configPath: configPath, config: config}
	result.projectName, _ = config["project_name"].(string)
	if value, exists := config["task_prefix"]; exists {
		prefix, ok := scalarString(value)
		if !ok || strings.TrimSpace(prefix) == "" {
			return sourceConfig{}, fmt.Errorf("task_prefix must be a non-empty string")
		}
		result.taskPrefix = prefix
	}
	var err error
	if value, ok := config["statuses"]; ok {
		result.statuses, err = stringList(value, "statuses", false)
		if err != nil {
			return sourceConfig{}, err
		}
	}
	if value, ok := config["labels"]; ok {
		result.labels, err = stringList(value, "labels", false)
		if err != nil {
			return sourceConfig{}, err
		}
	}
	if value, ok := config["definition_of_done"]; ok {
		result.dod, err = stringList(value, "definition_of_done", false)
		if err != nil {
			return sourceConfig{}, err
		}
	}
	return result, nil
}

func validateTarget(target string) (bool, error) {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect target: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("target must be an absent or empty project directory: %s", target)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return false, fmt.Errorf("inspect target: %w", err)
	}
	if len(entries) != 0 {
		return false, fmt.Errorf("refusing non-empty target directory: %s", target)
	}
	return true, nil
}

func scanPartitions(ctx context.Context, root string, report *Report) ([]sourceCandidate, error) {
	definitions := []sourceCandidate{
		{partition: "tasks"},
		{partition: "completed", completed: true},
		{partition: filepath.Join("archive", "tasks"), archived: true},
		{partition: "drafts", draft: true},
		{partition: filepath.Join("archive", "drafts"), archived: true, draft: true},
	}
	var result []sourceCandidate
	for _, definition := range definitions {
		base := filepath.Join(root, definition.partition)
		if !isDir(base) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				report.Skipped = append(report.Skipped, SkippedContent{Path: filepath.ToSlash(relative), Reason: "symbolic links are not imported"})
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				report.Skipped = append(report.Skipped, SkippedContent{Path: filepath.ToSlash(relative), Reason: "only regular Markdown task files are imported"})
				return nil
			}
			result = append(result, sourceCandidate{absolute: path, relative: filepath.ToSlash(relative), partition: definition.partition, archived: definition.archived, draft: definition.draft, completed: definition.completed})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", definition.partition, err)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].relative < result[j].relative })
	return result, nil
}

func scanMilestones(ctx context.Context, root string, report *Report) ([]sourceCandidate, error) {
	definitions := []sourceCandidate{{partition: "milestones"}, {partition: filepath.Join("archive", "milestones"), archived: true}}
	var result []sourceCandidate
	for _, definition := range definitions {
		base := filepath.Join(root, definition.partition)
		if !isDir(base) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				report.Skipped = append(report.Skipped, SkippedContent{Path: filepath.ToSlash(relative), Reason: "symbolic links are not imported"})
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				report.Skipped = append(report.Skipped, SkippedContent{Path: filepath.ToSlash(relative), Reason: "only regular Markdown milestone files are imported"})
				return nil
			}
			result = append(result, sourceCandidate{absolute: path, relative: filepath.ToSlash(relative), partition: definition.partition, archived: definition.archived})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", definition.partition, err)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].relative < result[j].relative })
	return result, nil
}

func collectSkipped(root, configPath string, report *Report) error {
	known := map[string]bool{
		"tasks": true, "completed": true, "drafts": true, "archive": true,
		"milestones": true,
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if known[entry.Name()] || (entry.Name() == "config.yml" || entry.Name() == "backlog.config.yml") && filepath.Clean(filepath.Join(root, entry.Name())) == filepath.Clean(configPath) {
			continue
		}
		path := entry.Name()
		if entry.IsDir() {
			path += "/"
		}
		reason := "not an imported Backlog.md partition"
		if entry.Name() == "docs" || entry.Name() == "decisions" {
			reason = "documentation and decisions are not imported"
		}
		report.Skipped = append(report.Skipped, SkippedContent{Path: path, Reason: reason})
	}
	archive := filepath.Join(root, "archive")
	if isDir(archive) {
		entries, err := os.ReadDir(archive)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() == "tasks" || entry.Name() == "drafts" || entry.Name() == "milestones" {
				continue
			}
			path := filepath.ToSlash(filepath.Join("archive", entry.Name()))
			if entry.IsDir() {
				path += "/"
			}
			report.Skipped = append(report.Skipped, SkippedContent{Path: path, Reason: "archive partition is not supported"})
		}
	}
	return nil
}

func readTask(candidate sourceCandidate) (taskInput, error) {
	data, err := os.ReadFile(candidate.absolute)
	if err != nil {
		return taskInput{}, err
	}
	doc, err := parseSourceDocument(data)
	if err != nil {
		return taskInput{}, err
	}
	id, err := requiredString(doc.fields, "id")
	if err != nil {
		return taskInput{}, err
	}
	title, err := requiredString(doc.fields, "title")
	if err != nil {
		return taskInput{}, err
	}
	if strings.ContainsAny(title, "\r\n") {
		return taskInput{}, fmt.Errorf("title must be one line in the native format")
	}
	status, hasStatus := scalarString(doc.fields["status"])
	if candidate.completed {
		status, hasStatus = "Done", true
	}
	if candidate.draft {
		status, hasStatus = "Draft", true
	}
	if !hasStatus || strings.TrimSpace(status) == "" {
		return taskInput{}, fmt.Errorf("missing required status")
	}
	status = strings.TrimSpace(status)
	typeName, err := optionalStringField(doc.fields, "type")
	if err != nil {
		return taskInput{}, err
	}
	due, err := optionalDateField(doc.fields, "due")
	if err != nil {
		return taskInput{}, err
	}
	milestone, err := optionalStringField(doc.fields, "milestone")
	if err != nil {
		return taskInput{}, err
	}
	stat, err := os.Stat(candidate.absolute)
	if err != nil {
		return taskInput{}, err
	}
	created, createdOK, err := readDateField(doc.fields, "created_date", "created")
	if err != nil {
		return taskInput{}, fmt.Errorf("created_date: %w", err)
	}
	updated, updatedOK, err := readDateField(doc.fields, "updated_date", "updated")
	if err != nil {
		return taskInput{}, fmt.Errorf("updated_date: %w", err)
	}
	createdFallback, updatedFallback := !createdOK, !updatedOK
	if !createdOK {
		if updatedOK {
			created = updated
		} else {
			created = stat.ModTime().UTC().Truncate(time.Second)
		}
	}
	if !updatedOK {
		updated = created
	}
	var ordinal *big.Int
	if value, ok := doc.fields["ordinal"]; ok && value != nil {
		ordinal, err = integerValue(value)
		if err != nil {
			return taskInput{}, fmt.Errorf("ordinal: %w", err)
		}
	}
	return taskInput{
		doc: doc, sourcePath: candidate.relative,
		id: id, title: title, status: status, typeName: typeName, due: due, created: created, updated: updated,
		createdFallback: createdFallback, updatedFallback: updatedFallback,
		ordinal: ordinal, archived: candidate.archived, draft: candidate.draft,
		milestone: milestone,
	}, nil
}

func readMilestone(candidate sourceCandidate) (milestoneInput, error) {
	data, err := os.ReadFile(candidate.absolute)
	if err != nil {
		return milestoneInput{}, err
	}
	doc, err := parseSourceDocument(data)
	if err != nil {
		return milestoneInput{}, err
	}
	id, err := requiredString(doc.fields, "id")
	if err != nil {
		return milestoneInput{}, err
	}
	name, err := requiredString(doc.fields, "title")
	if err != nil {
		return milestoneInput{}, err
	}
	description := doc.body.sections["description"]
	for _, unknown := range doc.body.unknown {
		description = joinSection(description, "## "+unknown.heading+"\n\n"+unknown.content)
	}
	return milestoneInput{doc: doc, sourcePath: candidate.relative, id: id, name: name, description: description, archived: candidate.archived}, nil
}

func makeIDMap(tasks []taskInput) (map[string]string, error) {
	result := make(map[string]string, len(tasks))
	seen := make(map[string]string, len(tasks))
	for _, task := range tasks {
		upper := strings.ToUpper(task.id)
		if !validBacklogID(upper) {
			return nil, fmt.Errorf("invalid task ID %q in %s", task.id, task.sourcePath)
		}
		canonical := canonicalID(upper)
		if previous, exists := seen[canonical]; exists {
			return nil, fmt.Errorf("task ID collision after native lookup normalization: %s and %s", previous, task.sourcePath)
		}
		seen[canonical] = task.sourcePath
		result[canonical] = upper
	}
	return result, nil
}

func validBacklogID(id string) bool {
	parts := strings.Split(id, "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for i, r := range parts[0] {
		if i == 0 && !(r >= 'A' && r <= 'Z') || i > 0 && !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	for _, component := range strings.Split(parts[1], ".") {
		if component == "" {
			return false
		}
		for _, r := range component {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func canonicalID(id string) string {
	prefix, numeric, _ := strings.Cut(strings.ToUpper(id), "-")
	parts := strings.Split(numeric, ".")
	for i, part := range parts {
		number, ok := new(big.Int).SetString(part, 10)
		if ok {
			parts[i] = number.String()
		}
	}
	return prefix + "-" + strings.Join(parts, ".")
}

func assignRanks(tasks []taskInput) {
	order := make([]int, len(tasks))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := tasks[order[i]], tasks[order[j]]
		if a.ordinal == nil && b.ordinal != nil {
			return false
		}
		if a.ordinal != nil && b.ordinal == nil {
			return true
		}
		if a.ordinal != nil && b.ordinal != nil {
			if comparison := a.ordinal.Cmp(b.ordinal); comparison != 0 {
				return comparison < 0
			}
		}
		return a.sourcePath < b.sourcePath
	})
	if len(order) == 0 {
		return
	}
	step := ^uint64(0) / uint64(len(order)+1)
	for position, index := range order {
		if tasks[index].ordinal == nil {
			if rawRank, ok := scalarString(tasks[index].doc.fields["rank"]); ok && rawRank != "" {
				tasks[index].rank = rawRank
				continue
			}
		}
		tasks[index].rank = fmt.Sprintf("a%016x", step*uint64(position+1))
	}
}

func nativePrefix(source sourceConfig, tasks []taskInput) (string, error) {
	prefix := strings.TrimSpace(source.taskPrefix)
	if prefix == "" {
		for _, task := range tasks {
			if !task.archived && !task.draft {
				prefix, _, _ = strings.Cut(task.id, "-")
				break
			}
		}
	}
	if prefix == "" {
		for _, task := range tasks {
			if !task.draft {
				prefix, _, _ = strings.Cut(task.id, "-")
				break
			}
		}
	}
	if prefix == "" && len(tasks) > 0 {
		prefix, _, _ = strings.Cut(tasks[0].id, "-")
	}
	if prefix == "" {
		prefix = "TASK"
	}
	prefix = strings.ToUpper(prefix)
	if !validNativePrefix(prefix) {
		return "", fmt.Errorf("invalid task_prefix %q; native prefix must start with an uppercase letter and contain only uppercase letters or digits", prefix)
	}
	return prefix, nil
}

func validNativePrefix(prefix string) bool {
	if prefix == "" || prefix[0] < 'A' || prefix[0] > 'Z' {
		return false
	}
	for i := 1; i < len(prefix); i++ {
		if (prefix[i] < 'A' || prefix[i] > 'Z') && (prefix[i] < '0' || prefix[i] > '9') {
			return false
		}
	}
	return true
}

func makeStatuses(source sourceConfig, tasks []taskInput) ([]project.StatusConfig, map[string]string, project.WorkflowConfig, project.ReadyConfig) {
	statuses := make([]project.StatusConfig, 0)
	byKey := map[string]int{}
	lookup := map[string]string{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		key := statusKey(name)
		identity := statusIdentity(name)
		if index, ok := byKey[key]; ok {
			lookup[identity] = statuses[index].Key
			return
		}
		category := statusCategory(name)
		statuses = append(statuses, project.StatusConfig{Key: key, Name: name, Category: string(category)})
		byKey[key] = len(statuses) - 1
		lookup[identity] = key
	}
	for _, status := range source.statuses {
		add(status)
	}
	for _, task := range tasks {
		add(task.status)
	}
	hasCategory := func(category model.StatusCategory) bool {
		for _, status := range statuses {
			if status.Category == string(category) {
				return true
			}
		}
		return false
	}
	if !hasCategory(model.Todo) {
		add("To Do")
	}
	if !hasCategory(model.Active) {
		add("In Progress")
	}
	if !hasCategory(model.Done) {
		add("Done")
	}
	for _, name := range []string{"To Do", "Todo", "Open", "Ready", "In Progress", "Doing", "Active", "Done", "Complete", "Completed", "Closed", "Canceled", "Cancelled", "Draft"} {
		if key, ok := byKey[statusKey(name)]; ok {
			lookup[statusIdentity(name)] = statuses[key].Key
		}
	}
	start, done := "", ""
	readyStatuses := []string{}
	satisfied := []string{}
	for _, status := range statuses {
		switch model.StatusCategory(status.Category) {
		case model.Active:
			if start == "" {
				start = status.Key
			}
		case model.Todo:
			readyStatuses = append(readyStatuses, status.Key)
		case model.Done:
			if done == "" {
				done = status.Key
			}
			satisfied = append(satisfied, status.Key)
		case model.Canceled:
			satisfied = append(satisfied, status.Key)
		}
	}
	if start == "" && len(readyStatuses) > 0 {
		start = readyStatuses[0]
	}
	if done == "" {
		done = "done"
	}
	excludeLabels := []string{}
	for _, label := range source.labels {
		if strings.EqualFold(label, "blocked") || strings.EqualFold(label, "deferred") {
			excludeLabels = append(excludeLabels, label)
		}
	}
	return statuses, lookup, project.WorkflowConfig{Start: start, Done: done}, project.ReadyConfig{
		Statuses: readyStatuses, SatisfiedBy: satisfied, ExcludeLabels: excludeLabels, SkipContainers: true,
	}
}

func statusIdentity(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func statusKey(value string) string {
	switch statusIdentity(value) {
	case "todo", "todolist", "open", "ready", "new":
		return "todo"
	case "inprogress", "doing", "active", "started":
		return "in-progress"
	case "done", "complete", "completed", "closed":
		return "done"
	case "canceled", "cancelled":
		return "canceled"
	case "draft":
		return "draft"
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	key := strings.Trim(b.String(), "-")
	if key == "" {
		return "status"
	}
	return key
}

func statusCategory(value string) model.StatusCategory {
	switch statusKey(value) {
	case "todo":
		return model.Todo
	case "in-progress":
		return model.Active
	case "done":
		return model.Done
	case "canceled":
		return model.Canceled
	default:
		return model.Backlog
	}
}

func mapTask(task *taskInput, idMap map[string]string, milestoneByKey, milestoneByTitle map[string]string, statusLookup map[string]string, report *Report) error {
	id := strings.ToUpper(task.id)
	statusKey, ok := statusLookup[statusIdentity(task.status)]
	if !ok {
		statusKey = statusKeyForTask(task.status)
	}
	task.mappedStatus = statusKey
	task.refMap = idMap
	refs, err := mergedLists(task.doc.fields, "assignee", "assignees")
	if err != nil {
		return err
	}
	labels, err := optionalList(task.doc.fields, "labels")
	if err != nil {
		return err
	}
	dependencies, err := mergedLists(task.doc.fields, "dependencies", "blocked_by")
	if err != nil {
		return err
	}
	for i, dependency := range dependencies {
		dependencies[i], err = rewriteRef(dependency, idMap)
		if err != nil {
			return fmt.Errorf("dependencies: %w", err)
		}
	}
	var parent *model.Ref
	parentValue, hasParent, err := mergedScalar(task.doc.fields, "parent_task_id", "parent")
	if err != nil {
		return err
	}
	if hasParent && parentValue != "" {
		value, err := rewriteRef(parentValue, idMap)
		if err != nil {
			return fmt.Errorf("parent: %w", err)
		}
		parsed := parseReference(value)
		parent = &parsed
	}
	allRefs, err := mergedLists(task.doc.fields, "references", "documentation", "refs")
	if err != nil {
		return err
	}
	files, err := mergedLists(task.doc.fields, "modified_files", "files")
	if err != nil {
		return err
	}
	related, err := optionalList(task.doc.fields, "related")
	if err != nil {
		return err
	}
	for i, reference := range related {
		related[i], err = rewriteRef(reference, idMap)
		if err != nil {
			return fmt.Errorf("related: %w", err)
		}
	}
	duplicateValue, hasDuplicate, err := mergedScalar(task.doc.fields, "duplicate_of")
	if err != nil {
		return err
	}
	var duplicateOf *model.Ref
	if hasDuplicate && duplicateValue != "" {
		value, err := rewriteRef(duplicateValue, idMap)
		if err != nil {
			return fmt.Errorf("duplicate_of: %w", err)
		}
		parsed := parseReference(value)
		duplicateOf = &parsed
	}
	priorityValue, err := optionalStringField(task.doc.fields, "priority")
	if err != nil {
		return err
	}
	priority := model.Priority(strings.ToLower(priorityValue))
	if priority != "" && priority != model.Urgent && priority != model.High && priority != model.Medium && priority != model.Low {
		return fmt.Errorf("unsupported priority %q", priorityValue)
	}
	milestone := task.milestone
	if milestone != "" {
		if mapped, found := milestoneByKey[strings.ToLower(milestone)]; found {
			milestone = mapped
		} else if mapped, found := milestoneByTitle[strings.ToLower(strings.TrimSpace(milestone))]; found {
			milestone = mapped
		} else {
			report.UnresolvedMilestones = append(report.UnresolvedMilestones, UnresolvedMilestone{SourcePath: task.sourcePath, TaskID: id, Value: task.milestone})
		}
	}
	for _, comment := range parseComments(task.doc.body.comments, task.updated) {
		if comment.dateFallback {
			report.Decisions = append(report.Decisions, fmt.Sprintf("%s contains a comment without a valid created timestamp; used the task's updated timestamp.", task.sourcePath))
			break
		}
	}
	created := task.created
	updated := task.updated
	item := model.Item{
		Ref: model.Ref{ID: id}, Title: task.title, Type: task.typeName,
		Status: statusKey, Priority: priority, Rank: task.rank, Due: task.due,
		Assignees: refs, Labels: labels, Milestone: milestone,
		Parent: parent, DuplicateOf: duplicateOf, BlockedBy: make([]model.Ref, 0, len(dependencies)),
		Related: make([]model.Ref, 0, len(related)), Refs: allRefs, Files: files, Description: task.doc.body.sections["description"],
		Created: created, Updated: updated, Fields: unknownFields(task.doc.fields),
	}
	for _, ref := range dependencies {
		item.BlockedBy = append(item.BlockedBy, parseReference(ref))
	}
	for _, ref := range related {
		item.Related = append(item.Related, parseReference(ref))
	}
	serialized, err := format.Serialize(item)
	if err != nil {
		return err
	}
	body := renderNativeBody(task.doc.body, task.updated)
	separator := []byte("\n---\n")
	index := bytes.Index(serialized, separator)
	if index < 0 {
		return fmt.Errorf("native serializer returned invalid frontmatter")
	}
	output := append(bytes.Clone(serialized[:index+len(separator)]), []byte(body)...)
	if _, err := format.Parse(output); err != nil {
		return fmt.Errorf("native item validation: %w", err)
	}
	task.outputPath = filepath.ToSlash(filepath.Join("backlog", id+".md"))
	if task.archived {
		task.outputPath = filepath.ToSlash(filepath.Join("backlog", "archive", id+".md"))
	}
	task.outputBytes = output
	task.milestone = milestone
	return nil
}

func statusKeyForTask(value string) string { return statusKey(value) }

func renderNativeBody(body parsedBody, fallback time.Time) string {
	var sections []struct{ name, key, value string }
	sections = append(sections, struct{ name, key, value string }{"Acceptance criteria", "criteria", body.sections["criteria"]})
	sections = append(sections, struct{ name, key, value string }{"Definition of done", "dod", body.sections["dod"]})
	sections = append(sections, struct{ name, key, value string }{"Plan", "plan", body.sections["plan"]})
	sections = append(sections, struct{ name, key, value string }{"Notes", "notes", body.sections["notes"]})
	sections = append(sections, struct{ name, key, value string }{"Summary", "summary", body.sections["summary"]})
	comments := parseComments(body.comments, fallback)
	if len(comments) > 0 {
		sections = append(sections, struct{ name, key, value string }{"Comments", "comments", renderComments(comments)})
	} else if _, exists := body.sections["comments"]; exists {
		sections = append(sections, struct{ name, key, value string }{"Comments", "comments", ""})
	}
	var out strings.Builder
	if description := strings.Trim(body.sections["description"], "\n"); description != "" {
		out.WriteString("\n")
		out.WriteString(description)
		out.WriteString("\n")
	}
	for _, section := range sections {
		if section.value == "" {
			continue
		}
		out.WriteString("\n## ")
		out.WriteString(section.name)
		out.WriteString("\n\n")
		out.WriteString(strings.Trim(section.value, "\n"))
		out.WriteString("\n")
	}
	if out.Len() == 0 {
		return ""
	}
	return out.String()
}

type importedComment struct {
	time         time.Time
	author       string
	body         string
	dateFallback bool
}

func parseComments(text string, fallback time.Time) []importedComment {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	var chunks []string
	var current []string
	fence := markdownFence{}
	for _, line := range lines {
		if !fence.consume(line) && strings.TrimSpace(line) == "---" {
			chunks = append(chunks, strings.Join(current, "\n"))
			current = nil
			continue
		}
		current = append(current, line)
	}
	chunks = append(chunks, strings.Join(current, "\n"))
	var comments []importedComment
	for i := 0; i < len(chunks); {
		header := chunks[i]
		metadata := parseCommentMetadata(header)
		if metadata.valid && i+1 < len(chunks) {
			body := ""
			if i+1 < len(chunks) {
				body = chunks[i+1]
				i += 2
			} else {
				i++
			}
			when := fallback
			dateFallback := !metadata.hasCreated
			if metadata.hasCreated {
				if parsed, err := parseLegacyTime(metadata.created); err == nil {
					when = parsed
				} else {
					dateFallback = true
				}
			}
			author := strings.TrimSpace(metadata.author)
			if author == "" || strings.ContainsAny(author, "\r\n") {
				author = "unknown"
			}
			if metadata.hasCreated {
				if _, err := parseLegacyTime(metadata.created); err != nil {
					body = joinSection("created: "+fmt.Sprint(metadata.created), body)
				}
			}
			if len(metadata.extra) > 0 {
				body = joinSection(strings.Join(metadata.extra, "\n"), body)
			}
			comments = append(comments, importedComment{time: when, author: author, body: strings.Trim(body, "\n"), dateFallback: dateFallback})
			continue
		}
		if strings.TrimSpace(header) != "" {
			comments = append(comments, importedComment{time: fallback, author: "unknown", body: strings.Trim(header, "\n"), dateFallback: true})
		}
		i++
	}
	return comments
}

type commentMetadata struct {
	author     string
	created    any
	hasCreated bool
	extra      []string
	valid      bool
}

func parseCommentMetadata(text string) commentMetadata {
	var result commentMetadata
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			result.extra = append(result.extra, line)
			continue
		}
		switch strings.TrimSpace(key) {
		case "author":
			result.author = strings.TrimSpace(value)
		case "created":
			result.created = strings.Trim(strings.TrimSpace(value), "\"'")
			result.hasCreated = true
		default:
			result.extra = append(result.extra, line)
		}
	}
	result.valid = result.author != "" || result.hasCreated
	return result
}

func renderComments(comments []importedComment) string {
	var rendered []string
	for _, comment := range comments {
		author := strings.TrimSpace(comment.author)
		if author == "" || strings.ContainsAny(author, "\r\n") {
			author = "unknown"
		}
		when := comment.time.UTC().Truncate(time.Second).Format(time.RFC3339)
		rendered = append(rendered, "### "+when+" "+author+"\n\n"+strings.Trim(comment.body, "\n"))
	}
	return strings.Join(rendered, "\n\n")
}

func rewriteRef(value string, idMap map[string]string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("empty reference")
	}
	source, id, hasSource := strings.Cut(value, ":")
	if !hasSource {
		id = value
	}
	id = strings.ToUpper(id)
	if !validBacklogID(id) {
		return "", fmt.Errorf("invalid task reference %q", value)
	}
	if mapped, ok := idMap[canonicalID(id)]; ok {
		id = mapped
	}
	if hasSource {
		return source + ":" + id, nil
	}
	return id, nil
}

func parseReference(value string) model.Ref {
	source, id, found := strings.Cut(value, ":")
	if found {
		return model.Ref{Source: source, ID: id}
	}
	return model.Ref{ID: value}
}

func taskFileReport(task taskInput) FileReport {
	mappings := make([]FieldMapping, 0, len(task.doc.fields)+8)
	for _, key := range sortedKeys(task.doc.fields) {
		original := task.doc.fields[key]
		output, value := mapReportedField(key, original, task)
		mappings = append(mappings, FieldMapping{Source: key, Output: output, Original: original, Value: value})
	}
	if _, ok := task.doc.fields["created_date"]; !ok && task.doc.fields["created"] == nil {
		mappings = append(mappings, FieldMapping{Source: "created_date", Output: "created", Original: nil, Value: task.created})
	}
	if _, ok := task.doc.fields["updated_date"]; !ok && task.doc.fields["updated"] == nil {
		mappings = append(mappings, FieldMapping{Source: "updated_date", Output: "updated", Original: nil, Value: task.updated})
	}
	if _, ok := task.doc.fields["ordinal"]; !ok {
		mappings = append(mappings, FieldMapping{Source: "ordinal", Output: "rank", Original: nil, Value: task.rank})
	}
	for _, key := range []string{"description", "criteria", "dod", "plan", "notes", "summary", "comments"} {
		if original, ok := task.doc.body.originals[key]; ok {
			output := key
			value := any(task.doc.body.sections[key])
			if key == "criteria" {
				output = "Acceptance criteria"
			}
			if key == "dod" {
				output = "Definition of done"
			}
			if key == "comments" {
				value = renderComments(parseComments(task.doc.body.comments, task.updated))
			}
			mappings = append(mappings, FieldMapping{Source: "body:" + key, Output: output, Original: original, Value: value})
		}
	}
	for _, unknown := range task.doc.body.unknown {
		mappings = append(mappings, FieldMapping{Source: "body:" + unknown.heading, Output: "description", Original: task.doc.body.originals["unknown:"+unknown.heading], Value: "## " + unknown.heading + "\n\n" + unknown.content})
	}
	sort.Slice(mappings, func(i, j int) bool { return mappings[i].Source < mappings[j].Source })
	kind := "task"
	if task.draft {
		kind = "draft"
	}
	if task.archived {
		kind = "archived-" + kind
	}
	return FileReport{SourcePath: task.sourcePath, OutputPath: task.outputPath, Kind: kind, ID: strings.ToUpper(task.id), Mappings: mappings}
}

func mapReportedField(key string, original any, task taskInput) (string, any) {
	switch key {
	case "id":
		return "id", strings.ToUpper(task.id)
	case "title":
		return "title", task.title
	case "status":
		return "status", task.mappedStatus
	case "assignee", "assignees":
		value, _ := mergedLists(task.doc.fields, "assignee", "assignees")
		return "assignees", value
	case "dependencies", "blocked_by":
		value, _ := mergedLists(task.doc.fields, "dependencies", "blocked_by")
		for i := range value {
			value[i], _ = rewriteRef(value[i], task.refMap)
		}
		return "blocked_by", value
	case "parent_task_id", "parent":
		value, _, _ := mergedScalar(task.doc.fields, "parent_task_id", "parent")
		value, _ = rewriteRef(value, task.refMap)
		return "parent", value
	case "ordinal", "rank":
		return "rank", task.rank
	case "references", "documentation", "refs":
		value, _ := mergedLists(task.doc.fields, "references", "documentation", "refs")
		return "refs", value
	case "modified_files", "files":
		value, _ := mergedLists(task.doc.fields, "modified_files", "files")
		return "files", value
	case "related":
		value, _ := optionalList(task.doc.fields, "related")
		for i := range value {
			value[i], _ = rewriteRef(value[i], task.refMap)
		}
		return "related", value
	case "duplicate_of":
		value, _, _ := mergedScalar(task.doc.fields, "duplicate_of")
		value, _ = rewriteRef(value, task.refMap)
		return "duplicate_of", value
	case "created_date", "created":
		return "created", task.created
	case "updated_date", "updated":
		return "updated", task.updated
	case "milestone":
		return "milestone", task.milestone
	case "type":
		return "type", task.typeName
	case "due":
		return "due", task.due
	default:
		if key == "priority" {
			return key, strings.ToLower(stringValue(original))
		}
		return key, original
	}
}

func milestoneFileReport(milestone milestoneInput, output project.Milestone) FileReport {
	mappings := []FieldMapping{
		{Source: "id", Output: "milestones[].key", Original: milestone.id, Value: output.Key},
		{Source: "title", Output: "milestones[].name", Original: milestone.name, Value: output.Name},
		{Source: "body:Description", Output: "milestones[].description", Original: milestone.doc.body.originals["description"], Value: output.Description},
		{Source: "archive", Output: "milestones[].archived", Original: milestone.archived, Value: output.Archived},
	}
	for _, key := range sortedKeys(milestone.doc.fields) {
		if key == "id" || key == "title" {
			continue
		}
		mappings = append(mappings, FieldMapping{Source: key, Output: "", Original: milestone.doc.fields[key], Value: nil})
	}
	for _, unknown := range milestone.doc.body.unknown {
		mappings = append(mappings, FieldMapping{Source: "body:" + unknown.heading, Output: "milestones[].description", Original: milestone.doc.body.originals["unknown:"+unknown.heading], Value: "## " + unknown.heading + "\n\n" + unknown.content})
	}
	return FileReport{SourcePath: milestone.sourcePath, OutputPath: filepath.ToSlash(filepath.Join(".config", "backlog.yaml")), Kind: "milestone", ID: milestone.id, Mappings: mappings}
}

func makeMilestones(inputs []milestoneInput, report *Report) ([]project.Milestone, map[string]string, map[string]string, error) {
	results := make([]project.Milestone, 0, len(inputs))
	byKey := map[string]string{}
	titles := map[string][]string{}
	seen := map[string]string{}
	for _, input := range inputs {
		key := strings.TrimSpace(input.id)
		if key == "" || strings.ContainsAny(key, "\r\n") {
			return nil, nil, nil, fmt.Errorf("invalid milestone ID in %s", input.sourcePath)
		}
		identity := strings.ToLower(key)
		if previous, exists := seen[identity]; exists {
			return nil, nil, nil, fmt.Errorf("milestone key collision: %s and %s", previous, input.sourcePath)
		}
		seen[identity] = input.sourcePath
		entry := project.Milestone{Key: key, Name: input.name, Description: input.description, Archived: input.archived}
		results = append(results, entry)
		byKey[identity] = key
		title := strings.ToLower(strings.TrimSpace(input.name))
		titles[title] = append(titles[title], key)
		for _, field := range sortedKeys(input.doc.fields) {
			if field != "id" && field != "title" {
				report.Skipped = append(report.Skipped, SkippedContent{Path: input.sourcePath, Field: field, Reason: "milestone metadata has no native milestone field", Content: input.doc.fields[field]})
			}
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Key < results[j].Key })
	byTitle := map[string]string{}
	titleNames := make([]string, 0, len(titles))
	for title := range titles {
		titleNames = append(titleNames, title)
	}
	sort.Strings(titleNames)
	for _, title := range titleNames {
		keys := titles[title]
		if title != "" && len(keys) == 1 {
			byTitle[title] = keys[0]
		}
	}
	for _, title := range titleNames {
		keys := titles[title]
		if len(keys) > 1 {
			report.Decisions = append(report.Decisions, fmt.Sprintf("Milestone title %q is ambiguous across keys %s; task values using that title are left unresolved.", title, strings.Join(keys, ", ")))
		}
	}
	return results, byKey, byTitle, nil
}

func configMappings(source sourceConfig, statuses []project.StatusConfig, milestones []project.Milestone, ready project.ReadyConfig, prefix string, settings project.Settings) []FieldMapping {
	fields := []FieldMapping{
		{Source: "project_name", Output: "project.name", Original: source.config["project_name"], Value: settings.Project.Name},
		{Source: "statuses", Output: "statuses", Original: source.config["statuses"], Value: statuses},
		{Source: "labels", Output: "labels", Original: source.config["labels"], Value: settings.Labels},
		{Source: "definition_of_done", Output: "defaults.dod", Original: source.config["definition_of_done"], Value: settings.Defaults.DOD},
		{Source: "ready labels", Output: "ready.excludeLabels", Original: source.config["labels"], Value: ready.ExcludeLabels},
		{Source: "milestones", Output: "milestones", Original: nil, Value: milestones},
		{Source: "task_prefix", Output: "sources[0].prefix", Original: source.config["task_prefix"], Value: prefix},
		{Source: "default_status", Output: "", Original: source.config["default_status"], Value: nil},
		{Source: "backlog_directory", Output: "", Original: source.config["backlog_directory"], Value: nil},
	}
	return fields
}

func knownConfigKey(key string) bool {
	switch key {
	case "project_name", "default_status", "statuses", "labels", "definition_of_done", "backlog_directory", "task_prefix":
		return true
	default:
		return false
	}
}

func writePlan(ctx context.Context, plan outputPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := validateTarget(plan.target); err != nil {
		return err
	}
	parent := filepath.Dir(plan.target)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("create target parent: %w", err)
	}
	stage, err := os.MkdirTemp(parent, ".bl-import-*")
	if err != nil {
		return fmt.Errorf("stage import: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0755); err != nil {
		return err
	}
	for _, relative := range plan.paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(stage, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return fmt.Errorf("create staged directory: %w", err)
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return fmt.Errorf("create staged file %s: %w", relative, err)
		}
		_, writeErr := file.Write(plan.files[relative])
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil {
			return fmt.Errorf("write staged file %s: %w", relative, writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close staged file %s: %w", relative, closeErr)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !plan.existed {
		if _, err := os.Lstat(plan.target); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return fmt.Errorf("refusing target created during import: %s", plan.target)
			}
			return err
		}
		if err := os.Rename(stage, plan.target); err != nil {
			return fmt.Errorf("publish imported project: %w", err)
		}
		return nil
	}
	if _, err := validateTarget(plan.target); err != nil {
		return err
	}
	backup, err := os.MkdirTemp(parent, ".bl-import-empty-*")
	if err != nil {
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}
	if err := os.Rename(plan.target, backup); err != nil {
		return fmt.Errorf("reserve empty target: %w", err)
	}
	entries, err := os.ReadDir(backup)
	if err != nil || len(entries) != 0 {
		_ = os.Rename(backup, plan.target)
		if err != nil {
			return err
		}
		return fmt.Errorf("refusing non-empty target directory: %s", plan.target)
	}
	if err := os.Rename(stage, plan.target); err != nil {
		_ = os.Rename(backup, plan.target)
		return fmt.Errorf("publish imported project: %w", err)
	}
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("remove replaced empty target: %w", err)
	}
	return nil
}

func requiredString(fields map[string]any, key string) (string, error) {
	value, ok := fields[key]
	if !ok {
		return "", fmt.Errorf("missing required %s", key)
	}
	text, ok := scalarString(value)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s must be a non-empty string", key)
	}
	return text, nil
}

func scalarString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case time.Time:
		return typed.Format(time.RFC3339), true
	default:
		return "", false
	}
}

func stringValue(value any) string {
	text, _ := scalarString(value)
	return text
}

func optionalStringField(fields map[string]any, key string) (string, error) {
	value, exists := fields[key]
	if !exists || value == nil {
		return "", nil
	}
	text, ok := scalarString(value)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return text, nil
}

func optionalDateField(fields map[string]any, key string) (string, error) {
	value, exists := fields[key]
	if !exists || value == nil {
		return "", nil
	}
	if date, ok := value.(time.Time); ok {
		return date.Format("2006-01-02"), nil
	}
	return optionalStringField(fields, key)
}

func readDateField(fields map[string]any, preferred, alternate string) (time.Time, bool, error) {
	var preferredTime, alternateTime time.Time
	var hasPreferred, hasAlternate bool
	if value, ok := fields[preferred]; ok && value != nil {
		parsed, err := parseLegacyTime(value)
		if err != nil {
			return time.Time{}, false, err
		}
		preferredTime, hasPreferred = parsed, true
	}
	if value, ok := fields[alternate]; ok && value != nil {
		parsed, err := parseLegacyTime(value)
		if err != nil {
			return time.Time{}, false, err
		}
		alternateTime, hasAlternate = parsed, true
	}
	if hasPreferred && hasAlternate && !preferredTime.Equal(alternateTime) {
		return time.Time{}, false, fmt.Errorf("conflicting %s and %s values", preferred, alternate)
	}
	if hasPreferred {
		return preferredTime, true, nil
	}
	if hasAlternate {
		return alternateTime, true, nil
	}
	return time.Time{}, false, nil
}

func integerValue(value any) (*big.Int, error) {
	text := fmt.Sprint(value)
	if strings.ContainsAny(text, ".eE") {
		return nil, fmt.Errorf("must be an integer, got %q", text)
	}
	number, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return nil, fmt.Errorf("must be an integer, got %q", text)
	}
	return number, nil
}

func stringList(value any, field string, allowScalar bool) ([]string, error) {
	if value == nil {
		return []string{}, nil
	}
	if values, ok := value.([]any); ok {
		result := make([]string, 0, len(values))
		for _, entry := range values {
			text, ok := scalarString(entry)
			if !ok {
				return nil, fmt.Errorf("%s entries must be strings", field)
			}
			result = append(result, text)
		}
		return result, nil
	}
	if values, ok := value.([]string); ok {
		return append([]string(nil), values...), nil
	}
	if allowScalar {
		if text, ok := scalarString(value); ok {
			return []string{text}, nil
		}
	}
	return nil, fmt.Errorf("%s must be a list of strings", field)
}

func optionalList(fields map[string]any, key string) ([]string, error) {
	value, ok := fields[key]
	if !ok || value == nil {
		return []string{}, nil
	}
	return stringList(value, key, true)
}

func mergedLists(fields map[string]any, keys ...string) ([]string, error) {
	var result []string
	for _, key := range keys {
		values, err := optionalList(fields, key)
		if err != nil {
			return nil, err
		}
		result = append(result, values...)
	}
	return result, nil
}

func mergedScalar(fields map[string]any, keys ...string) (string, bool, error) {
	var result string
	found := false
	for _, key := range keys {
		value, exists := fields[key]
		if !exists || value == nil {
			continue
		}
		text, ok := scalarString(value)
		if !ok {
			return "", false, fmt.Errorf("%s must be a string", key)
		}
		if found && text != result {
			return "", false, fmt.Errorf("conflicting values for %s", strings.Join(keys, " and "))
		}
		result, found = text, true
	}
	return result, found, nil
}

func unknownFields(fields map[string]any) map[string]any {
	known := map[string]bool{
		"id": true, "title": true, "status": true, "type": true, "priority": true,
		"assignee": true, "assignees": true, "labels": true, "milestone": true,
		"due": true, "created_date": true, "created": true, "updated_date": true,
		"updated": true, "dependencies": true, "blocked_by": true, "parent_task_id": true,
		"parent": true, "ordinal": true, "rank": true, "references": true,
		"documentation": true, "refs": true, "modified_files": true, "files": true,
		"related": true, "duplicate_of": true,
	}
	result := map[string]any{}
	for key, value := range fields {
		if !known[key] {
			result[key] = value
		}
	}
	return result
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func hasBacklogPartition(path string) bool {
	for _, name := range []string{"tasks", "completed", "drafts", "milestones", "archive"} {
		if isDir(filepath.Join(path, name)) {
			return true
		}
	}
	return false
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
