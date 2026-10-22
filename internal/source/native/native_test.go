package native_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/native"
	"github.com/brettinternet/backlog/internal/testkit"
)

var now = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)

func openSource(t *testing.T, root, common string) *native.Source {
	t.Helper()
	s, err := native.New(native.Options{ID: "tasks", Prefix: "BL", Path: filepath.Join(root, "backlog"), CommonDir: common, ProjectID: "test-project", Now: func() time.Time { return now }, Statuses: map[string]model.StatusCategory{"todo": model.Todo, "done": model.Done}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func create(t *testing.T, s *native.Source) model.Item {
	t.Helper()
	item, err := s.Apply(context.Background(), model.Mutation{Op: model.Create, Item: &model.Item{Title: "An item", Status: "todo"}})
	if err != nil {
		t.Fatal(err)
	}
	return item
}
func reason(t *testing.T, err error, want string) {
	t.Helper()
	var e *source.Error
	if !errors.As(err, &e) || e.Reason != want {
		t.Fatalf("wanted %s, got %v", want, err)
	}
}
func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}
func fixture(t *testing.T, id string) []byte {
	t.Helper()
	data, err := format.Serialize(model.Item{Ref: model.Ref{ID: id}, Title: "Imported", Status: "todo", Created: now, Updated: now, Fields: map[string]any{"custom": "retained"}})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
}
func TestApplyIntegrity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	common := filepath.Join(root, "git-common")
	s := openSource(t, root, common)
	item := create(t, s)
	path := filepath.Join(root, "backlog", item.Ref.ID+".md")
	original := read(t, path)
	for _, version := range []string{"stale", ""} {
		_, err := s.Apply(context.Background(), model.Mutation{Ref: item.Ref, ExpectedVersion: version, Op: model.Set, Field: "title", Value: "Lost update"})
		reason(t, err, "conflict")
		if !bytes.Equal(original, read(t, path)) {
			t.Fatal("conflict changed file")
		}
	}
	changed, err := s.Apply(context.Background(), model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "title", Value: "Changed"})
	if err != nil || changed.Title != "Changed" || changed.Version == item.Version {
		t.Fatalf("edit: %#v %v", changed, err)
	}
	_, err = s.Apply(context.Background(), model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Set, Field: "title", Value: "Stale"})
	reason(t, err, "conflict")
	original = read(t, path)
	// A subprocess lowers its own file-size limit. The temp is created and the
	// write fails after partial output, without changing this test's limits.
	cmd := exec.Command(os.Args[0], "-test.run=^TestWriteFailureProcess$")
	cmd.Env = append(os.Environ(), "BL_TEST_FAIL_ROOT="+root, "BL_TEST_FAIL_COMMON="+common)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failure process: %v\n%s", err, out)
	}
	if !bytes.Equal(original, read(t, path)) {
		t.Fatal("failed write changed original")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".bl-write-") {
			t.Fatalf("leaked temp %s", e.Name())
		}
	}
}
func TestWriteFailureProcess(t *testing.T) {
	root := os.Getenv("BL_TEST_FAIL_ROOT")
	if root == "" {
		return
	}
	s := openSource(t, root, os.Getenv("BL_TEST_FAIL_COMMON"))
	item, err := s.Read(context.Background(), "BL-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 512, Max: 512}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Apply(context.Background(), model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.AppendNote, Text: strings.Repeat("a", 8192)})
	if !errors.Is(err, syscall.EFBIG) {
		t.Fatalf("expected file-size failure, got %v", err)
	}
}
func TestLoadAndArchive(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := openSource(t, root, filepath.Join(root, "git-common"))
	data := fixture(t, "BL-007.2")
	path := filepath.Join(root, "backlog", "BL-007.2.md")
	write(t, path, data)
	write(t, filepath.Join(root, "backlog", "README.md"), []byte("not an item"))
	write(t, filepath.Join(root, "backlog", ".hidden.md"), []byte("hidden"))
	write(t, filepath.Join(root, "backlog", "BL-8.md"), []byte("bad frontmatter"))
	write(t, filepath.Join(root, "backlog", "BL-9.md"), fixture(t, "BL-10"))
	write(t, filepath.Join(root, "backlog", "nested", "BL-999.md"), fixture(t, "BL-999"))
	write(t, filepath.Join(root, "backlog", "archive", "BL-020.md"), fixture(t, "BL-020"))
	delta, err := s.Load(context.Background(), source.Cursor{})
	if err != nil || delta.Coverage != source.Partial || len(delta.Items) != 1 || len(delta.Findings) != 2 {
		t.Fatalf("load: %#v %v", delta, err)
	}
	if delta.Findings[0].Code != "invalid-frontmatter" || delta.Findings[1].Code != "filename-mismatch" {
		t.Fatalf("finding codes: %#v", delta.Findings)
	}
	if delta.Items[0].Ref.Source != "tasks" || delta.Items[0].Category != model.Todo {
		t.Fatalf("source metadata: %#v", delta.Items[0])
	}
	delta, err = s.Load(context.Background(), source.Cursor{IncludeArchive: true})
	if err != nil || len(delta.Items) != 2 || len(delta.Findings) != 2 {
		t.Fatalf("archive load: %#v %v", delta, err)
	}
	item, err := s.Read(context.Background(), "BL-7.2")
	if err != nil {
		t.Fatal(err)
	}
	archived, err := s.Apply(context.Background(), model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Archive})
	if err != nil || archived.Version != item.Version {
		t.Fatalf("archive: %#v %v", archived, err)
	}
	archivePath := filepath.Join(root, "backlog", "archive", "BL-007.2.md")
	if !bytes.Equal(data, read(t, archivePath)) {
		t.Fatal("archive changed bytes")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active file exists: %v", err)
	}
	allocated := create(t, s)
	if allocated.Ref.ID != "BL-21" {
		t.Fatalf("reused imported/archive ID: %s", allocated.Ref.ID)
	}
	_, err = s.Apply(context.Background(), model.Mutation{Ref: item.Ref, ExpectedVersion: item.Version, Op: model.Unarchive})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, read(t, path)) {
		t.Fatal("unarchive changed bytes")
	}
	// Reconcile an imported archive ID even after the counter already exists.
	write(t, filepath.Join(root, "backlog", "archive", "BL-100.md"), fixture(t, "BL-100"))
	if got := create(t, s).Ref.ID; got != "BL-101" {
		t.Fatalf("counter missed import: %s", got)
	}
	// Duplicate natural IDs refuse reads rather than silently choosing a file.
	write(t, filepath.Join(root, "backlog", "BL-7.2.md"), fixture(t, "BL-7.2"))
	_, err = s.Read(context.Background(), "BL-7.2")
	reason(t, err, "duplicate-id")
}
func TestIndexedReadPreservesAliasesAndDuplicates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s, err := native.New(native.Options{
		ID: "tasks", Prefix: "BL", Path: filepath.Join(root, "backlog"),
		CommonDir: filepath.Join(root, "git-common"), ProjectID: "test-project",
		CacheDir: filepath.Join(root, "cache"), Now: func() time.Time { return now },
		Statuses: map[string]model.StatusCategory{"todo": model.Todo, "done": model.Done},
	})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "backlog", "BL-007.md"), fixture(t, "BL-007"))
	item, err := s.Read(context.Background(), "BL-7")
	if err != nil || item.Ref.ID != "BL-007" {
		t.Fatalf("canonical lookup: %#v %v", item, err)
	}

	archiveDuplicate := filepath.Join(root, "backlog", "archive", "BL-7.md")
	write(t, archiveDuplicate, fixture(t, "BL-7"))
	_, err = s.Read(context.Background(), "BL-7")
	reason(t, err, "duplicate-id")
	if err := os.Remove(archiveDuplicate); err != nil {
		t.Fatal(err)
	}
	item, err = s.Read(context.Background(), "BL-7")
	if err != nil || item.Ref.ID != "BL-007" {
		t.Fatalf("lookup after archive removal: %#v %v", item, err)
	}

	activeDuplicate := filepath.Join(root, "backlog", "BL-7.md")
	write(t, activeDuplicate, fixture(t, "BL-7"))
	_, err = s.Read(context.Background(), "BL-007")
	reason(t, err, "duplicate-id")
	if err := os.Remove(activeDuplicate); err != nil {
		t.Fatal(err)
	}
	item, err = s.Read(context.Background(), "BL-7")
	if err != nil || item.Ref.ID != "BL-007" {
		t.Fatalf("lookup after active removal: %#v %v", item, err)
	}
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
func TestMultiProcessWorktreeAllocation(t *testing.T) {
	testkit.Isolate(t)
	root := t.TempDir()
	git(t, root, "init", "-q")
	git(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
	// Exact-SHA detached worktrees are test fixtures, not agent branches.
	// Git setup/teardown is contained in the test's owned temporary directory.
	first, second := filepath.Join(root, "one"), filepath.Join(root, "two")
	git(t, root, "worktree", "add", "--detach", first, "HEAD")
	git(t, root, "worktree", "add", "--detach", second, "HEAD")
	t.Cleanup(func() {
		git(t, root, "worktree", "remove", "--force", first)
		git(t, root, "worktree", "remove", "--force", second)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	type process struct {
		cmd *exec.Cmd
		out bytes.Buffer
	}
	processes := make([]*process, 8)
	for i := range processes {
		checkout := first
		if i%2 != 0 {
			checkout = second
		}
		p := &process{}
		p.cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAllocationProcess$")
		p.cmd.Dir = checkout
		p.cmd.Env = append(os.Environ(), "BL_TEST_ALLOCATE=1")
		p.cmd.Stdout = &p.out
		p.cmd.Stderr = &p.out
		if err := p.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		processes[i] = p
	}
	for _, p := range processes {
		if err := p.cmd.Wait(); err != nil {
			t.Errorf("worker: %v\n%s", err, &p.out)
		}
	}
	if t.Failed() {
		return
	}
	s := openSource(t, root, filepath.Join(root, ".git"))
	delta, err := s.Load(ctx, source.Cursor{})
	if err != nil || len(delta.Items) != 400 || len(delta.Findings) != 0 {
		t.Fatalf("400 distinct items: %d findings %v error %v", len(delta.Items), delta.Findings, err)
	}
	seen := map[string]bool{}
	for _, item := range delta.Items {
		if seen[item.Ref.ID] {
			t.Fatalf("duplicate %s", item.Ref.ID)
		}
		seen[item.Ref.ID] = true
	}
	for i := 1; i <= 400; i++ {
		if !seen[fmt.Sprintf("BL-%d", i)] {
			t.Fatalf("missing ID %d", i)
		}
	}
}
func TestAllocationProcess(t *testing.T) {
	if os.Getenv("BL_TEST_ALLOCATE") == "" {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	d, err := project.Discover(cwd)
	if err != nil {
		t.Fatal(err)
	}
	s := openSource(t, d.ControlRoot, d.CommonDir)
	for i := 0; i < 50; i++ {
		create(t, s)
	}
}
func TestOutsideGitState(t *testing.T) {
	testkit.Isolate(t)
	root := t.TempDir()
	s := openSource(t, root, "")
	if got := create(t, s).Ref.ID; got != "BL-1" {
		t.Fatal(got)
	}
	// Independent directories for the same project share the reservation counter.
	other := openSource(t, t.TempDir(), "")
	if got := create(t, other).Ref.ID; got != "BL-2" {
		t.Fatal(got)
	}
}
