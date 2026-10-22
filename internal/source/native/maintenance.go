package native

import (
	"bytes"
	"context"
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
	"github.com/brettinternet/backlog/internal/source"
)

type checkedFile struct {
	source *Source
	path   string
	data   []byte
	mode   os.FileMode
	doc    *format.Document
	item   model.Item
}

type fileChange struct {
	oldPath  string
	newPath  string
	data     []byte
	original []byte
	mode     os.FileMode
}

type itemKey struct {
	source string
	id     string
}

type graphEdge struct {
	target itemKey
	path   string
	line   int
}

func lockSources(ctx context.Context, sources []*Source) ([]*os.File, error) {
	states := make([]string, 0, len(sources))
	seen := map[string]bool{}
	for _, s := range sources {
		if !seen[s.state] {
			states = append(states, s.state)
			seen[s.state] = true
		}
	}
	sort.Strings(states)
	var locks []*os.File
	for _, state := range states {
		var selected *Source
		for _, s := range sources {
			if s.state == state {
				selected = s
				break
			}
		}
		lock, err := selected.lock(ctx)
		if err != nil {
			for i := len(locks) - 1; i >= 0; i-- {
				unlock(locks[i])
			}
			return nil, err
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

func unlockSources(locks []*os.File) {
	for i := len(locks) - 1; i >= 0; i-- {
		unlock(locks[i])
	}
}

func sourceFiles(sources []*Source) ([]checkedFile, []source.Finding, error) {
	var files []checkedFile
	var findings []source.Finding
	for _, s := range sources {
		paths, err := s.paths(true)
		if err != nil {
			return nil, nil, err
		}
		for _, path := range paths {
			info, err := os.Lstat(path)
			if err != nil {
				return nil, nil, err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, nil, err
			}
			f := checkedFile{source: s, path: path, data: data, mode: info.Mode()}
			doc, err := format.Parse(data)
			if err != nil {
				findings = append(findings, source.Finding{
					Code: format.ErrorCode(err), Severity: "error", Path: path,
					Line: format.ErrorLine(err), Message: err.Error(),
				})
			} else {
				f.doc = doc
				f.item = doc.Item()
			}
			files = append(files, f)
		}
	}
	return files, findings, nil
}

func normalizedKey(sourceID, id string) itemKey {
	return itemKey{source: sourceID, id: canonical(id)}
}

func referenceTarget(ref string, defaultSource string) itemKey {
	sourceID, id, qualified := strings.Cut(ref, ":")
	if !qualified {
		sourceID, id = defaultSource, ref
	}
	return normalizedKey(sourceID, id)
}

func pathOrder(path string) string { return filepath.ToSlash(filepath.Clean(path)) }

func validateFiles(files []checkedFile, parseFindings []source.Finding) []source.Finding {
	findings := append([]source.Finding(nil), parseFindings...)
	items := make(map[itemKey][]checkedFile)
	known := make(map[itemKey]bool)
	for _, f := range files {
		if f.doc == nil {
			continue
		}
		itemKey := normalizedKey(f.source.options.ID, f.item.Ref.ID)
		items[itemKey] = append(items[itemKey], f)
		known[itemKey] = true
		if filepath.Base(f.path) != f.item.Ref.ID+".md" {
			findings = append(findings, source.Finding{Code: "filename-mismatch", Severity: "error", Path: f.path, Line: f.doc.FieldLine("id"), Message: "filename does not match item ID"})
		}
		if len(f.source.options.Statuses) > 0 {
			if _, ok := f.source.options.Statuses[f.item.Status]; !ok {
				findings = append(findings, source.Finding{Code: "unknown-status", Severity: "error", Path: f.path, Line: f.doc.FieldLine("status"), Message: "status is not defined in project config: " + f.item.Status})
			}
		}
		if line := format.NoncanonicalLine(f.data); line > 0 {
			findings = append(findings, source.Finding{Code: "noncanonical", Severity: "info", Path: f.path, Line: line, Message: "normalize line endings and add a final newline"})
		}
	}
	for _, duplicates := range items {
		if len(duplicates) < 2 {
			continue
		}
		sort.Slice(duplicates, func(i, j int) bool { return duplicates[i].path < duplicates[j].path })
		for _, f := range duplicates {
			findings = append(findings, source.Finding{Code: "duplicate-id", Severity: "error", Path: f.path, Line: f.doc.FieldLine("id"), Message: "item ID is also used by another active or archived item"})
		}
	}
	blockerGraph := map[itemKey][]graphEdge{}
	parentGraph := map[itemKey][]graphEdge{}
	for _, f := range files {
		if f.doc == nil {
			continue
		}
		from := normalizedKey(f.source.options.ID, f.item.Ref.ID)
		for _, ref := range f.doc.References() {
			to := referenceTarget(ref.Value, f.source.options.ID)
			if !known[to] {
				findings = append(findings, source.Finding{Code: "dangling-ref", Severity: "warning", Path: f.path, Line: ref.Line, Message: ref.Field + " references missing item " + ref.Value})
			}
			edge := graphEdge{target: to, path: f.path, line: ref.Line}
			switch ref.Field {
			case "blocked_by":
				blockerGraph[from] = append(blockerGraph[from], edge)
			case "parent":
				parentGraph[from] = append(parentGraph[from], edge)
			}
		}
	}
	findings = append(findings, cycleFindings(blockerGraph, "blocker-cycle")...)
	findings = append(findings, cycleFindings(parentGraph, "parent-cycle")...)
	sort.SliceStable(findings, func(i, j int) bool {
		if pathOrder(findings[i].Path) != pathOrder(findings[j].Path) {
			return pathOrder(findings[i].Path) < pathOrder(findings[j].Path)
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Code < findings[j].Code
	})
	return findings
}

func cycleFindings(graph map[itemKey][]graphEdge, code string) []source.Finding {
	state := map[itemKey]uint8{}
	var result []source.Finding
	var visit func(itemKey)
	visit = func(node itemKey) {
		state[node] = 1
		edges := graph[node]
		sort.SliceStable(edges, func(i, j int) bool {
			if edges[i].path != edges[j].path {
				return edges[i].path < edges[j].path
			}
			return edges[i].line < edges[j].line
		})
		for _, edge := range edges {
			switch state[edge.target] {
			case 0:
				visit(edge.target)
			case 1:
				result = append(result, source.Finding{Code: code, Severity: "error", Path: edge.path, Line: edge.line, Message: "reference graph contains a cycle"})
			}
		}
		state[node] = 2
	}
	nodes := make([]itemKey, 0, len(graph))
	for node := range graph {
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].source != nodes[j].source {
			return nodes[i].source < nodes[j].source
		}
		return nodes[i].id < nodes[j].id
	})
	for _, node := range nodes {
		if state[node] == 0 {
			visit(node)
		}
	}
	return result
}

func Check(ctx context.Context, sources []*Source, fix bool) ([]source.Finding, error) {
	if len(sources) == 0 {
		return nil, source.Refuse("invalid-source", "no native sources configured")
	}
	if fix {
		locks, err := lockSources(ctx, sources)
		if err != nil {
			return nil, err
		}
		defer unlockSources(locks)
		files, _, err := sourceFiles(sources)
		if err != nil {
			return nil, err
		}
		changes, err := checkFixes(files)
		if err != nil {
			return nil, err
		}
		if err := applyChanges(changes); err != nil {
			return nil, err
		}
	}
	files, parseFindings, err := sourceFiles(sources)
	if err != nil {
		return nil, err
	}
	return validateFiles(files, parseFindings), nil
}

func checkFixes(files []checkedFile) ([]fileChange, error) {
	var changes []fileChange
	for _, f := range files {
		data := bytes.Clone(f.data)
		_, parseErr := format.Parse(data)
		if parseErr != nil && format.ErrorCode(parseErr) == "merge-conflict" {
			fixed, changed := format.ResolveConflicts(data)
			if changed {
				_, afterErr := format.Parse(fixed)
				if afterErr == nil || format.ErrorCode(afterErr) == "merge-conflict" {
					// Supported hunks may be repaired while another hunk remains.
					data = fixed
				}
			}
		}
		doc, parseErr := format.Parse(data)
		if parseErr != nil {
			if !bytes.Equal(data, f.data) {
				changes = append(changes, fileChange{oldPath: f.path, newPath: f.path, data: data, original: f.data, mode: f.mode})
			}
			continue
		}
		if format.NoncanonicalLine(data) > 0 {
			data = format.NormalizeLineEndings(data)
			doc, parseErr = format.Parse(data)
			if parseErr != nil {
				return nil, fmt.Errorf("normalized %s is invalid: %w", f.path, parseErr)
			}
		}
		newPath := f.path
		if filepath.Base(f.path) != doc.Item().Ref.ID+".md" {
			candidate := filepath.Join(filepath.Dir(f.path), doc.Item().Ref.ID+".md")
			if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
				newPath = candidate
			}
		}
		if !bytes.Equal(data, f.data) || newPath != f.path {
			changes = append(changes, fileChange{oldPath: f.path, newPath: newPath, data: data, original: f.data, mode: f.mode})
		}
	}
	if err := preflightChanges(changes); err != nil {
		return nil, err
	}
	return changes, nil
}

func preflightChanges(changes []fileChange) error {
	sort.Slice(changes, func(i, j int) bool { return changes[i].oldPath < changes[j].oldPath })
	targets := map[string]string{}
	for _, change := range changes {
		if previous, ok := targets[change.newPath]; ok && previous != change.oldPath {
			return fmt.Errorf("multiple repairs target %s", change.newPath)
		}
		targets[change.newPath] = change.oldPath
		current, err := os.ReadFile(change.oldPath)
		if err != nil || !bytes.Equal(current, change.original) {
			return source.Refuse("conflict", "file changed while maintenance was being preflighted: "+change.oldPath)
		}
		if change.newPath != change.oldPath {
			if _, err := os.Lstat(change.newPath); !errors.Is(err, os.ErrNotExist) {
				return source.Refuse("conflict", "repair target exists or is inaccessible: "+change.newPath)
			}
		}
	}
	return nil
}

func stageFile(path string, data []byte, mode os.FileMode) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".bl-maintenance-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	cleanup := func(err error) (string, error) {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		return cleanup(err)
	}
	if _, err := file.Write(data); err != nil {
		return cleanup(err)
	}
	if err := file.Sync(); err != nil {
		return cleanup(err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func applyChanges(changes []fileChange) error {
	if len(changes) == 0 {
		return nil
	}
	staged := make([]string, len(changes))
	for i, change := range changes {
		name, err := stageFile(change.newPath, change.data, change.mode)
		if err != nil {
			for _, pending := range staged {
				if pending != "" {
					_ = os.Remove(pending)
				}
			}
			return fmt.Errorf("stage maintenance write %s: %w", change.newPath, err)
		}
		staged[i] = name
	}
	discardStaged := func() {
		for _, pending := range staged {
			if pending != "" {
				_ = os.Remove(pending)
			}
		}
	}
	committed := make([]bool, len(changes))
	for i, change := range changes {
		var err error
		linked := false
		if change.oldPath == change.newPath {
			err = os.Rename(staged[i], change.newPath)
		} else {
			err = os.Link(staged[i], change.newPath)
			linked = err == nil
			if linked {
				if removeErr := os.Remove(staged[i]); removeErr != nil {
					err = removeErr
				}
			}
		}
		if err != nil {
			discardStaged()
			if linked {
				_ = os.Remove(change.newPath)
			}
			return rollbackChanges(changes, committed, fmt.Errorf("write %s: %w", change.newPath, err))
		}
		staged[i] = ""
		committed[i] = true
		if change.oldPath != change.newPath {
			if err := os.Remove(change.oldPath); err != nil {
				discardStaged()
				return rollbackChanges(changes, committed, fmt.Errorf("remove renamed source %s: %w", change.oldPath, err))
			}
		}
		if err := syncDir(filepath.Dir(change.newPath)); err != nil {
			discardStaged()
			return rollbackChanges(changes, committed, fmt.Errorf("sync %s: %w", filepath.Dir(change.newPath), err))
		}
		if change.oldPath != change.newPath && filepath.Dir(change.oldPath) != filepath.Dir(change.newPath) {
			if err := syncDir(filepath.Dir(change.oldPath)); err != nil {
				discardStaged()
				return rollbackChanges(changes, committed, fmt.Errorf("sync %s: %w", filepath.Dir(change.oldPath), err))
			}
		}
	}
	return nil
}

func rollbackChanges(changes []fileChange, committed []bool, cause error) error {
	var rollback []string
	for i := len(changes) - 1; i >= 0; i-- {
		if !committed[i] {
			continue
		}
		change := changes[i]
		if change.oldPath == change.newPath {
			if err := atomicWrite(change.oldPath, change.original, change.mode); err != nil {
				rollback = append(rollback, change.oldPath+": "+err.Error())
			}
			continue
		}
		removeNew := true
		if _, err := os.Lstat(change.oldPath); errors.Is(err, os.ErrNotExist) {
			if err := atomicWrite(change.oldPath, change.original, change.mode); err != nil {
				rollback = append(rollback, change.oldPath+": "+err.Error())
				removeNew = false
			}
		}
		if removeNew {
			if err := os.Remove(change.newPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				rollback = append(rollback, change.newPath+": "+err.Error())
			}
		}
	}
	if len(rollback) > 0 {
		return fmt.Errorf("%w; rollback incomplete (%s)", cause, strings.Join(rollback, "; "))
	}
	return cause
}

var selectorID = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+(?:\.[0-9]+)*$`)

type RenumberResult struct {
	Source string `json:"source"`
	From   string `json:"from"`
	To     string `json:"to"`
	Files  int    `json:"files"`
}

type selectedItem struct {
	file checkedFile
}

func Renumber(ctx context.Context, sources []*Source, selector, to string) (RenumberResult, error) {
	if len(sources) == 0 {
		return RenumberResult{}, source.Refuse("invalid-source", "no native sources configured")
	}
	if !selectorID.MatchString(to) && !numericID(to) {
		return RenumberResult{}, source.Refuse("invalid-id", "TO must be an uppercase native ID or a number")
	}
	locks, err := lockSources(ctx, sources)
	if err != nil {
		return RenumberResult{}, err
	}
	defer unlockSources(locks)

	files, findings, err := sourceFiles(sources)
	if err != nil {
		return RenumberResult{}, err
	}
	if len(findings) > 0 {
		return RenumberResult{}, source.Refuse("rejected", "renumber requires every active and archived item to parse cleanly")
	}
	selected, err := selectItem(sources, files, selector)
	if err != nil {
		return RenumberResult{}, err
	}
	from := selected.file.item.Ref.ID
	selectedSource := selected.file.source
	if numericID(to) {
		to = selectedSource.options.Prefix + "-" + to
	}
	if canonical(from) == canonical(to) {
		return RenumberResult{}, source.Refuse("invalid-id", "FROM and TO resolve to the same ID")
	}

	for _, f := range files {
		if f.source == selectedSource && canonical(f.item.Ref.ID) == canonical(to) {
			return RenumberResult{}, source.Refuse("conflict", "target ID already exists: "+to)
		}
	}
	targetPath := filepath.Join(filepath.Dir(selected.file.path), to+".md")
	if _, err := os.Lstat(targetPath); !errors.Is(err, os.ErrNotExist) {
		return RenumberResult{}, source.Refuse("conflict", "target path exists or is inaccessible: "+targetPath)
	}

	now := selectedSource.options.Now().UTC().Truncate(time.Second)
	var changes []fileChange
	for _, f := range files {
		isSelected := f.path == selected.file.path
		next, changed, err := renumberDocument(f.doc, f.source.options.ID, selectedSource.options.ID, from, to, now, isSelected)
		if err != nil {
			return RenumberResult{}, fmt.Errorf("preflight %s: %w", f.path, err)
		}
		newPath := f.path
		if f.path == selected.file.path {
			newPath = targetPath
			changed = true
		}
		if changed {
			changes = append(changes, fileChange{oldPath: f.path, newPath: newPath, data: next.Bytes(), original: f.data, mode: f.mode})
		}
	}
	if len(changes) == 0 {
		return RenumberResult{}, fmt.Errorf("renumber produced no changes")
	}
	if err := preflightChanges(changes); err != nil {
		return RenumberResult{}, err
	}
	if err := applyChanges(changes); err != nil {
		return RenumberResult{}, err
	}
	return RenumberResult{Source: selectedSource.options.ID, From: from, To: to, Files: len(changes)}, nil
}

func selectorParts(sources []*Source, selector string) (*Source, string) {
	if prefix, rest, ok := strings.Cut(selector, ":"); ok {
		for _, s := range sources {
			if prefix == s.options.ID {
				return s, rest
			}
		}
	}
	return nil, selector
}

func selectItem(sources []*Source, files []checkedFile, selector string) (selectedItem, error) {
	chosenSource, value := selectorParts(sources, selector)
	isPath := strings.ContainsAny(value, `/\\`) || strings.HasSuffix(value, ".md")
	var matches []checkedFile
	if isPath {
		cleanPath := filepath.Clean(filepath.FromSlash(value))
		if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
			return selectedItem{}, source.Refuse("invalid-id", "path selector must stay within a source directory")
		}
		for _, f := range files {
			if chosenSource != nil && f.source != chosenSource {
				continue
			}
			rel, err := filepath.Rel(f.source.options.Path, f.path)
			if err == nil && filepath.Clean(rel) == cleanPath {
				matches = append(matches, f)
			}
		}
	} else {
		if !selectorID.MatchString(value) && !numericID(value) {
			return selectedItem{}, source.Refuse("invalid-id", "FROM must be an ID, a number, or a source-relative item path")
		}
		for _, f := range files {
			if chosenSource != nil && f.source != chosenSource {
				continue
			}
			want := value
			if numericID(value) {
				want = f.source.options.Prefix + "-" + value
			}
			if canonical(f.item.Ref.ID) == canonical(want) {
				matches = append(matches, f)
			}
		}
	}
	if len(matches) == 0 {
		return selectedItem{}, source.Refuse("not-found", "renumber source item not found: "+selector)
	}
	if len(matches) > 1 {
		candidates := make([]string, 0, len(matches))
		for _, f := range matches {
			rel, _ := filepath.Rel(f.source.options.Path, f.path)
			candidates = append(candidates, f.source.options.ID+":"+filepath.ToSlash(rel))
		}
		sort.Strings(candidates)
		return selectedItem{}, source.Refuse("ambiguous-id", "FROM matches multiple items; use one of: "+strings.Join(candidates, ", "))
	}
	return selectedItem{file: matches[0]}, nil
}

// numericID reports D19 numeric shorthand, expanded with a source's prefix.
func numericID(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func refString(ref model.Ref) string {
	if ref.Source == "" {
		return ref.ID
	}
	return ref.Source + ":" + ref.ID
}

func shouldRewrite(ref model.Ref, itemSource, targetSource, from string) bool {
	if canonical(ref.ID) != canonical(from) {
		return false
	}
	if ref.Source == "" {
		return itemSource == targetSource
	}
	return ref.Source == targetSource
}

func rewrittenRef(ref model.Ref, to string) model.Ref {
	ref.ID = to
	return ref
}

func renumberDocument(doc *format.Document, itemSource, targetSource, from, to string, now time.Time, renameID bool) (*format.Document, bool, error) {
	item := doc.Item()
	mutations := []model.Mutation{}
	if item.Parent != nil && shouldRewrite(*item.Parent, itemSource, targetSource, from) {
		mutations = append(mutations, model.Mutation{Op: model.Set, Field: "parent", Value: refString(rewrittenRef(*item.Parent, to))})
	}
	if item.DuplicateOf != nil && shouldRewrite(*item.DuplicateOf, itemSource, targetSource, from) {
		mutations = append(mutations, model.Mutation{Op: model.Set, Field: "duplicate_of", Value: refString(rewrittenRef(*item.DuplicateOf, to))})
	}
	for _, field := range []struct {
		name string
		refs []model.Ref
	}{{"blocked_by", item.BlockedBy}, {"related", item.Related}} {
		values := make([]string, 0, len(field.refs))
		changed := false
		for _, ref := range field.refs {
			if shouldRewrite(ref, itemSource, targetSource, from) {
				ref = rewrittenRef(ref, to)
				changed = true
			}
			values = append(values, refString(ref))
		}
		if changed {
			mutations = append(mutations, model.Mutation{Op: model.Set, Field: field.name, Value: values})
		}
	}
	current := doc
	changed := false
	for _, mutation := range mutations {
		next, _, err := current.Apply(mutation, now)
		if err != nil {
			return nil, false, err
		}
		current = next
		changed = true
	}
	if renameID {
		next, _, err := current.Apply(model.Mutation{Op: model.Set, Field: "id", Value: to}, now)
		if err != nil {
			return nil, false, err
		}
		current = next
		changed = true
	}
	return current, changed, nil
}
