package backlogmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"go.yaml.in/yaml/v3"
)

const transitionRestore = "backlogmd:draft-transition"

func (s *Source) status(value string) (string, error) {
	for key := range s.options.Statuses {
		if strings.EqualFold(key, value) {
			if strings.EqualFold(key, "draft") {
				return "draft", nil
			}
			return key, nil
		}
	}
	return "", source.Refuse("unknown-status", value)
}

func mutationField(key string) (keys []string, list bool) {
	switch key {
	case "title", "status", "type", "priority", "milestone", "project", "ordinal", "url":
		return []string{key}, false
	case "due", "due_date":
		return []string{"due_date", "due"}, false
	case "assignees", "assignee":
		return []string{"assignee", "assignees"}, true
	case "blocked_by", "dependencies":
		return []string{"dependencies", "blocked_by"}, true
	case "parent", "parent_task_id":
		return []string{"parent_task_id", "parent"}, false
	case "duplicate_of":
		return []string{key}, false
	case "related", "labels":
		return []string{key}, true
	case "refs":
		return []string{"references", "documentation", "refs"}, true
	case "references", "documentation":
		return []string{key}, true
	case "files", "modified_files":
		return []string{"modified_files", "files"}, true
	}
	return nil, false
}

func (s *Source) fieldMutation(d *document, m model.Mutation) ([]byte, string, error) {
	keys, list := mutationField(m.Field)
	if keys == nil {
		return nil, "", source.Refuse("unsupported", "field "+m.Field+" is not writable")
	}
	if m.Op == model.Unset && (m.Field == "title" || m.Field == "status") {
		return nil, "", source.Refuse("invalid-value", "required field cannot be unset")
	}
	if m.Op == model.Add || m.Op == model.Remove {
		if !list {
			return nil, "", source.Refuse("invalid-value", "expected list field")
		}
		values, err := mergedLists(d.parsed.fields, keys...)
		if err != nil {
			return nil, "", err
		}
		value, ok := m.Value.(string)
		if !ok || strings.ContainsAny(value, "\r\n") {
			return nil, "", source.Refuse("invalid-value", "list entry must be a single-line string")
		}
		index := slices.Index(values, value)
		if m.Op == model.Add {
			if index >= 0 {
				return bytes.Clone(d.raw), "", nil
			}
			key := keys[0]
			for _, candidate := range keys {
				if _, exists := d.fields[candidate]; exists {
					key = candidate
					break
				}
			}
			own, err := optionalList(d.parsed.fields, key)
			if err != nil {
				return nil, "", err
			}
			next, err := d.listEdit(key, append(own, value), -1)
			return next, "", err
		}
		if index < 0 {
			return bytes.Clone(d.raw), "", nil
		}
		data := bytes.Clone(d.raw)
		for _, key := range keys {
			current, err := parseDocument(data)
			if err != nil {
				return nil, "", err
			}
			own, err := optionalList(current.parsed.fields, key)
			if err != nil {
				return nil, "", err
			}
			if index := slices.Index(own, value); index >= 0 {
				data, err = current.listEdit(key, slices.Delete(own, index, index+1), index)
				if err != nil {
					return nil, "", err
				}
			}
		}
		return data, "", nil
	}
	var value any
	status := ""
	if m.Op != model.Unset {
		if list {
			values, ok := m.Value.([]string)
			if !ok {
				return nil, "", source.Refuse("invalid-value", "field requires []string")
			}
			for _, v := range values {
				if strings.ContainsAny(v, "\r\n") {
					return nil, "", source.Refuse("invalid-value", "list entries must be single-line strings")
				}
			}
			prior, err := mergedLists(d.parsed.fields, keys...)
			if err != nil {
				return nil, "", err
			}
			if slices.Equal(prior, values) {
				return bytes.Clone(d.raw), "", nil
			}
			value = values
		} else {
			text, ok := m.Value.(string)
			if !ok || strings.ContainsAny(text, "\r\n") {
				return nil, "", source.Refuse("invalid-value", "field requires a single-line string")
			}
			if m.Field == "due" || m.Field == "due_date" {
				if text != "" {
					if _, err := time.Parse("2006-01-02", text); err != nil {
						return nil, "", source.Refuse("invalid-value", "due must be YYYY-MM-DD")
					}
				}
			}
			value = text
			if m.Field == "milestone" && text != "" {
				milestones, _, err := s.Catalog()
				if err != nil {
					return nil, "", err
				}
				value = resolveMilestone(milestones, text)
			}
			if m.Field == "priority" && text != "" {
				if err := s.validatePriority(text); err != nil {
					return nil, "", err
				}
				value = strings.ToLower(text)
			}
			if m.Field == "status" {
				var err error
				status, err = s.status(text)
				if err != nil {
					return nil, "", err
				}
				if status == "draft" {
					// Demotion retains the provider status in frontmatter.
					return bytes.Clone(d.raw), status, nil
				}
				value = status
			}
			if m.Field == "title" && strings.TrimSpace(text) == "" {
				return nil, "", source.Refuse("invalid-value", "title is required")
			}
			if m.Field == "ordinal" {
				n, ok := new(big.Int).SetString(text, 10)
				if !ok || n.Sign() < 0 {
					return nil, "", source.Refuse("invalid-value", "ordinal must be a non-negative integer")
				}
				// yaml.Node retains large integer ordinals without float rounding.
				value = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: n.String()}
			}
		}
	}
	// Keep the provider's existing alias, but remove redundant aliases when a
	// caller explicitly replaces the merged model field.
	primary := keys[0]
	for _, key := range keys {
		if _, ok := d.fields[key]; ok {
			primary = key
			break
		}
	}
	data := bytes.Clone(d.raw)
	for _, key := range keys {
		current, err := parseDocument(data)
		if err != nil {
			return nil, "", err
		}
		unset := m.Op == model.Unset || key != primary
		if text, ok := value.(string); ok && text == "" && key != "title" && key != "status" {
			unset = true
		}
		data, err = current.fieldEdit(key, value, unset)
		if err != nil {
			return nil, "", err
		}
	}
	return data, status, nil
}

func safeSectionText(text string, description bool) error {
	fence := markdownFence{}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if fence.consume(line) {
			continue
		}
		if markerLine.MatchString(strings.TrimSpace(line)) || strings.HasPrefix(line, "## ") && (!description || bodyCategory(strings.TrimPrefix(line, "## ")) != "") {
			return source.Refuse("invalid-value", "text must not add section headings or generated markers")
		}
	}
	if fence.count > 0 {
		return source.Refuse("invalid-value", "unclosed code fence")
	}
	return nil
}

// edit is pure. Only Apply allocates IDs or publishes bytes.
func (s *Source) edit(data []byte, m model.Mutation, now time.Time) ([]byte, string, error) {
	d, err := parseDocument(data)
	if err != nil {
		return nil, "", err
	}
	status := ""
	var next []byte
	switch m.Op {
	case model.Batch:
		next = bytes.Clone(data)
		for _, change := range m.Changes {
			if change.Op == model.Batch || change.Op == model.Create || change.Op == model.Archive || change.Op == model.Unarchive || change.Op == model.Restore {
				return nil, "", source.Refuse("unsupported", "operation not allowed in batch")
			}
			if change.Ref.Source != "" && change.Ref.Source != s.options.ID || change.Ref.ID != "" && canonicalID(change.Ref.ID) != canonicalID(m.Ref.ID) {
				return nil, "", source.Refuse("source-mismatch", "batch must target one item")
			}
			var requested string
			next, requested, err = s.edit(next, change, now)
			if err != nil {
				return nil, "", err
			}
			if requested != "" {
				status = requested
			}
		}
	case model.Set, model.Unset, model.Add, model.Remove:
		next, status, err = s.fieldMutation(d, m)
	case model.ReplaceSection:
		key := sectionKey(m.Section)
		if key == "comments" || key == "" {
			return nil, "", source.Refuse("unsupported", "comments are append-only; known section required")
		}
		if err := safeSectionText(m.Text, key == "description"); err != nil {
			return nil, "", err
		}
		text := m.Text
		if key == "criteria" || key == "dod" {
			text = indexedText(text)
		}
		next, err = d.sectionEdit(key, text, false)
	case model.AppendNote:
		if err := safeSectionText(m.Text, false); err != nil {
			return nil, "", err
		}
		next, err = d.sectionEdit("notes", m.Text, true)
	case model.AppendComment:
		comment := m.Comment
		if comment.Author == "" || strings.TrimSpace(comment.Author) != comment.Author || strings.ContainsAny(comment.Author, "\r\n") || strings.TrimSpace(comment.Body) == "" {
			return nil, "", source.Refuse("invalid-value", "comment author and body required")
		}
		if err := safeSectionText(comment.Body, false); err != nil {
			return nil, "", err
		}
		for _, line := range strings.Split(comment.Body, "\n") {
			if strings.TrimSpace(line) == "---" {
				return nil, "", source.Refuse("invalid-value", "standalone --- lines are reserved in comments")
			}
		}
		if comment.Time.IsZero() {
			comment.Time = now
		}
		text := "author: " + comment.Author + d.nl + "created: " + comment.Time.UTC().Format("2006-01-02 15:04") + d.nl + "---" + d.nl + comment.Body + d.nl + "---"
		next, err = d.sectionEdit("comments", text, true)
	case model.CheckCriterion, model.UncheckCriterion, model.AddCriterion, model.RemoveCriterion, model.SetCriterion:
		next, err = d.criterionEdit(m)
	case model.Restore:
		original, parseErr := parseDocument(m.Original)
		if parseErr != nil {
			return nil, "", parseErr
		}
		if m.Field == transitionRestore {
			status, _ = m.Value.(string) // validated with partition and identity in existing
			next, err = original.fieldEdit("id", d.parsed.fields["id"], false)
			if err == nil && status != "draft" {
				var restored *document
				restored, err = parseDocument(next)
				if err == nil {
					next, err = restored.fieldEdit("status", status, false)
				}
			}
		} else {
			if original.parsed.fields["id"] != d.parsed.fields["id"] {
				return nil, "", source.Refuse("invalid-value", "restore cannot change ID")
			}
			// Undo restores the exact version-bound provider bytes, including
			// timestamp presence and criterion formatting, like the native codec.
			return bytes.Clone(m.Original), "", nil
		}
	default:
		return nil, "", source.Refuse("unsupported", string(m.Op))
	}
	if err != nil {
		return nil, "", err
	}
	nextDoc, err := parseDocument(next)
	if err != nil {
		return nil, "", err
	}
	next = nextDoc.numberCriteria()
	nextDoc, err = parseDocument(next)
	if err != nil {
		return nil, "", err
	}
	next, err = nextDoc.fieldEdit("updated_date", now.UTC().Format("2006-01-02 15:04"), false)
	return next, status, err
}

func (s *Source) existing(ctx context.Context, m model.Mutation) (sourceCandidate, []byte, []project.Milestone, model.Item, error) {
	if m.Ref.Source != "" && m.Ref.Source != s.options.ID {
		return sourceCandidate{}, nil, nil, model.Item{}, source.Refuse("source-mismatch", m.Ref.Source)
	}
	candidate, err := s.locate(ctx, m.Ref.ID)
	if err != nil {
		return candidate, nil, nil, model.Item{}, err
	}
	data, err := os.ReadFile(candidate.absolute)
	if err != nil {
		return candidate, nil, nil, model.Item{}, err
	}
	if m.ExpectedVersion == "" || m.ExpectedVersion != contentVersion(data) {
		return candidate, nil, nil, model.Item{}, source.Refuse("conflict", "expected version does not match current content")
	}
	milestones, _, err := s.Catalog()
	if err != nil {
		return candidate, nil, nil, model.Item{}, err
	}
	item, err := s.item(candidate, data, milestones)
	if err == nil && m.Op == model.Restore && m.Field == transitionRestore {
		err = s.validateTransitionRestore(candidate, item, m)
	}
	return candidate, data, milestones, item, err
}

func (s *Source) validateTransitionRestore(candidate sourceCandidate, current model.Item, m model.Mutation) error {
	status, ok := m.Value.(string)
	if !ok {
		return source.Refuse("invalid-value", "transition inverse requires its prior status")
	}
	prior, err := s.status(status)
	if err != nil || prior != status || candidate.draft == (prior == "draft") {
		return source.Refuse("invalid-value", "transition inverse must restore the opposite partition")
	}
	original, err := parseDocument(m.Original)
	if err != nil {
		return err
	}
	id, _ := scalarString(original.parsed.fields["id"])
	partition, originalID, recorded := strings.Cut(m.Section, "|")
	if !recorded || !validBacklogID(strings.ToUpper(id)) || canonicalID(id) != canonicalID(originalID) || canonicalID(id) == canonicalID(current.Ref.ID) {
		return source.Refuse("invalid-value", "transition inverse has invalid original identity")
	}
	archived := strings.HasPrefix(partition, "archive/")
	partition = strings.TrimPrefix(partition, "archive/")
	prefix, _, _ := strings.Cut(strings.ToUpper(id), "-")
	if !slices.Contains([]string{"tasks", "completed", "drafts"}, partition) || archived != candidate.archived || (partition == "drafts") != (prior == "draft") || (prefix == "DRAFT") != (prior == "draft") {
		return source.Refuse("invalid-value", "transition inverse identity does not match its original partition")
	}
	if partition == "completed" {
		if s.options.Statuses[prior] != model.Done {
			return source.Refuse("invalid-value", "completed inverse requires a done status")
		}
	} else if prior != "draft" {
		rawStatus, _ := scalarString(original.parsed.fields["status"])
		if rawStatus != prior {
			return source.Refuse("invalid-value", "transition inverse status does not match its original content")
		}
	}
	return nil
}

func previewCandidate(candidate sourceCandidate, requested string) sourceCandidate {
	if requested != "" && candidate.draft != (requested == "draft") {
		candidate.draft, candidate.completed = requested == "draft", false
	}
	return candidate
}

func (s *Source) Prepare(ctx context.Context, m model.Mutation) (source.Prepared, error) {
	if !s.Info().Capabilities.Allows(m.Op) {
		return source.Prepared{}, source.Refuse("unsupported", string(m.Op))
	}
	candidate, data, milestones, before, err := s.existing(ctx, m)
	if err != nil {
		return source.Prepared{}, err
	}
	inverse := model.Mutation{Ref: before.Ref, Op: model.Restore, Original: bytes.Clone(data)}
	if m.Op == model.Archive || m.Op == model.Unarchive {
		inverse.Op = model.Archive
		if !candidate.archived {
			inverse.Op = model.Unarchive
		}
		return source.Prepared{Item: before, Inverse: inverse}, nil
	}
	next, requested, err := s.edit(data, m, s.options.Now())
	if err != nil {
		return source.Prepared{}, err
	}
	preview := previewCandidate(candidate, requested)
	item, err := s.item(preview, next, milestones)
	if err != nil {
		return source.Prepared{}, err
	}
	if candidate.draft != preview.draft {
		inverse = model.Mutation{Ref: before.Ref, Op: model.Set, Field: "status", Value: before.Status}
		if m.Op == model.Batch || m.Op == model.Restore {
			inverse = model.Mutation{Ref: before.Ref, Op: model.Restore, Field: transitionRestore, Section: filepath.ToSlash(candidate.partition) + "|" + before.Ref.ID, Value: before.Status, Original: bytes.Clone(data)}
		}
	}
	return source.Prepared{Item: item, Inverse: inverse}, nil
}

func (s *Source) Apply(ctx context.Context, m model.Mutation) (model.Item, error) {
	if m.Ref.Source != "" && m.Ref.Source != s.options.ID {
		return model.Item{}, source.Refuse("source-mismatch", m.Ref.Source)
	}
	if !s.Info().Capabilities.Allows(m.Op) {
		return model.Item{}, source.Refuse("unsupported", string(m.Op))
	}
	lock, err := s.lock(ctx)
	if err != nil {
		return model.Item{}, err
	}
	defer unlock(lock)
	if m.Op == model.Create {
		return s.create(ctx, m)
	}
	candidate, data, milestones, before, err := s.existing(ctx, m)
	if err != nil {
		return model.Item{}, err
	}
	info, err := os.Lstat(candidate.absolute)
	if err != nil || !info.Mode().IsRegular() {
		return model.Item{}, source.Refuse("invalid-path", "item is not a regular file")
	}
	change := fileChange{path: candidate.absolute, target: candidate.absolute, before: data, data: data, mode: info.Mode()}
	if m.Op == model.Archive || m.Op == model.Unarchive {
		partition := "tasks"
		if candidate.draft {
			partition = "drafts"
		} else if candidate.completed {
			partition = "completed"
		}
		if m.Op == model.Archive {
			partition = filepath.Join("archive", partition)
		}
		change.target = filepath.Join(s.root, partition, filepath.Base(candidate.absolute))
		if change.target == change.path {
			return before, nil
		}
		if err := s.publish([]fileChange{change}); err != nil {
			return model.Item{}, err
		}
		return before, nil
	}
	next, requested, err := s.edit(data, m, s.options.Now())
	if err != nil {
		return model.Item{}, err
	}
	preview := previewCandidate(candidate, requested)
	var changes []fileChange
	if preview.draft != candidate.draft {
		id, err := s.allocate(ctx, preview.draft)
		if err != nil {
			return model.Item{}, err
		}
		doc, err := parseDocument(next)
		if err != nil {
			return model.Item{}, err
		}
		next, err = doc.fieldEdit("id", id, false)
		if err != nil {
			return model.Item{}, err
		}
		partition := "tasks"
		if preview.draft {
			partition = "drafts"
		}
		if preview.archived {
			partition = filepath.Join("archive", partition)
		}
		title, _ := scalarString(doc.parsed.fields["title"])
		change.target = filepath.Join(s.root, partition, taskFilename(id, title))
		changes, err = s.referenceChanges(ctx, before.Ref.ID, id, candidate.absolute)
		if err != nil {
			return model.Item{}, err
		}
		// Self-references follow the new identity too.
		next, err = s.rewriteReferences(next, before.Ref.ID, id)
		if err != nil {
			return model.Item{}, err
		}
	}
	item, err := s.item(preview, next, milestones)
	if err != nil {
		return model.Item{}, err
	}
	change.data = next
	changes = append(changes, change)
	if err := ctx.Err(); err != nil {
		return model.Item{}, err
	}
	if err := s.publish(changes); err != nil {
		return model.Item{}, err
	}
	return item, nil
}

func (s *Source) allocate(ctx context.Context, draft bool) (string, error) {
	config, err := locateSource(s.options.Path)
	if err != nil {
		return "", err
	}
	prefix := strings.ToUpper(config.taskPrefix)
	if prefix == "" {
		prefix = "TASK"
	}
	if draft {
		prefix = "DRAFT"
	}
	if !validNativePrefix(prefix) {
		return "", source.Refuse("invalid-source", "invalid task prefix")
	}
	width := 0
	if value, exists := config.config["zero_padded_ids"]; exists {
		n, err := integerValue(value)
		if err != nil || !n.IsInt64() || n.Sign() < 0 || n.Int64() > 1000 {
			return "", source.Refuse("invalid-source", "invalid zero_padded_ids")
		}
		width = int(n.Int64())
	}
	next := big.NewInt(1)
	paths, err := s.candidates(ctx, true)
	if err != nil {
		return "", err
	}
	for _, candidate := range paths {
		data, err := os.ReadFile(candidate.absolute)
		if err != nil {
			return "", err
		}
		doc, err := parseSourceDocument(data)
		if err != nil {
			return "", err
		}
		id, _ := scalarString(doc.fields["id"])
		if !validBacklogID(strings.ToUpper(id)) {
			return "", source.Refuse("invalid-id", id)
		}
		p, numeric, _ := strings.Cut(strings.ToUpper(id), "-")
		if p == prefix {
			first, _, _ := strings.Cut(numeric, ".")
			n, _ := new(big.Int).SetString(first, 10)
			if n.Cmp(next) >= 0 {
				next = new(big.Int).Add(n, big.NewInt(1))
			}
		}
	}
	counter := filepath.Join(s.state, "backlogmd-next", contentVersion([]byte(s.root)), prefix)
	if data, err := os.ReadFile(counter); err == nil {
		n, ok := new(big.Int).SetString(strings.TrimSpace(string(data)), 10)
		if !ok || n.Sign() <= 0 {
			return "", source.Refuse("invalid-counter", counter)
		}
		if n.Cmp(next) > 0 {
			next = n
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(counter), 0700); err != nil {
		return "", err
	}
	if err := atomicWrite(counter, []byte(new(big.Int).Add(next, big.NewInt(1)).String()+"\n"), 0600); err != nil {
		return "", err
	}
	return prefix + "-" + fmt.Sprintf("%0*s", width, next.String()), nil
}

func taskFilename(id, title string) string {
	var slug strings.Builder
	separator := false
	for _, char := range title {
		switch {
		case unicode.IsSpace(char) || char == '-':
			separator = slug.Len() > 0
		case unicode.IsLetter(char) || unicode.IsNumber(char) || char == '_' || char == '.':
			if separator {
				slug.WriteByte('-')
				separator = false
			}
			slug.WriteRune(char)
		}
	}
	if slug.Len() == 0 {
		slug.WriteString("untitled")
	}
	return strings.ToLower(id) + " - " + slug.String() + ".md"
}

func (s *Source) create(ctx context.Context, m model.Mutation) (model.Item, error) {
	if m.Item == nil || m.Item.Ref.ID != "" || m.Ref.ID != "" || m.Item.Ref.Source != "" && m.Item.Ref.Source != s.options.ID {
		return model.Item{}, source.Refuse("invalid-create", "template without an ID required")
	}
	item := *m.Item
	if item.Rank != "" {
		return model.Item{}, source.Refuse("unsupported", "Backlog.md uses ordinal, not native rank keys")
	}
	config, err := locateSource(s.options.Path)
	if err != nil {
		return model.Item{}, err
	}
	if item.Status == "" {
		item.Status, _ = scalarString(config.config["default_status"])
		if item.Status == "" {
			item.Status = "To Do"
		}
	}
	status, err := s.status(item.Status)
	if err != nil {
		return model.Item{}, err
	}
	item.Status = status
	now := s.options.Now().UTC().Truncate(time.Minute)
	milestones, _, err := s.Catalog()
	if err != nil {
		return model.Item{}, err
	}
	item.Milestone = resolveMilestone(milestones, item.Milestone)
	if item.Priority != "" {
		if err := s.validatePriority(string(item.Priority)); err != nil {
			return model.Item{}, err
		}
	}
	if item.Due != "" {
		if _, err := time.Parse("2006-01-02", item.Due); err != nil {
			return model.Item{}, source.Refuse("invalid-value", "due must be YYYY-MM-DD")
		}
	}
	// Validate all bytes and fields before reserving an ID.
	data, err := newTaskBytes(item, "TASK-1", now, config)
	if err != nil {
		return model.Item{}, err
	}
	candidate := sourceCandidate{draft: status == "draft"}
	if _, err := s.item(candidate, data, milestones); err != nil {
		return model.Item{}, err
	}
	id, err := s.allocate(ctx, candidate.draft)
	if err != nil {
		return model.Item{}, err
	}
	doc, err := parseDocument(data)
	if err != nil {
		return model.Item{}, err
	}
	data, err = doc.fieldEdit("id", id, false)
	if err != nil {
		return model.Item{}, err
	}
	partition := "tasks"
	if candidate.draft {
		partition = "drafts"
	}
	path := filepath.Join(s.root, partition, taskFilename(id, item.Title))
	candidate.absolute = path
	created, err := s.item(candidate, data, milestones)
	if err != nil {
		return model.Item{}, err
	}
	if err := ctx.Err(); err != nil {
		return model.Item{}, err
	}
	if err := s.publish([]fileChange{{target: path, data: data, mode: 0644}}); err != nil {
		return model.Item{}, err
	}
	return created, nil
}

func resolveMilestone(milestones []project.Milestone, value string) string {
	for _, milestone := range milestones {
		if strings.EqualFold(milestone.Key, value) {
			return milestone.Key
		}
	}
	key := ""
	for _, milestone := range milestones {
		if strings.EqualFold(strings.TrimSpace(milestone.Name), strings.TrimSpace(value)) {
			if key != "" {
				return value // ambiguous titles never choose an arbitrary milestone
			}
			key = milestone.Key
		}
	}
	if key != "" {
		return key
	}
	return value
}

func (s *Source) validatePriority(value string) error {
	config, err := locateSource(s.options.Path)
	if err != nil {
		return err
	}
	priorities := []string{"high", "medium", "low"}
	if configured, exists := config.config["priorities"]; exists {
		priorities, err = stringList(configured, "priorities", false)
		if err != nil {
			return err
		}
	}
	for _, priority := range priorities {
		if strings.EqualFold(priority, value) {
			return nil
		}
	}
	return source.Refuse("invalid-value", "priority is not configured in Backlog.md")
}

func refStrings(refs []model.Ref) []string {
	values := make([]string, 0, len(refs))
	for _, ref := range refs {
		value := ref.ID
		if ref.Source != "" {
			value = ref.Source + ":" + value
		}
		values = append(values, value)
	}
	return values
}

func newTaskBytes(item model.Item, id string, now time.Time, config sourceConfig) ([]byte, error) {
	if strings.TrimSpace(item.Title) == "" || strings.ContainsAny(item.Title, "\r\n") {
		return nil, source.Refuse("invalid-create", "single-line title required")
	}
	for _, criteria := range [][]model.Criterion{item.Criteria, item.DoD} {
		for _, criterion := range criteria {
			if strings.TrimSpace(criterion.Text) == "" || strings.ContainsAny(criterion.Text, "\r\n") || strings.ContainsAny(criterion.Evidence, "\r\n") {
				return nil, source.Refuse("invalid-value", "criteria and evidence must be single-line")
			}
		}
	}
	status := item.Status
	if status == "draft" {
		status, _ = scalarString(config.config["default_status"])
		if status == "" || strings.EqualFold(status, "draft") {
			status = "To Do"
		}
	}
	values := map[string]any{"id": id, "title": item.Title, "status": status, "assignee": slices.Clone(item.Assignees), "created_date": now.Format("2006-01-02 15:04"), "labels": slices.Clone(item.Labels), "dependencies": refStrings(item.BlockedBy)}
	for _, entry := range []struct{ key, value string }{{"type", item.Type}, {"priority", string(item.Priority)}, {"milestone", item.Milestone}, {"due_date", item.Due}, {"url", item.URL}} {
		if entry.value != "" {
			values[entry.key] = entry.value
		}
	}
	if len(item.Refs) > 0 {
		values["references"] = item.Refs
	}
	if len(item.Files) > 0 {
		values["modified_files"] = item.Files
	}
	if item.Parent != nil {
		values["parent_task_id"] = refStrings([]model.Ref{*item.Parent})[0]
	}
	if item.DuplicateOf != nil {
		values["duplicate_of"] = refStrings([]model.Ref{*item.DuplicateOf})[0]
	}
	if len(item.Related) > 0 {
		values["related"] = refStrings(item.Related)
	}
	for key, value := range item.Fields {
		if _, exists := values[key]; !exists && !slices.Contains(backlogFieldOrder, key) && !slices.Contains([]string{"assignees", "blocked_by", "parent", "due", "created", "updated", "rank", "refs", "files"}, key) {
			values[key] = value
		}
	}
	if ordinal, ok := item.Fields["ordinal"]; ok {
		n, err := integerValue(ordinal)
		if err != nil || n.Sign() < 0 {
			return nil, source.Refuse("invalid-value", "ordinal must be a non-negative integer")
		}
		values["ordinal"] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: n.String()}
	}
	mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	keys := slices.Clone(backlogFieldOrder)
	var extra []string
	for key := range values {
		if !slices.Contains(keys, key) {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)
	for _, key := range keys {
		value, exists := values[key]
		if !exists {
			continue
		}
		var node yaml.Node
		if err := node.Encode(value); err != nil {
			return nil, err
		}
		if key == "created_date" || key == "due_date" {
			node.Style = yaml.SingleQuotedStyle
		}
		if node.Kind == yaml.SequenceNode && len(node.Content) == 0 {
			node.Style = yaml.FlowStyle
		}
		mapping.Content = append(mapping.Content, stringNode(key), &node)
	}
	data := []byte("---\n" + yamlText(mapping) + "\n---\n")
	for _, section := range []struct{ key, text string }{{"description", item.Description}, {"criteria", criteriaText(item.Criteria)}, {"dod", criteriaText(item.DoD)}, {"plan", item.Plan}, {"notes", item.Notes}, {"summary", item.Summary}} {
		if section.text == "" {
			continue
		}
		if err := safeSectionText(section.text, section.key == "description"); err != nil {
			return nil, err
		}
		doc, err := parseDocument(data)
		if err != nil {
			return nil, err
		}
		data, err = doc.sectionEdit(section.key, section.text, false)
		if err != nil {
			return nil, err
		}
	}
	if len(item.Comments) > 0 {
		return nil, source.Refuse("unsupported", "append comments after creating the task")
	}
	return data, nil
}

func criteriaText(criteria []model.Criterion) string {
	var lines []string
	for i, criterion := range criteria {
		mark := " "
		if criterion.Checked {
			mark = "x"
		}
		lines = append(lines, fmt.Sprintf("- [%s] #%d %s", mark, i+1, criterion.Text))
		if criterion.Evidence != "" {
			lines = append(lines, "  Evidence: "+criterion.Evidence)
		}
	}
	return strings.Join(lines, "\n")
}

func (s *Source) referenceChanges(ctx context.Context, oldID, newID, except string) ([]fileChange, error) {
	paths, err := s.candidates(ctx, true)
	if err != nil {
		return nil, err
	}
	var changes []fileChange
	for _, candidate := range paths {
		if candidate.absolute == except {
			continue
		}
		data, err := os.ReadFile(candidate.absolute)
		if err != nil {
			return nil, err
		}
		next, err := s.rewriteReferences(data, oldID, newID)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(data, next) {
			info, err := os.Lstat(candidate.absolute)
			if err != nil || !info.Mode().IsRegular() {
				return nil, source.Refuse("invalid-path", "reference file is not regular")
			}
			changes = append(changes, fileChange{path: candidate.absolute, target: candidate.absolute, before: data, data: next, mode: info.Mode()})
		}
	}
	return changes, nil
}

func (s *Source) rewriteReferences(data []byte, oldID, newID string) ([]byte, error) {
	for _, key := range []string{"dependencies", "blocked_by", "parent_task_id", "parent", "related", "duplicate_of"} {
		doc, err := parseDocument(data)
		if err != nil {
			return nil, err
		}
		field, exists := doc.fields[key]
		if !exists {
			continue
		}
		nodes := []*yaml.Node{field.value}
		if field.value.Kind == yaml.SequenceNode {
			nodes = field.value.Content
		}
		changed := false
		var values []string
		for _, node := range nodes {
			value := node.Value
			ref := parseReference(value)
			if (ref.Source == "" || ref.Source == s.options.ID) && canonicalID(ref.ID) == canonicalID(oldID) {
				value = newID
				if ref.Source != "" {
					value = ref.Source + ":" + newID
				}
				changed = true
			}
			values = append(values, value)
		}
		if !changed {
			continue
		}
		var value any = values
		if field.value.Kind != yaml.SequenceNode {
			value = values[0]
		}
		data, err = doc.fieldEdit(key, value, false)
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}
