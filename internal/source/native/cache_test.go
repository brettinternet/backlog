package native_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
)

func cachedSource(t *testing.T, root string, readFile func(string) ([]byte, error)) *native.Source {
	t.Helper()
	s, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: filepath.Join(root, "backlog"), CommonDir: filepath.Join(root, "common"), ProjectID: "cache-test", CacheDir: filepath.Join(root, "cache"), ReadFile: readFile, Statuses: map[string]model.StatusCategory{"todo": model.Todo, "done": model.Done}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func cachedLoad(t *testing.T, s *native.Source) source.Delta {
	t.Helper()
	d, err := s.Load(context.Background(), source.Cursor{IncludeArchive: true})
	if err != nil || d.Coverage != source.Complete {
		t.Fatalf("load: %+v %v", d, err)
	}
	return d
}
func cachePath(root string) string {
	return cache.New(filepath.Join(root, "cache"), "cache-test", "tasks", filepath.Join(root, "backlog")).Path
}

// Catches a full filename-index rebuild after every atomic item replacement.
func TestFilenameIndexSurvivesAtomicReplacement(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "backlog", "BL-1.md")
	write(t, path, fixture(t, "BL-1"))
	s := cachedSource(t, root, nil)
	item, err := s.Read(context.Background(), "BL-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background(), model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "title", Value: "Replaced atomically"}); err != nil {
		t.Fatal(err)
	}
	store := cache.New(filepath.Join(root, "cache"), "cache-test", "tasks", filepath.Join(root, "backlog"))
	identified := 0
	paths, err := store.FindPaths(context.Background(), []string{filepath.Dir(path), filepath.Join(root, "backlog", "archive")}, "BL-1", func(name string) (string, bool) {
		identified++
		return strings.TrimSuffix(name, ".md"), strings.HasSuffix(name, ".md")
	})
	if err != nil || !reflect.DeepEqual(paths, []string{path}) || identified != 0 {
		t.Fatalf("lookup after replacement: paths=%v identified=%d err=%v", paths, identified, err)
	}
}

// Catches warm reparsing and lossy serialization of raw fields/body metadata.
func TestCacheWarmAndChanges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "backlog", "BL-1.md")
	data := fixture(t, "BL-1")
	data = bytes.Replace(data, []byte("custom: retained"), []byte("custom: [retained, 42, true]\r\nextra: {nested: value}"), 1)
	write(t, path, data)
	// Keep the timestamp in one second, then restore it after a same-size edit.
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int64
	s := cachedSource(t, root, func(path string) ([]byte, error) { reads.Add(1); return os.ReadFile(path) })
	cold := cachedLoad(t, s)
	if reads.Load() != 1 {
		t.Fatalf("cold reads: %d", reads.Load())
	}
	reads.Store(0)
	warm := cachedLoad(t, s)
	if reads.Load() != 0 || !reflect.DeepEqual(cold.Items, warm.Items) {
		t.Fatalf("warm reads=%d; items equal=%t", reads.Load(), reflect.DeepEqual(cold.Items, warm.Items))
	}
	assertFresh := func(wantReads int64) source.Delta {
		t.Helper()
		reads.Store(0)
		got := cachedLoad(t, s)
		direct, err := openSource(t, root, filepath.Join(root, "common")).Load(context.Background(), source.Cursor{IncludeArchive: true})
		if err != nil || !reflect.DeepEqual(got.Items, direct.Items) || reads.Load() != wantReads {
			t.Fatalf("fresh reads=%d want=%d equal=%t err=%v", reads.Load(), wantReads, reflect.DeepEqual(got.Items, direct.Items), err)
		}
		return got
	}
	write(t, path, bytes.Replace(data, []byte("Imported"), []byte("Modified"), 1))
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	if got := assertFresh(1); got.Items[0].Title != "Modified" {
		t.Fatal("same-size edit missed")
	}
	added := filepath.Join(root, "backlog", "BL-2.md")
	write(t, added, fixture(t, "BL-2"))
	assertFresh(1)
	archive := filepath.Join(root, "backlog", "archive", "BL-2.md")
	if err := os.MkdirAll(filepath.Dir(archive), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(added, archive); err != nil {
		t.Fatal(err)
	}
	assertFresh(1)
	reads.Store(0)
	active, err := s.Load(context.Background(), source.Cursor{})
	if err != nil || len(active.Items) != 1 || reads.Load() != 0 {
		t.Fatalf("active view: %+v reads=%d err=%v", active, reads.Load(), err)
	}
	// Switching views must share rows and the refresh lock, not parse twice.
	assertFresh(0)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := assertFresh(0); len(got.Items) != 1 || got.Items[0].Ref.ID != "BL-2" {
		t.Fatal("deletion missed")
	}
	// A failed parse must evict the item from the batch snapshot, and repair
	// must be visible on the next load rather than reusing its previous value.
	write(t, archive, []byte("invalid item"))
	partial, err := s.Load(context.Background(), source.Cursor{IncludeArchive: true})
	if err != nil || partial.Coverage != source.Partial || len(partial.Items) != 0 {
		t.Fatalf("invalid cached item: %+v err=%v", partial, err)
	}
	write(t, archive, fixture(t, "BL-2"))
	assertFresh(1)
	assertFresh(0)
	// A warm item must be interpreted against current configuration, not the
	// category that was in effect when it was cached.
	changed, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: filepath.Join(root, "backlog"), CommonDir: filepath.Join(root, "common"), ProjectID: "cache-test", CacheDir: filepath.Join(root, "cache"), Statuses: map[string]model.StatusCategory{"todo": model.Active}})
	if err != nil {
		t.Fatal(err)
	}
	if got := cachedLoad(t, changed); got.Items[0].Category != model.Active {
		t.Fatal("cached stale category")
	}
}

// Catches concurrent processes both reparsing the same changed file or leaking
// SQLite busy errors instead of joining the source refresh lock.
func TestCacheSingleFlight(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "backlog", "BL-1.md")
	write(t, path, fixture(t, "BL-1"))
	cachedLoad(t, cachedSource(t, root, nil))
	write(t, path, bytes.Replace(fixture(t, "BL-1"), []byte("Imported"), []byte("Modified"), 1))
	type child struct {
		cmd    *exec.Cmd
		gate   *os.File
		output bytes.Buffer
	}
	children := make([]*child, 2)
	for i := range children {
		input, gate, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		c := &child{cmd: exec.Command(os.Args[0], "-test.run=^TestCacheRefreshProcess$"), gate: gate}
		c.cmd.Env = append(os.Environ(), "BL_CACHE_TEST_ROOT="+root)
		c.cmd.Stdin = input
		c.cmd.Stdout = &c.output
		c.cmd.Stderr = &c.output
		if err = c.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_ = input.Close()
		children[i] = c
	}
	for _, c := range children {
		_, err := c.gate.Write([]byte("x"))
		_ = c.gate.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range children {
		if err := c.cmd.Wait(); err != nil {
			t.Fatalf("child: %v\n%s", err, c.output.String())
		}
	}
	parsed := read(t, filepath.Join(root, "parses"))
	if strings.Count(string(parsed), "BL-1.md\n") != 1 {
		t.Fatalf("expected one parse across both processes: %q", parsed)
	}
}
func TestCacheRefreshProcess(t *testing.T) {
	root := os.Getenv("BL_CACHE_TEST_ROOT")
	if root == "" {
		return
	}
	if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	s := cachedSource(t, root, func(path string) ([]byte, error) {
		log, err := os.OpenFile(filepath.Join(root, "parses"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return nil, err
		}
		_, err = fmt.Fprintln(log, filepath.Base(path))
		_ = log.Close()
		if err != nil {
			return nil, err
		}
		return os.ReadFile(path)
	})
	d := cachedLoad(t, s)
	if len(d.Findings) != 0 || len(d.Items) != 1 || d.Items[0].Title != "Modified" {
		t.Fatalf("child result: %+v", d)
	}
}

// Catches cache damage preventing otherwise valid source files from loading.
func TestCacheRecovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, filepath.Join(root, "backlog", "BL-1.md"), fixture(t, "BL-1"))
	s := cachedSource(t, root, nil)
	original := cachedLoad(t, s)
	for _, kind := range []string{"corrupt", "schema"} {
		if kind == "corrupt" {
			write(t, cachePath(root), []byte("not a SQLite database"))
		} else {
			db, err := sql.Open("sqlite", cachePath(root))
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`PRAGMA user_version=999`)
			_ = db.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		got := cachedLoad(t, s)
		if !reflect.DeepEqual(original.Items, got.Items) || len(got.Findings) != 1 || got.Findings[0].Code != "cache-rebuilt" {
			t.Fatalf("%s recovery: %+v", kind, got)
		}
	}
	// Rows written by another binary may reflect an older parser and must be
	// reparsed silently, even though their stat identities still match.
	db, err := sql.Open("sqlite", cachePath(root))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE metadata SET build='older-binary'`)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int64
	counted := cachedSource(t, root, func(path string) ([]byte, error) { reads.Add(1); return os.ReadFile(path) })
	if got := cachedLoad(t, counted); reads.Load() != 1 || len(got.Findings) != 0 {
		t.Fatalf("other binary rows reused: reads=%d %+v", reads.Load(), got)
	}
	if cachedLoad(t, counted); reads.Load() != 1 {
		t.Fatalf("same binary rows not reused: reads=%d", reads.Load())
	}
	// A cache location that cannot be opened must also leave source loading usable.
	blocked := filepath.Join(root, "blocked")
	write(t, blocked, []byte("file"))
	fallback, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: filepath.Join(root, "backlog"), CommonDir: filepath.Join(root, "common"), ProjectID: "cache-test", CacheDir: blocked, Statuses: map[string]model.StatusCategory{"todo": model.Todo}})
	if err != nil {
		t.Fatal(err)
	}
	got := cachedLoad(t, fallback)
	if !reflect.DeepEqual(original.Items, got.Items) || len(got.Findings) != 1 || got.Findings[0].Code != "cache-unavailable" {
		t.Fatalf("fallback: %+v", got)
	}
}

// Catches FTS token/phrase semantics silently changing literal substring search,
// including Unicode folding, punctuation, negation, and body section boundaries.
func TestCacheTextParity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	data := fixture(t, "BL-1")
	data = append(data, []byte("\r\n## Description\r\n\r\nHello WORLD: café 日本語 a*b \"quotes\"\r\n\r\n## Plan\r\n\r\nStraße İSTANBUL\r\n\r\n## Acceptance criteria\r\n\r\n- [ ] Criterion phrase\r\n")...)
	write(t, filepath.Join(root, "backlog", "BL-1.md"), data)
	write(t, filepath.Join(root, "backlog", "BL-2.md"), fixture(t, "BL-2"))
	s := cachedSource(t, root, nil)
	d := cachedLoad(t, s)
	indexed := 0
	for _, text := range []string{"port", "WORLD:", "world: café", "日本語", "a*b", `"quotes"`, "İSTANBUL", "Straße", "criterion phrase", "missing", "ed\nHello", "a", "日本"} {
		matches, err := d.TextSearch(context.Background(), text)
		if err != nil {
			t.Fatalf("search %q: %v", text, err)
		}
		if matches != nil {
			indexed++
		}
		for _, negated := range []bool{false, true} {
			q := query.Query{Terms: []query.Term{{Field: "text", Negated: negated, Values: []query.Value{{Text: text}}}}}
			plain := query.Snapshot{Items: d.Items}
			accelerated := plain
			if matches != nil {
				accelerated.TextMatches = map[string]map[model.Ref]bool{text: matches}
			}
			for _, item := range d.Items {
				if q.Match(item, plain, nil) != q.Match(item, accelerated, nil) {
					t.Fatalf("FTS mismatch for %q negated=%t id=%s candidates=%v", text, negated, item.Ref.ID, matches)
				}
			}
		}
	}
	if indexed < 10 {
		t.Fatalf("index was bypassed: %d indexed searches", indexed)
	}
	cachedLoad(t, s)
	if _, err := d.TextSearch(context.Background(), "world"); err == nil {
		t.Fatal("stale generation accepted")
	}
}
