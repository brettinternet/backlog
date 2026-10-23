package backlogmd

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

// Options locates a Backlog.md project or its backlog directory. Statuses use
// Backlog.md's raw status names; draft is a synthetic status for drafts/.
type Options struct {
	ID, Path, CommonDir string
	Statuses            map[string]model.StatusCategory
	ReadOnly            bool
	Now                 func() time.Time
}

type Source struct {
	options Options
	root    string
	state   string
}

var _ source.Source = (*Source)(nil)
var _ source.Preparer = (*Source)(nil)
var _ source.Watcher = (*Source)(nil)
var safeSourceID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func New(o Options) (*Source, error) {
	if !safeSourceID.MatchString(o.ID) || !filepath.IsAbs(o.Path) || o.CommonDir != "" && !filepath.IsAbs(o.CommonDir) {
		return nil, source.Refuse("invalid-source", "absolute path and safe source ID required")
	}
	config, err := locateSource(o.Path)
	if err != nil {
		return nil, err
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	statuses := make(map[string]model.StatusCategory, len(o.Statuses)+1)
	if len(o.Statuses) == 0 {
		settings, err := project.BacklogMDSettings(o.Path)
		if err != nil {
			return nil, err
		}
		for _, status := range settings.Statuses {
			statuses[status.Key] = model.StatusCategory(status.Category)
		}
	} else {
		for key, category := range o.Statuses {
			statuses[key] = category
		}
	}
	if _, ok := statuses["draft"]; !ok {
		statuses["draft"] = model.Backlog
	}
	o.Statuses = statuses
	state := filepath.Join(o.CommonDir, "backlog")
	if o.CommonDir == "" {
		paths, err := project.UserPaths()
		if err != nil {
			return nil, err
		}
		state = filepath.Join(paths.StateDir, "backlogmd", fmt.Sprintf("%x", sha256.Sum256([]byte(config.root))))
	}
	return &Source{options: o, root: config.root, state: state}, nil
}

func (s *Source) Info() source.Info {
	caps := model.Capabilities{}
	if !s.options.ReadOnly {
		for _, op := range []model.Operation{model.Batch, model.Create, model.Archive, model.Unarchive, model.Set, model.Add, model.Remove, model.Unset, model.ReplaceSection, model.AppendNote, model.AppendComment, model.CheckCriterion, model.UncheckCriterion, model.AddCriterion, model.RemoveCriterion, model.Restore} {
			caps[op] = true
		}
	}
	return source.Info{ID: s.options.ID, Kind: "backlog-md", Display: s.root, Capabilities: caps}
}

// Catalog reads the read-only milestone and configured label catalogs. A task
// may still carry values absent from either catalog.
func (s *Source) Catalog() ([]project.Milestone, []string, error) {
	settings, err := project.BacklogMDSettings(s.options.Path)
	return settings.Milestones, settings.Labels, err
}

func (s *Source) candidates(ctx context.Context, archive bool) ([]sourceCandidate, error) {
	paths, err := scanPartitions(ctx, s.root, &Report{})
	if err != nil {
		return nil, err
	}
	if archive {
		return paths, nil
	}
	live := paths[:0]
	for _, candidate := range paths {
		if !candidate.archived {
			live = append(live, candidate)
		}
	}
	return live, nil
}

func (s *Source) item(candidate sourceCandidate, data []byte, milestones []project.Milestone) (model.Item, error) {
	task, err := readTaskBytes(candidate, data)
	if err != nil {
		return model.Item{}, err
	}
	if !validBacklogID(strings.ToUpper(task.id)) {
		return model.Item{}, source.Refuse("invalid-id", task.id)
	}
	status := task.status
	if candidate.draft {
		status = "draft"
	}
	category, ok := s.options.Statuses[status]
	if !ok {
		return model.Item{}, source.Refuse("unknown-status", status)
	}
	byKey, byTitle, duplicates := map[string]string{}, map[string]string{}, map[string]bool{}
	for _, milestone := range milestones {
		byKey[strings.ToLower(milestone.Key)] = milestone.Key
		title := strings.ToLower(strings.TrimSpace(milestone.Name))
		if _, exists := byTitle[title]; exists {
			duplicates[title] = true
		}
		byTitle[title] = milestone.Key
	}
	for title := range duplicates {
		delete(byTitle, title)
	}
	if task.ordinal != nil {
		// Backlog.md ordinals are non-negative integers, not native rank keys.
		// Padding preserves their lexical ordering without reading other tasks.
		if task.ordinal.Sign() < 0 {
			return model.Item{}, source.Refuse("invalid-value", "ordinal must be non-negative")
		}
		task.rank = fmt.Sprintf("%040s", task.ordinal.String())
	} else {
		task.rank, _ = scalarString(task.doc.fields["rank"])
	}
	item := model.Item{Ref: model.Ref{Source: s.options.ID, ID: strings.ToUpper(task.id)}, Title: task.title, Type: task.typeName, Status: status, Category: category, Rank: task.rank, Due: task.due, Milestone: task.milestone, Created: task.created, Updated: task.updated, Fields: task.doc.fields, Version: contentVersion(data)}
	if key, exists := byKey[strings.ToLower(item.Milestone)]; exists {
		item.Milestone = key
	} else if key, exists := byTitle[strings.ToLower(strings.TrimSpace(item.Milestone))]; exists {
		item.Milestone = key
	}
	for _, group := range []struct {
		target *[]string
		keys   []string
	}{{&item.Assignees, []string{"assignee", "assignees"}}, {&item.Labels, []string{"labels"}}, {&item.Refs, []string{"references", "documentation", "refs"}}, {&item.Files, []string{"modified_files", "files"}}} {
		*group.target, err = mergedLists(task.doc.fields, group.keys...)
		if err != nil {
			return model.Item{}, err
		}
	}
	for _, group := range []struct {
		kind model.EdgeKind
		keys []string
		list bool
	}{{model.BlockedBy, []string{"dependencies", "blocked_by"}, true}, {model.Parent, []string{"parent_task_id", "parent"}, false}, {model.Related, []string{"related"}, true}, {model.DuplicateOf, []string{"duplicate_of"}, false}} {
		var values []string
		if group.list {
			values, err = mergedLists(task.doc.fields, group.keys...)
		} else {
			var value string
			value, _, err = mergedScalar(task.doc.fields, group.keys...)
			if value != "" {
				values = []string{value}
			}
		}
		if err != nil {
			return model.Item{}, err
		}
		for _, value := range values {
			value, err := rewriteRef(value, nil)
			if err != nil {
				return model.Item{}, err
			}
			ref := parseReference(value)
			item.Edges = append(item.Edges, model.Edge{Kind: group.kind, Target: ref})
			switch group.kind {
			case model.Parent:
				item.Parent = &ref
			case model.BlockedBy:
				item.BlockedBy = append(item.BlockedBy, ref)
			case model.Related:
				item.Related = append(item.Related, ref)
			case model.DuplicateOf:
				item.DuplicateOf = &ref
			}
		}
	}
	priority, err := optionalStringField(task.doc.fields, "priority")
	if err != nil {
		return model.Item{}, err
	}
	item.Priority = model.Priority(strings.ToLower(priority))
	item.URL, _ = scalarString(task.doc.fields["url"])
	item.Description = task.doc.body.sections["description"]
	item.Plan, item.Notes, item.Summary = task.doc.body.sections["plan"], task.doc.body.sections["notes"], task.doc.body.sections["summary"]
	item.Criteria, item.DoD = readCriteria(task.doc.body.sections["criteria"]), readCriteria(task.doc.body.sections["dod"])
	for _, comment := range parseComments(task.doc.body.comments, task.updated) {
		item.Comments = append(item.Comments, model.Comment{Time: comment.time, Author: comment.author, Body: comment.body})
	}
	return item, nil
}

func contentVersion(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func (s *Source) Load(ctx context.Context, cursor source.Cursor) (source.Delta, error) {
	result := source.Delta{Coverage: source.Complete, Cursor: source.Cursor{IncludeArchive: cursor.IncludeArchive}, Archived: map[model.Ref]bool{}}
	paths, err := s.candidates(ctx, cursor.IncludeArchive)
	if err != nil {
		result.Coverage = source.Unknown
		return result, err
	}
	milestones, _, err := s.Catalog()
	if err != nil {
		result.Coverage = source.Unknown
		return result, err
	}
	seen := map[string]string{}
	for _, candidate := range paths {
		if err := ctx.Err(); err != nil {
			result.Coverage = source.Partial
			return result, err
		}
		data, e := os.ReadFile(candidate.absolute)
		var item model.Item
		if e == nil {
			item, e = s.item(candidate, data, milestones)
		}
		if e == nil {
			identity := canonicalID(item.Ref.ID)
			if previous, exists := seen[identity]; exists {
				e = source.Refuse("duplicate-id", "ID also occurs in "+previous)
			} else {
				seen[identity] = candidate.relative
			}
		}
		if e != nil {
			code := format.ErrorCode(e)
			var refusal *source.Error
			if errors.As(e, &refusal) {
				code = refusal.Reason
			}
			result.Findings = append(result.Findings, source.Finding{Code: code, Severity: "error", Path: candidate.absolute, Line: 1, Message: e.Error()})
			result.Coverage = source.Partial
			continue
		}
		result.Items = append(result.Items, item)
		result.Archived[item.Ref] = candidate.archived
	}
	sort.Slice(result.Items, func(i, j int) bool { return idLess(result.Items[i].Ref.ID, result.Items[j].Ref.ID) })
	return result, nil
}

func (s *Source) locate(ctx context.Context, id string) (sourceCandidate, error) {
	if !validBacklogID(strings.ToUpper(id)) {
		return sourceCandidate{}, source.Refuse("invalid-id", id)
	}
	paths, err := s.candidates(ctx, true)
	if err != nil {
		return sourceCandidate{}, err
	}
	var found *sourceCandidate
	for _, candidate := range paths {
		// IDs are authoritative in frontmatter. Legacy files need not have
		// title-bearing filenames, and padded and natural IDs compare equally.
		data, err := os.ReadFile(candidate.absolute)
		if err != nil {
			return sourceCandidate{}, err
		}
		doc, err := parseSourceDocument(data)
		if err != nil {
			return sourceCandidate{}, fmt.Errorf("%s: %w", candidate.relative, err)
		}
		fileID, _ := scalarString(doc.fields["id"])
		if canonicalID(fileID) == canonicalID(id) {
			if found != nil {
				return sourceCandidate{}, source.Refuse("duplicate-id", id)
			}
			copy := candidate
			found = &copy
		}
	}
	if found == nil {
		return sourceCandidate{}, source.Refuse("not-found", id)
	}
	return *found, nil
}

func (s *Source) Read(ctx context.Context, id string) (model.Item, error) {
	item, _, err := s.ReadState(ctx, id)
	return item, err
}

func (s *Source) ReadState(ctx context.Context, id string) (model.Item, bool, error) {
	if err := ctx.Err(); err != nil {
		return model.Item{}, false, err
	}
	candidate, err := s.locate(ctx, id)
	if err != nil {
		return model.Item{}, false, err
	}
	data, err := os.ReadFile(candidate.absolute)
	if err != nil {
		return model.Item{}, false, err
	}
	milestones, _, err := s.Catalog()
	if err != nil {
		return model.Item{}, false, err
	}
	item, err := s.item(candidate, data, milestones)
	return item, candidate.archived, err
}

// Watch performs the same two-second metadata sweep as the native polling
// fallback. Parsing and I/O remain on the source worker, never the UI loop.
func (s *Source) Watch(ctx context.Context, changed func()) error {
	identity := func() (string, error) {
		hash := sha256.New()
		err := filepath.WalkDir(s.root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type().IsRegular() && (strings.HasSuffix(path, ".md") || path == filepath.Join(s.root, "config.yml")) {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				fmt.Fprintf(hash, "%s\x00%d\x00%d\n", path, info.Size(), info.ModTime().UnixNano())
			}
			return nil
		})
		if err == nil {
			config, e := locateSource(s.options.Path)
			if e != nil {
				return "", e
			}
			if config.configPath != "" {
				info, e := os.Stat(config.configPath)
				if e != nil {
					return "", e
				}
				fmt.Fprintf(hash, "%s\x00%d\x00%d", config.configPath, info.Size(), info.ModTime().UnixNano())
			}
		}
		return fmt.Sprintf("%x", hash.Sum(nil)), err
	}
	before, err := identity()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			after, err := identity()
			if err != nil {
				return err
			}
			if before != after {
				before = after
				changed()
			}
		}
	}
}
