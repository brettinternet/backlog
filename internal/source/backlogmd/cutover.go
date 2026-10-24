package backlogmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// cutover replaces a Backlog.md project with its native import in the same
// directory. The removed files must be committed in Git, so the change can be
// reviewed and reverted like any other.
type cutover struct {
	plan    outputPlan
	project string
	remove  []string // source files, links, and config
	prune   []string // partition directories, removed only when empty
}

func planCutover(ctx context.Context, source sourceConfig, plan outputPlan, candidates []sourceCandidate) (cutover, error) {
	c := cutover{plan: plan, project: source.projectRoot}
	if len(candidates) == 0 {
		return c, fmt.Errorf("no Backlog.md task or milestone files found under %s; run from the repository root or pass the Backlog.md folder", source.root)
	}
	for _, name := range []string{"backlog.yaml", "backlog.local.yaml"} {
		if _, err := os.Lstat(filepath.Join(c.project, name)); !errors.Is(err, fs.ErrNotExist) {
			return c, fmt.Errorf("%s already exists in %s; an in-place import needs a project without native config", name, c.project)
		}
	}
	for _, candidate := range candidates {
		c.remove = append(c.remove, candidate.path)
	}
	if source.configPath != "" {
		c.remove = append(c.remove, source.configPath)
	}
	pathspecs := []string{}
	for _, path := range append([]string{source.root}, c.remove...) {
		relative, err := filepath.Rel(c.project, path)
		if err != nil || !within(c.project, path) {
			return c, fmt.Errorf("%s is outside the project %s", path, c.project)
		}
		pathspecs = append(pathspecs, relative)
	}
	output, err := exec.CommandContext(ctx, "git", append([]string{"--literal-pathspecs", "-C", c.project, "status", "--porcelain", "--untracked-files=all", "--"}, pathspecs...)...).CombinedOutput()
	if err != nil {
		return c, fmt.Errorf("an in-place import needs %s to be a Git checkout, so the removed Backlog.md files stay recoverable: %s", c.project, strings.TrimSpace(string(output)))
	}
	if changes := strings.TrimSpace(string(output)); changes != "" {
		return c, fmt.Errorf("commit or stash Backlog.md changes before an in-place import:\n%s", changes)
	}
	// Status is silent for ignored files and paths behind a symlink or in a
	// nested repository; only files in the index can be restored.
	if output, err := exec.CommandContext(ctx, "git", append([]string{"--literal-pathspecs", "-C", c.project, "ls-files", "--error-unmatch", "--"}, pathspecs[1:]...)...).CombinedOutput(); err != nil {
		return c, fmt.Errorf("an in-place import removes only files committed to %s; commit these Backlog.md files first, or import with --to:\n%s", c.project, strings.TrimSpace(string(output)))
	}
	for _, relative := range plan.paths {
		if _, err := os.Lstat(filepath.Join(c.project, relative)); !errors.Is(err, fs.ErrNotExist) {
			return c, fmt.Errorf("%s already exists; move it aside before an in-place import", relative)
		}
	}
	// Parents precede children; apply prunes in reverse.
	c.prune = []string{source.root}
	for _, partition := range []string{"tasks", "completed", "drafts", "milestones", "archive"} {
		_ = filepath.WalkDir(filepath.Join(source.root, partition), func(path string, entry fs.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				c.prune = append(c.prune, path)
			}
			return nil
		})
	}
	return c, nil
}

func (c cutover) removedPaths() []string {
	paths := make([]string, 0, len(c.remove))
	for _, path := range c.remove {
		relative, _ := filepath.Rel(c.project, path)
		paths = append(paths, filepath.ToSlash(relative))
	}
	slices.Sort(paths)
	return paths
}

// apply writes every native file without replacing anything, undoing the
// writes if one fails, and only then removes the source files.
func (c cutover) apply(ctx context.Context) error {
	var created []string
	undo := func() {
		for _, path := range slices.Backward(created) {
			_ = os.Remove(path)
		}
	}
	for _, relative := range c.plan.paths {
		if err := ctx.Err(); err != nil {
			undo()
			return err
		}
		path := filepath.Join(c.project, relative)
		var parents []string
		for dir := filepath.Dir(path); !isDir(dir); dir = filepath.Dir(dir) {
			parents = append(parents, dir)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			undo()
			return fmt.Errorf("create %s: %w", filepath.Dir(relative), err)
		}
		for _, dir := range slices.Backward(parents) {
			created = append(created, dir)
		}
		if err := writeNew(path, c.plan.files[relative]); err != nil {
			undo()
			return fmt.Errorf("write %s: %w", relative, err)
		}
		created = append(created, path)
	}
	for _, path := range c.remove {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("the native project is written, but removing %s failed: %w; remove the remaining Backlog.md files, or restore them with git and remove the native files", path, err)
		}
	}
	for _, dir := range slices.Backward(c.prune) {
		if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
			_ = os.Remove(dir)
		}
	}
	return nil
}

func writeNew(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
