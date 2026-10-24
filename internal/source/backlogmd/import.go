// Package backlogmd reads and writes Backlog.md files and imports them into the native format.
package backlogmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
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
	Renumbered           []RenumberedID        `json:"renumbered"`
	UnresolvedReferences []UnresolvedReference `json:"unresolvedReferences"`
	InPlace              bool                  `json:"inPlace"`
	Removed              []string              `json:"removed"`
}

// ImportOptions selects the destination. InPlace writes the native project into the
// source's project root, then removes the imported Backlog.md files.
type ImportOptions struct {
	Target  string
	DryRun  bool
	InPlace bool
}

type RenumberedID struct {
	SourcePath string `json:"sourcePath"`
	From       string `json:"from"`
	To         string `json:"to"`
}

// UnresolvedReference is body text that may name a moved item or file but was
// left as written.
type UnresolvedReference struct {
	SourcePath string `json:"sourcePath"`
	Value      string `json:"value"`
	Reason     string `json:"reason"`
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
	projectRoot string
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
	doc           sourceDocument
	sourcePath    string
	id            string
	title         string
	status        string
	typeName      string
	due           string
	created       time.Time
	updated       time.Time
	createdFrom   string
	file          string
	path          string
	ordinal       *big.Int
	rank          string
	archived      bool
	draft         bool
	outputPath    string
	outputBytes   []byte
	milestone     string
	mappedStatus  string
	refMap        map[string]string
	nativeID      string
	legacyID      string
	refs          []string
	dropped       map[string]bool
	priority      model.Priority
	priorityAlias string
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
// writing when DryRun is true. Target must be absent or an empty directory;
// InPlace instead writes into the source's project root and removes the
// imported source files, which must be committed in Git.
func Import(ctx context.Context, input string, options ImportOptions) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if strings.TrimSpace(input) == "" {
		return Report{}, fmt.Errorf("input directory is required")
	}
	if options.InPlace == (strings.TrimSpace(options.Target) != "") {
		return Report{}, fmt.Errorf("choose a target directory or an in-place import")
	}
	inputAbs, err := filepath.Abs(input)
	if err != nil {
		return Report{}, fmt.Errorf("resolve input: %w", err)
	}
	source, err := locateSource(inputAbs)
	if err != nil {
		return Report{}, err
	}
	items := "backlog"
	if options.InPlace {
		// Discovery reads backlog.yaml only at the checkout root, so the
		// native source keeps the Backlog.md folder's path from there.
		if top, ok := checkoutRoot(source.root); ok {
			source.projectRoot = top
			items, _ = filepath.Rel(top, source.root)
			items = filepath.ToSlash(items)
		}
	}
	targetAbs, existed := source.projectRoot, false
	if !options.InPlace {
		targetAbs, err = filepath.Abs(options.Target)
		if err != nil {
			return Report{}, fmt.Errorf("resolve target: %w", err)
		}
		existed, err = validateTarget(targetAbs)
		if err != nil {
			return Report{}, err
		}
	}
	report := Report{
		Input: inputAbs, Target: targetAbs, DryRun: options.DryRun, InPlace: options.InPlace,
		OutputPaths: []string{}, Files: []FileReport{}, Configuration: []FieldMapping{},
		Decisions: []string{}, Renumbered: []RenumberedID{}, UnresolvedReferences: []UnresolvedReference{}, Removed: []string{},
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := collectSkipped(source.root, source.configPath, &report); err != nil {
		return report, err
	}
	taskFiles, err := scanPartitions(ctx, source.root, &report, true)
	if err != nil {
		return report, err
	}
	milestoneFiles, err := scanMilestones(ctx, source.root, &report)
	if err != nil {
		return report, err
	}
	links := 0
	for _, candidate := range append(append([]sourceCandidate(nil), taskFiles...), milestoneFiles...) {
		if candidate.path != candidate.absolute {
			links++
		}
	}
	if links > 0 {
		report.Decisions = append(report.Decisions, fmt.Sprintf("Followed %d symbolic link(s) to the Markdown files they name; the targets are left in place.", links))
	}
	tasks := make([]taskInput, 0, len(taskFiles))
	indexed := 0
	for _, candidate := range taskFiles {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		task, err := readTask(candidate)
		if err != nil {
			return report, fmt.Errorf("%s: %w", candidate.relative, err)
		}
		if task.createdFrom != "" {
			// Git history is only consulted here: the live adapter reads on every list.
			if first, ok := gitCreated(ctx, task.file); ok && (task.createdFrom == createdFromModTime || !first.After(task.updated)) {
				if task.createdFrom == createdFromModTime {
					task.updated = first
				}
				task.created, task.createdFrom = first, "its first Git commit"
			}
			report.Decisions = append(report.Decisions, fmt.Sprintf("%s has no created timestamp; used %s.", task.sourcePath, task.createdFrom))
		}
		for _, key := range []string{"criteria", "dod"} {
			original := task.doc.body.originals[key]
			if stripBodyMarkers(original, true) != stripBodyMarkers(original, false) {
				indexed++
				break
			}
		}
		tasks = append(tasks, task)
	}
	if indexed > 0 {
		report.Decisions = append(report.Decisions, fmt.Sprintf("Removed one leading #N index per criterion in %d file(s); text, order, and checked state are kept.", indexed))
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
	prefix, err := nativePrefix(source, tasks)
	if err != nil {
		return report, err
	}
	refs, err := assignIDs(tasks, prefix, items, &report)
	if err != nil {
		return report, err
	}
	milestoneConfig, milestoneByKey, milestoneByTitle, err := makeMilestones(milestones, &report)
	if err != nil {
		return report, err
	}
	if _, configured := source.config["task_prefix"]; !configured {
		report.Decisions = append(report.Decisions, fmt.Sprintf("No task_prefix was configured; inferred native ID prefix %s.", prefix))
	}
	assignRanks(tasks)
	statuses, statusLookup, workflow, ready := makeStatuses(source, tasks)
	noteUnconfiguredStatuses(source, tasks, statusLookup, &report)
	categories := map[string]string{}
	for _, status := range statuses {
		categories[status.Key] = status.Category
	}
	closed := func(task *taskInput) bool {
		category := model.StatusCategory(categories[taskStatusKey(task.status, statusLookup)])
		return task.archived || category == model.Done || category == model.Canceled
	}
	if err := breakClosedCycles(tasks, refs, closed, &report); err != nil {
		return report, err
	}
	rewriter := newReferenceRewriter(source, tasks, refs, &report)
	for i := range tasks {
		if err := rewriter.rewrite(&tasks[i]); err != nil {
			return report, fmt.Errorf("%s: %w", tasks[i].sourcePath, err)
		}
	}

	plan := outputPlan{files: map[string][]byte{}, target: targetAbs, existed: existed}
	aliases := map[string]int{}
	for i := range tasks {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := mapTask(&tasks[i], refs, milestoneByKey, milestoneByTitle, statusLookup, &report); err != nil {
			return report, fmt.Errorf("%s: %w", tasks[i].sourcePath, err)
		}
		if tasks[i].priorityAlias != "" {
			aliases[tasks[i].priorityAlias+" to "+string(tasks[i].priority)]++
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
	for _, alias := range slices.Sorted(maps.Keys(aliases)) {
		report.Decisions = append(report.Decisions, fmt.Sprintf("Mapped priority %s in %d item(s).", alias, aliases[alias]))
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
		Sources:    []project.SourceConfig{{ID: "tasks", Type: "native", Path: items, Prefix: prefix}},
		Statuses:   statuses,
		Workflow:   workflow,
		Milestones: milestoneConfig,
		Labels:     append([]string(nil), source.labels...),
		Ready:      ready,
		Defaults:   project.DefaultsConfig{DOD: append([]string(nil), source.dod...)},
	}
	configBytes, err := format.MarshalConfig(settings)
	if err != nil {
		return report, fmt.Errorf("encode native project config: %w", err)
	}
	configPath := "backlog.yaml"
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
	if options.InPlace {
		cutover, err := planCutover(ctx, source, plan, append(taskFiles, milestoneFiles...))
		if err != nil {
			return report, err
		}
		report.Removed = cutover.removedPaths()
		if !options.DryRun {
			return report, cutover.apply(ctx)
		}
		return report, nil
	}
	if !options.DryRun {
		if err := writePlan(ctx, plan); err != nil {
			return report, err
		}
	}
	return report, nil
}

// absolute is the file to read; path is its entry in the source tree, which
// differs only for a followed symbolic link.
type sourceCandidate struct {
	absolute  string
	path      string
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
		source.projectRoot = input
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
				source.projectRoot = projectRoot
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
		source.projectRoot = filepath.Dir(input)
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
		source.projectRoot = input
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

// scanPartitions lists task files. With followLinks, a symbolic link to a
// Markdown file is read through; any other link is refused. Without it, links
// are skipped and reported.
func scanPartitions(ctx context.Context, root string, report *Report, followLinks bool) ([]sourceCandidate, error) {
	definitions := []sourceCandidate{
		{partition: "tasks"},
		{partition: "completed", completed: true},
		{partition: filepath.Join("archive", "tasks"), archived: true},
		{partition: filepath.Join("archive", "completed"), archived: true, completed: true},
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
			file := path
			if entry.Type()&os.ModeSymlink != 0 {
				if !followLinks {
					report.Skipped = append(report.Skipped, SkippedContent{Path: filepath.ToSlash(relative), Reason: "symbolic links are not imported"})
					return nil
				}
				if file, err = followLink(path); err != nil {
					return fmt.Errorf("%s: %w", filepath.ToSlash(relative), err)
				}
			} else if entry.IsDir() {
				return nil
			} else if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				report.Skipped = append(report.Skipped, SkippedContent{Path: filepath.ToSlash(relative), Reason: "only regular Markdown task files are imported"})
				return nil
			}
			result = append(result, sourceCandidate{absolute: file, path: path, relative: filepath.ToSlash(relative), partition: definition.partition, archived: definition.archived, draft: definition.draft, completed: definition.completed})
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
			file := path
			if entry.Type()&os.ModeSymlink != 0 {
				if file, err = followLink(path); err != nil {
					return fmt.Errorf("%s: %w", filepath.ToSlash(relative), err)
				}
			} else if entry.IsDir() {
				return nil
			} else if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				report.Skipped = append(report.Skipped, SkippedContent{Path: filepath.ToSlash(relative), Reason: "only regular Markdown milestone files are imported"})
				return nil
			}
			result = append(result, sourceCandidate{absolute: file, path: path, relative: filepath.ToSlash(relative), partition: definition.partition, archived: definition.archived})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", definition.partition, err)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].relative < result[j].relative })
	return result, nil
}

// followLink resolves a symbolic link to the regular Markdown file it names.
// Anything else is refused rather than skipped, so no item goes missing.
func followLink(path string) (string, error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("broken symbolic link (%w); restore its target or remove it", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(target), ".md") {
		return "", fmt.Errorf("symbolic link to %s, which is not a Markdown file; replace the link with the file or remove it", target)
	}
	return target, nil
}

// checkoutRoot returns the nearest directory at or above dir with a .git
// entry, which a linked worktree has as a file.
func checkoutRoot(dir string) (string, bool) {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// gitCreated returns the author time of the commit that added path, following
// renames. It reports false outside Git or for an uncommitted file.
func gitCreated(ctx context.Context, path string) (time.Time, bool) {
	output, err := exec.CommandContext(ctx, "git", "-C", filepath.Dir(path), "log", "--follow", "--diff-filter=A", "--format=%aI", "--", filepath.Base(path)).Output()
	if err != nil {
		return time.Time{}, false
	}
	lines := strings.Fields(string(output))
	if len(lines) == 0 {
		return time.Time{}, false
	}
	created, err := time.Parse(time.RFC3339, lines[len(lines)-1])
	if err != nil {
		return time.Time{}, false
	}
	return created.UTC(), true
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
			if entry.Name() == "tasks" || entry.Name() == "completed" || entry.Name() == "drafts" || entry.Name() == "milestones" {
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

const createdFromModTime = "the file modification time"

func readTask(candidate sourceCandidate) (taskInput, error) {
	data, err := os.ReadFile(candidate.absolute)
	if err != nil {
		return taskInput{}, err
	}
	return readTaskBytes(candidate, data)
}

func readTaskBytes(candidate sourceCandidate, data []byte) (taskInput, error) {
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
	due, err := optionalDateField(doc.fields, "due_date")
	if err != nil {
		return taskInput{}, err
	}
	if due == "" {
		due, err = optionalDateField(doc.fields, "due")
		if err != nil {
			return taskInput{}, err
		}
	}
	milestone, err := optionalStringField(doc.fields, "milestone")
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
	createdFrom := ""
	if !createdOK {
		if updatedOK {
			created, createdFrom = updated, "the updated timestamp"
		} else {
			createdFrom = createdFromModTime
			stat, err := os.Stat(candidate.absolute)
			if err != nil {
				return taskInput{}, err
			}
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
		createdFrom: createdFrom, file: candidate.absolute, path: candidate.path,
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

// assignIDs gives each task its native ID and output path, and returns the
// reference lookup from each source ID key to its native ID. Drafts outside
// the native prefix get the next IDs after its highest one, as Backlog.md's
// draft promote would, because a native ID never changes when a draft is
// promoted. IDs that aren't PREFIX-N, such as slugs, are renumbered the same
// way. Renumbered items keep the source ID as legacy_id, so archived drafts
// that reused an ID each get their own. A reference to a reused ID resolves to
// the only unarchived holder; with none or several, it is ambiguous ("").
func assignIDs(tasks []taskInput, prefix, items string, report *Report) (map[string]string, error) {
	kept := map[string]string{}
	holders := map[string][]int{}
	highest := new(big.Int)
	var renumber []int
	for i, task := range tasks {
		upper := strings.ToUpper(strings.TrimSpace(task.id))
		key := refKey(upper)
		holders[key] = append(holders[key], i)
		taskPrefix, number, _ := strings.Cut(key, "-")
		if !validBacklogID(upper) || task.draft && taskPrefix != prefix {
			renumber = append(renumber, i)
			continue
		}
		if previous, exists := kept[key]; exists {
			return nil, fmt.Errorf("task ID collision after native lookup normalization: %s and %s", previous, task.sourcePath)
		}
		kept[key] = task.sourcePath
		tasks[i].nativeID = upper
		if taskPrefix != prefix {
			continue
		}
		first, _, _ := strings.Cut(number, ".")
		if n, _ := new(big.Int).SetString(first, 10); n.Cmp(highest) > 0 {
			highest = n
		}
	}
	sort.SliceStable(renumber, func(i, j int) bool {
		a, b := strings.ToUpper(tasks[renumber[i]].id), strings.ToUpper(tasks[renumber[j]].id)
		if validBacklogID(a) != validBacklogID(b) {
			return validBacklogID(a)
		}
		if validBacklogID(a) {
			return idLess(canonicalID(a), canonicalID(b))
		}
		return a < b
	})
	for _, i := range renumber {
		highest = new(big.Int).Add(highest, big.NewInt(1))
		tasks[i].nativeID = prefix + "-" + highest.String()
		tasks[i].legacyID = tasks[i].id
		report.Renumbered = append(report.Renumbered, RenumberedID{SourcePath: tasks[i].sourcePath, From: tasks[i].id, To: tasks[i].nativeID})
	}
	if len(renumber) > 0 {
		report.Decisions = append(report.Decisions, fmt.Sprintf("Renumbered %d item(s) whose IDs were drafts outside %s, not PREFIX-N, or reused; each keeps its old ID as legacy_id.", len(renumber), prefix))
	}
	refs := make(map[string]string, len(holders))
	for key, indexes := range holders {
		var live []int
		for _, i := range indexes {
			if !tasks[i].archived {
				live = append(live, i)
			}
		}
		switch {
		case len(indexes) == 1:
			refs[key] = tasks[indexes[0]].nativeID
		case len(live) == 1:
			refs[key] = tasks[live[0]].nativeID
		default:
			refs[key] = ""
		}
	}
	for i := range tasks {
		tasks[i].outputPath = path.Join(items, tasks[i].nativeID+".md")
		if tasks[i].archived {
			tasks[i].outputPath = path.Join(items, "archive", tasks[i].nativeID+".md")
		}
	}
	return refs, nil
}

// refKey is the lookup key for a source ID: canonical when it is PREFIX-N,
// otherwise upper-cased.
func refKey(id string) string {
	upper := strings.ToUpper(strings.TrimSpace(id))
	if validBacklogID(upper) {
		return canonicalID(upper)
	}
	return upper
}

// idLess orders valid canonical IDs by prefix, then numerically by component.
func idLess(a, b string) bool {
	aPrefix, aNumber, _ := strings.Cut(a, "-")
	bPrefix, bNumber, _ := strings.Cut(b, "-")
	if aPrefix != bPrefix {
		return aPrefix < bPrefix
	}
	aParts, bParts := strings.Split(aNumber, "."), strings.Split(bNumber, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		x, _ := new(big.Int).SetString(aParts[i], 10)
		y, _ := new(big.Int).SetString(bParts[i], 10)
		if c := x.Cmp(y); c != 0 {
			return c < 0
		}
	}
	return len(aParts) < len(bParts)
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
			if !task.archived && !task.draft && validBacklogID(strings.ToUpper(task.id)) {
				prefix, _, _ = strings.Cut(task.id, "-")
				break
			}
		}
	}
	if prefix == "" {
		for _, task := range tasks {
			if !task.draft && validBacklogID(strings.ToUpper(task.id)) {
				prefix, _, _ = strings.Cut(task.id, "-")
				break
			}
		}
	}
	// Drafts never set the prefix: they get native IDs in it (makeIDMap).
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
	// Every Backlog.md source has drafts/, so the draft status always exists.
	add("Draft")
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
	for _, status := range statuses {
		switch model.StatusCategory(status.Category) {
		case model.Active:
			if start == "" {
				start = status.Key
			}
		case model.Done:
			if done == "" {
				done = status.Key
			}
		}
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
	// Readiness matches status categories, not keys.
	return statuses, lookup, project.WorkflowConfig{Start: start, Done: done}, project.ReadyConfig{
		Statuses: []string{string(model.Todo)}, SatisfiedBy: []string{string(model.Done), string(model.Canceled)}, ExcludeLabels: excludeLabels, SkipContainers: true,
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
	id := task.nativeID
	statusKey := taskStatusKey(task.status, statusLookup)
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
	kept := dependencies[:0]
	for _, dependency := range dependencies {
		value, err := rewriteRef(dependency, idMap)
		if err != nil {
			return fmt.Errorf("dependencies: %w", err)
		}
		if !task.dropped[value] {
			kept = append(kept, value)
		}
	}
	dependencies = kept
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
	priority := model.Priority(strings.ToLower(strings.TrimSpace(priorityValue)))
	if alias, ok := priorityAliases[string(priority)]; ok {
		task.priorityAlias, priority = string(priority), alias
	}
	if priority != "" && priority != model.Urgent && priority != model.High && priority != model.Medium && priority != model.Low {
		return fmt.Errorf("unsupported priority %q; use urgent, high, medium, or low", priorityValue)
	}
	task.priority = priority
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
		Related: make([]model.Ref, 0, len(related)), Refs: task.refs, Files: files, Description: task.doc.body.sections["description"],
		Created: created, Updated: updated, Fields: unknownFields(task.doc.fields),
	}
	if _, exists := item.Fields["legacy_id"]; task.legacyID != "" && !exists {
		item.Fields["legacy_id"] = task.legacyID
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
	doc, err := format.Parse(output)
	if err != nil {
		return fmt.Errorf("native item validation: %w", err)
	}
	if err := verifyBody(doc.Item(), task.doc.body, task.updated); err != nil {
		return fmt.Errorf("native item validation: %w", err)
	}
	task.outputBytes = output
	task.milestone = milestone
	return nil
}

// priorityAliases maps common priority scales onto the native one.
var priorityAliases = map[string]model.Priority{
	"p0": model.Urgent, "critical": model.Urgent,
	"p1": model.High,
	"p2": model.Medium, "normal": model.Medium,
	"p3": model.Low, "later": model.Low,
}

func taskStatusKey(status string, lookup map[string]string) string {
	if key, ok := lookup[statusIdentity(status)]; ok {
		return key
	}
	return statusKey(status)
}

// noteUnconfiguredStatuses reports task statuses the source config doesn't
// list, which Backlog.md tolerates but which may be typos.
func noteUnconfiguredStatuses(source sourceConfig, tasks []taskInput, lookup map[string]string, report *Report) {
	if len(source.statuses) == 0 {
		return
	}
	configured := map[string]bool{"draft": true}
	for _, status := range source.statuses {
		configured[statusIdentity(status)] = true
	}
	counts := map[string]int{}
	for _, task := range tasks {
		if !configured[statusIdentity(task.status)] {
			counts[task.status]++
		}
	}
	for _, status := range slices.Sorted(maps.Keys(counts)) {
		report.Decisions = append(report.Decisions, fmt.Sprintf("%d item(s) have status %q, which the source config doesn't list; imported as %s.", counts[status], status, taskStatusKey(status, lookup)))
	}
}

// breakClosedCycles drops dependency cycles that only closed items take part
// in, which bl check would otherwise reject: within such a cycle, each edge to
// an equal or higher ID goes, leaving only edges to lower IDs. A cycle through
// an open item is unfinished work, so it is refused.
func breakClosedCycles(tasks []taskInput, refs map[string]string, closed func(*taskInput) bool, report *Report) error {
	index := make(map[string]int, len(tasks))
	for i := range tasks {
		index[tasks[i].nativeID] = i
	}
	graph := make([][]int, len(tasks))
	for i := range tasks {
		dependencies, err := mergedLists(tasks[i].doc.fields, "dependencies", "blocked_by")
		if err != nil {
			return fmt.Errorf("%s: %w", tasks[i].sourcePath, err)
		}
		for _, dependency := range dependencies {
			value, err := rewriteRef(dependency, refs)
			if err != nil {
				return fmt.Errorf("%s: dependencies: %w", tasks[i].sourcePath, err)
			}
			if j, ok := index[value]; ok {
				graph[i] = append(graph[i], j)
			}
		}
	}
	for _, component := range stronglyConnected(graph) {
		if len(component) == 1 && !slices.Contains(graph[component[0]], component[0]) {
			continue
		}
		members := map[int]bool{}
		ids := make([]string, 0, len(component))
		open := false
		for _, i := range component {
			members[i] = true
			ids = append(ids, tasks[i].nativeID)
			open = open || !closed(&tasks[i])
		}
		sort.Slice(ids, func(i, j int) bool { return idLess(ids[i], ids[j]) })
		if open {
			return fmt.Errorf("dependency cycle among %s includes open items; remove one of these dependencies in the source and import again", strings.Join(ids, ", "))
		}
		var dropped []string
		for _, i := range component {
			for _, j := range graph[i] {
				blocker := tasks[j].nativeID
				if !members[j] || idLess(blocker, tasks[i].nativeID) || tasks[i].dropped[blocker] {
					continue
				}
				if tasks[i].dropped == nil {
					tasks[i].dropped = map[string]bool{}
				}
				tasks[i].dropped[blocker] = true
				dropped = append(dropped, tasks[i].nativeID+" → "+blocker)
			}
		}
		sort.Strings(dropped)
		report.Decisions = append(report.Decisions, fmt.Sprintf("Broke a dependency cycle among closed items %s by dropping %s.", strings.Join(ids, ", "), strings.Join(dropped, ", ")))
	}
	return nil
}

// stronglyConnected returns the strongly connected components of graph
// (Tarjan's algorithm).
func stronglyConnected(graph [][]int) [][]int {
	index, low := make([]int, len(graph)), make([]int, len(graph))
	onStack := make([]bool, len(graph))
	var stack []int
	var components [][]int
	next := 1
	var visit func(int)
	visit = func(v int) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range graph[v] {
			if index[w] == 0 {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		var component []int
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			component = append(component, w)
			if w == v {
				break
			}
		}
		components = append(components, component)
	}
	for v := range graph {
		if index[v] == 0 {
			visit(v)
		}
	}
	return components
}

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

// verifyBody refuses source text that would change native structure, such as
// a comment line shaped like a native comment header or an unclosed fence that
// would swallow later comments. Silently splitting or merging content would
// misattribute it.
func verifyBody(parsed model.Item, body parsedBody, fallback time.Time) error {
	for _, section := range []struct{ name, got, want string }{
		{"description", parsed.Description, body.sections["description"]},
		{"plan", parsed.Plan, body.sections["plan"]},
		{"notes", parsed.Notes, body.sections["notes"]},
		{"summary", parsed.Summary, body.sections["summary"]},
	} {
		if strings.TrimSpace(section.got) != strings.TrimSpace(section.want) {
			return fmt.Errorf("%s section does not keep its content in the native format", section.name)
		}
	}
	want := parseComments(body.comments, fallback)
	if len(parsed.Comments) != len(want) {
		return fmt.Errorf("comments would become %d native comments instead of %d; a comment line looks like a native comment header (### <RFC 3339 UTC time> <author>) or opens an unclosed code fence", len(parsed.Comments), len(want))
	}
	for i, comment := range want {
		if strings.TrimSpace(parsed.Comments[i].Body) != strings.TrimSpace(comment.body) {
			return fmt.Errorf("comment %d does not keep its content in the native format", i+1)
		}
	}
	return nil
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
	mapped, ok := idMap[refKey(id)]
	switch {
	case ok && mapped == "":
		return "", fmt.Errorf("ambiguous reference %q: several items have that ID; make their IDs unique in the source and import again", value)
	case ok:
		id = mapped
	case !validBacklogID(strings.ToUpper(id)):
		return "", fmt.Errorf("invalid task reference %q", value)
	default:
		id = strings.ToUpper(id)
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
	return FileReport{SourcePath: task.sourcePath, OutputPath: task.outputPath, Kind: kind, ID: task.nativeID, Mappings: mappings}
}

func mapReportedField(key string, original any, task taskInput) (string, any) {
	switch key {
	case "id":
		return "id", task.nativeID
	case "title":
		return "title", task.title
	case "status":
		return "status", task.mappedStatus
	case "assignee", "assignees":
		value, _ := mergedLists(task.doc.fields, "assignee", "assignees")
		return "assignees", value
	case "dependencies", "blocked_by":
		value, _ := mergedLists(task.doc.fields, "dependencies", "blocked_by")
		kept := value[:0]
		for i := range value {
			if value[i], _ = rewriteRef(value[i], task.refMap); !task.dropped[value[i]] {
				kept = append(kept, value[i])
			}
		}
		return "blocked_by", kept
	case "parent_task_id", "parent":
		value, _, _ := mergedScalar(task.doc.fields, "parent_task_id", "parent")
		value, _ = rewriteRef(value, task.refMap)
		return "parent", value
	case "ordinal", "rank":
		return "rank", task.rank
	case "references", "documentation", "refs":
		return "refs", task.refs
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
	case "due_date", "due":
		return "due", task.due
	default:
		if key == "priority" {
			return key, string(task.priority)
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
	return FileReport{SourcePath: milestone.sourcePath, OutputPath: "backlog.yaml", Kind: "milestone", ID: milestone.id, Mappings: mappings}
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

// readDateField reads a timestamp from either key. An empty string counts as
// absent, as Backlog.md writes created_date: "" for some tasks.
func readDateField(fields map[string]any, preferred, alternate string) (time.Time, bool, error) {
	var preferredTime, alternateTime time.Time
	var hasPreferred, hasAlternate bool
	present := func(value any) bool {
		text, isText := value.(string)
		return value != nil && (!isText || strings.TrimSpace(text) != "")
	}
	if value, ok := fields[preferred]; ok && present(value) {
		parsed, err := parseLegacyTime(value)
		if err != nil {
			return time.Time{}, false, err
		}
		preferredTime, hasPreferred = parsed, true
	}
	if value, ok := fields[alternate]; ok && present(value) {
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
		"due_date": true, "due": true, "created_date": true, "created": true, "updated_date": true,
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
