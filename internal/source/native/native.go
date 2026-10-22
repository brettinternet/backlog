// Package native reads and writes native v1 item files.
package native

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
)

var idPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+(\.[0-9]+)*$`)
var prefixPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*$`)
var sourcePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

type Options struct {
	ID, Path, Prefix string
	// CommonDir is Discovery.CommonDir; Path must already use the control root.
	CommonDir, ProjectID string
	Statuses             map[string]model.StatusCategory
	Now                  func() time.Time
	CacheDir             string                       // empty disables the disposable cache
	ReadFile             func(string) ([]byte, error) // optional item-read instrumentation
}
type Source struct {
	options Options
	state   string
	loadMu  sync.Mutex
	loaded  map[string]loadedItem
}

var _ source.Source = (*Source)(nil)

func New(o Options) (*Source, error) {
	if !sourcePattern.MatchString(o.ID) || !prefixPattern.MatchString(o.Prefix) || !filepath.IsAbs(o.Path) {
		return nil, source.Refuse("invalid-source", "absolute path, safe source ID, and uppercase prefix required")
	}
	state := filepath.Join(o.CommonDir, "backlog")
	if o.CommonDir == "" {
		if o.ProjectID == "" {
			return nil, source.Refuse("invalid-source", "project identity required outside Git")
		}
		paths, err := project.UserPaths()
		if err != nil {
			return nil, err
		}
		state = filepath.Join(paths.StateDir, "projects", fmt.Sprintf("%x", sha256.Sum256([]byte(o.ProjectID))))
	}
	if o.ReadFile == nil {
		o.ReadFile = os.ReadFile
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	statuses := make(map[string]model.StatusCategory, len(o.Statuses))
	for k, v := range o.Statuses {
		statuses[k] = v
	}
	o.Statuses = statuses
	return &Source{options: o, state: state}, nil
}
func (s *Source) Info() source.Info {
	caps := model.Capabilities{}
	for _, op := range []model.Operation{model.Batch, model.Create, model.Archive, model.Unarchive, model.Set, model.Add, model.Remove, model.Unset, model.ReplaceSection, model.AppendNote, model.AppendComment, model.CheckCriterion, model.UncheckCriterion, model.AddCriterion, model.RemoveCriterion, model.Restore} {
		caps[op] = true
	}
	return source.Info{ID: s.options.ID, Kind: "native", Display: s.options.Path, Capabilities: caps}
}

func canonical(id string) string {
	prefix, number, _ := strings.Cut(id, "-")
	parts := strings.Split(number, ".")
	for i, p := range parts {
		n, _ := new(big.Int).SetString(p, 10)
		parts[i] = n.String()
	}
	return prefix + "-" + strings.Join(parts, ".")
}

// Only native ID filenames are items. Hidden files, symlinks and directories
// are not followed. A missing source directory is an empty source.
func itemPaths(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".md") && idPattern.MatchString(strings.TrimSuffix(entry.Name(), ".md")) {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	return paths, nil
}
func (s *Source) paths(archive bool) ([]string, error) {
	paths, err := itemPaths(s.options.Path)
	if err != nil || !archive {
		return paths, err
	}
	dir := filepath.Join(s.options.Path, "archive")
	if info, e := os.Lstat(dir); e == nil && !info.IsDir() {
		return nil, source.Refuse("invalid-path", "archive is not a directory")
	}
	archived, err := itemPaths(dir)
	return append(paths, archived...), err
}
func (s *Source) document(path string) (*format.Document, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, source.Refuse("invalid-path", "item is not a regular file")
	}
	data, err := s.options.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := format.Parse(data)
	if err != nil {
		return nil, err
	}
	item := doc.Item()
	if filepath.Base(path) != item.Ref.ID+".md" {
		return nil, source.Refuse("filename-mismatch", "filename does not match id")
	}
	if len(s.options.Statuses) > 0 {
		if _, ok := s.options.Statuses[item.Status]; !ok {
			return nil, source.Refuse("unknown-status", item.Status)
		}
	}
	return doc, nil
}
func (s *Source) item(doc *format.Document) model.Item {
	item := doc.Item()
	item.Ref.Source = s.options.ID
	item.Category = s.options.Statuses[item.Status]
	return item
}

// loadFinding classifies a per-file Load failure with a stable code.
func loadFinding(path string, err error) source.Finding {
	var refusal *source.Error
	var pathErr *fs.PathError
	f := source.Finding{Code: format.ErrorCode(err), Severity: "error", Path: path, Line: 1, Message: err.Error()}
	switch {
	case errors.As(err, &refusal):
		f.Code = refusal.Reason
	case errors.As(err, &pathErr):
		f.Code = "unreadable"
	default:
		f.Line = format.ErrorLine(err)
	}
	return f
}
func (s *Source) Load(ctx context.Context, cursor source.Cursor) (source.Delta, error) {
	if s.options.CacheDir == "" {
		return s.load(ctx, cursor)
	}
	store := cache.New(s.options.CacheDir, s.options.ProjectID, s.options.ID, s.options.Path)
	snapshot, warnings, err := store.Refresh(ctx, func() ([]string, error) { return s.paths(cursor.IncludeArchive) }, func(path string) bool { return cursor.IncludeArchive || filepath.Dir(path) == s.options.Path }, func(path string) (model.Item, error) {
		doc, e := s.document(path)
		if e != nil {
			return model.Item{}, e
		}
		return s.item(doc), nil
	})
	if err != nil {
		result, e := s.load(ctx, cursor)
		result.Findings = append(result.Findings, source.Finding{Code: "cache-unavailable", Severity: "warning", Message: err.Error()})
		return result, e
	}
	result := source.Delta{Coverage: source.Complete, Cursor: source.Cursor{IncludeArchive: cursor.IncludeArchive}, Items: make([]model.Item, 0, len(snapshot.Records))}
	byPath := map[string]model.Ref{}
	for _, record := range snapshot.Records {
		e := record.Err
		if e == nil && len(s.options.Statuses) > 0 {
			if _, ok := s.options.Statuses[record.Item.Status]; !ok {
				e = source.Refuse("unknown-status", record.Item.Status)
			}
		}
		if e != nil {
			result.Findings = append(result.Findings, loadFinding(record.Path, e))
			result.Coverage = source.Partial
			continue
		}
		item := *record.Item
		item.Category = s.options.Statuses[item.Status]
		result.Items = append(result.Items, item)
		byPath[record.Path] = item.Ref
	}
	for _, warning := range warnings {
		result.Findings = append(result.Findings, source.Finding{Code: "cache-rebuilt", Severity: "warning", Message: warning})
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Ref.ID < result.Items[j].Ref.ID })
	sort.Slice(result.Findings, func(i, j int) bool { return result.Findings[i].Path < result.Findings[j].Path })
	// Retain only the generation in the search closure, not the full cache
	// snapshot and its duplicate item records for the lifetime of the delta.
	generation := snapshot.Generation
	result.TextSearch = func(ctx context.Context, text string) (map[model.Ref]bool, error) {
		paths, e := store.Search(ctx, generation, text)
		if e != nil || paths == nil {
			return nil, e
		}
		matches := map[model.Ref]bool{}
		for path := range paths {
			if ref, ok := byPath[path]; ok {
				matches[ref] = true
			}
		}
		return matches, nil
	}
	return result, nil
}

type loadedItem struct {
	stat os.FileInfo
	item model.Item
}

func (s *Source) load(ctx context.Context, cursor source.Cursor) (source.Delta, error) {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	nextLoaded := map[string]loadedItem{}
	result := source.Delta{Coverage: source.Complete, Cursor: source.Cursor{IncludeArchive: cursor.IncludeArchive}}
	paths, err := s.paths(cursor.IncludeArchive)
	if err != nil {
		result.Coverage = source.Unknown
		return result, err
	}
	var mu sync.Mutex
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < min(runtime.GOMAXPROCS(0), len(paths)); i++ {
		wg.Go(func() {
			for path := range jobs {
				if ctx.Err() != nil {
					continue
				}
				stat, e := os.Lstat(path)
				var item model.Item
				if e == nil {
					old, ok := s.loaded[path]
					if ok && stat.Mode().IsRegular() && os.SameFile(stat, old.stat) && stat.Size() == old.stat.Size() && stat.ModTime() == old.stat.ModTime() && stat.Mode() == old.stat.Mode() {
						item = old.item
					} else {
						var doc *format.Document
						doc, e = s.document(path)
						if e == nil {
							item = s.item(doc)
						}
					}
				}
				mu.Lock()
				if e != nil {
					result.Findings = append(result.Findings, loadFinding(path, e))
					result.Coverage = source.Partial
				} else {
					result.Items = append(result.Items, item)
					nextLoaded[path] = loadedItem{stat, item}
				}
				mu.Unlock()
			}
		})
	}
	for _, path := range paths {
		select {
		case jobs <- path:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		result.Coverage = source.Partial
		return result, err
	}
	s.loaded = nextLoaded
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Ref.ID < result.Items[j].Ref.ID })
	sort.Slice(result.Findings, func(i, j int) bool { return result.Findings[i].Path < result.Findings[j].Path })
	return result, nil
}
func (s *Source) locate(ctx context.Context, id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", source.Refuse("invalid-id", id)
	}
	find := func(paths []string) (string, error) {
		found := ""
		for _, path := range paths {
			if canonical(strings.TrimSuffix(filepath.Base(path), ".md")) == canonical(id) {
				if found != "" {
					return "", source.Refuse("duplicate-id", id)
				}
				found = path
			}
		}
		if found == "" {
			return "", source.Refuse("not-found", id)
		}
		return found, nil
	}
	if s.options.CacheDir != "" {
		archive := filepath.Join(s.options.Path, "archive")
		if info, err := os.Lstat(archive); err == nil && !info.IsDir() {
			return "", source.Refuse("invalid-path", "archive is not a directory")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		store := cache.New(s.options.CacheDir, s.options.ProjectID, s.options.ID, s.options.Path)
		paths, err := store.FindPaths(ctx, []string{s.options.Path, archive}, canonical(id), func(name string) (string, bool) {
			if !strings.HasSuffix(name, ".md") {
				return "", false
			}
			fileID := strings.TrimSuffix(name, ".md")
			if !idPattern.MatchString(fileID) {
				return "", false
			}
			return canonical(fileID), true
		})
		// Trust the index only for one hit that still exists. A miss, duplicate, or
		// vanished path rechecks the directories, so a stale index (identity
		// unchanged on a coarse-timestamp filesystem) never decides the outcome.
		if err == nil {
			if path, e := find(paths); e == nil {
				if _, e = os.Stat(path); e == nil {
					return path, nil
				}
			}
		}
	}
	paths, err := s.paths(true)
	if err != nil {
		return "", err
	}
	return find(paths)
}
func (s *Source) Read(ctx context.Context, id string) (model.Item, error) {
	if err := ctx.Err(); err != nil {
		return model.Item{}, err
	}
	path, err := s.locate(ctx, id)
	if err != nil {
		return model.Item{}, err
	}
	doc, err := s.document(path)
	if err != nil {
		return model.Item{}, err
	}
	return s.item(doc), nil
}

// allocate persists the reservation before publishing an item. Failed creates
// may leave gaps, but no reservation is reused, including across worktrees.
func (s *Source) allocate() (string, error) {
	paths, err := s.paths(true)
	if err != nil {
		return "", err
	}
	next := big.NewInt(1)
	for _, path := range paths {
		id := strings.TrimSuffix(filepath.Base(path), ".md")
		prefix, rest, _ := strings.Cut(id, "-")
		if prefix != s.options.Prefix {
			continue
		}
		first, _, _ := strings.Cut(rest, ".")
		n, _ := new(big.Int).SetString(first, 10)
		n.Add(n, big.NewInt(1))
		if n.Cmp(next) > 0 {
			next = n
		}
	}
	counter := filepath.Join(s.state, "next", s.options.ID)
	data, err := os.ReadFile(counter)
	if err == nil {
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
	id := s.options.Prefix + "-" + next.String()
	next.Add(next, big.NewInt(1))
	if err := os.MkdirAll(filepath.Dir(counter), 0700); err != nil {
		return "", err
	}
	if err := atomicWrite(counter, []byte(next.String()+"\n"), 0600); err != nil {
		return "", err
	}
	return id, nil
}
func (s *Source) Apply(ctx context.Context, m model.Mutation) (model.Item, error) {
	fail := func(e error) (model.Item, error) { return model.Item{}, e }
	if m.Ref.Source != "" && m.Ref.Source != s.options.ID {
		return fail(source.Refuse("source-mismatch", m.Ref.Source))
	}
	if !s.Info().Capabilities.Allows(m.Op) {
		return fail(source.Refuse("unsupported", string(m.Op)))
	}
	lock, err := s.lock(ctx)
	if err != nil {
		return fail(err)
	}
	defer unlock(lock)
	if m.Op == model.Create {
		if m.Item == nil || m.Item.Ref.ID != "" || m.Ref.ID != "" {
			return fail(source.Refuse("invalid-create", "template without an ID required"))
		}
		item := *m.Item
		item.Created = s.options.Now().UTC().Truncate(time.Second)
		item.Updated = item.Created
		// Validate before reserving an ID.
		item.Ref = model.Ref{Source: s.options.ID, ID: s.options.Prefix + "-1"}
		if _, err := format.Serialize(item); err != nil {
			return fail(err)
		}
		if len(s.options.Statuses) > 0 {
			if _, ok := s.options.Statuses[item.Status]; !ok {
				return fail(source.Refuse("unknown-status", item.Status))
			}
		}
		id, err := s.allocate()
		if err != nil {
			return fail(err)
		}
		item.Ref.ID = id
		data, err := format.Serialize(item)
		if err != nil {
			return fail(err)
		}
		if err := os.MkdirAll(s.options.Path, 0755); err != nil {
			return fail(err)
		}
		path := filepath.Join(s.options.Path, id+".md")
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return fail(source.Refuse("conflict", "creation target exists or is inaccessible"))
		}
		if err := atomicWrite(path, data, 0644); err != nil {
			return fail(err)
		}
		doc, err := format.Parse(data)
		if err != nil {
			return fail(err)
		}
		return s.item(doc), nil
	}
	path, err := s.locate(ctx, m.Ref.ID)
	if err != nil {
		return fail(err)
	}
	doc, err := s.document(path)
	if err != nil {
		return fail(err)
	}
	if m.ExpectedVersion == "" || m.ExpectedVersion != doc.Version() {
		return fail(source.Refuse("conflict", "expected version does not match current content"))
	}
	if m.Op == model.Archive || m.Op == model.Unarchive {
		dir := s.options.Path
		if m.Op == model.Archive {
			dir = filepath.Join(dir, "archive")
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fail(err)
		}
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
			return fail(source.Refuse("invalid-path", "archive target is not a directory"))
		}
		target := filepath.Join(dir, filepath.Base(path))
		if target == path {
			return s.item(doc), nil
		}
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			return fail(source.Refuse("conflict", "archive target exists or is inaccessible"))
		}
		if err := os.Rename(path, target); err != nil {
			return fail(err)
		}
		if err := syncDir(dir); err != nil {
			return fail(err)
		}
		if err := syncDir(filepath.Dir(path)); err != nil {
			return fail(err)
		}
		return s.item(doc), nil
	}
	m.Ref.ID = doc.Item().Ref.ID // permit natural-ID lookup of padded imports
	next, _, err := doc.Apply(m, s.options.Now())
	if err != nil {
		return fail(err)
	}
	if next.Item().Ref.ID != doc.Item().Ref.ID {
		return fail(source.Refuse("invalid-value", "ID changes require renumber"))
	}
	if len(s.options.Statuses) > 0 {
		if _, ok := s.options.Statuses[next.Item().Status]; !ok {
			return fail(source.Refuse("unknown-status", next.Item().Status))
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return fail(err)
	}
	if err := atomicWrite(path, next.Bytes(), info.Mode()); err != nil {
		return fail(err)
	}
	return s.item(next), nil
}
