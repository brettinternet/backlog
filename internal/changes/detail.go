package changes

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/git"
)

// File statuses. Uncommitted marks a file a worktree changed but hasn't
// committed, and binary one Git can't count lines in.
const (
	FileAdded       = "added"
	FileModified    = "modified"
	FileDeleted     = "deleted"
	FileRenamed     = "renamed"
	FileBinary      = "binary"
	FileUncommitted = "uncommitted"
	// FileUnchanged is a file the change doesn't touch, which the Diff
	// shows for a tour step on it.
	FileUnchanged = "unchanged"
)

// File is one file a change touches, with From the old path of a rename.
// Patch is its unified diff hunks, read only when asked for, and Cut says why
// they are missing or partial.
type File struct {
	Path, From, Status   string
	Additions, Deletions int
	Patch, Cut           string
}

// Detail is what a change touches: its files and its commits, newest first.
// Reason says what it leaves out, such as a worktree's uncommitted files, and
// Patched that its files carry their patches.
type Detail struct {
	Files   []File
	Commits []Commit
	Reason  string
	Patched bool
}

const (
	// maxDetailCommits bounds the commits read for one change.
	maxDetailCommits = 1000
	// maxUntrackedBytes bounds how much of an untracked file is read to count
	// its lines.
	maxUntrackedBytes = 4 << 20
	// MaxPatchBytes bounds one file's patch.
	MaxPatchBytes = 128 << 10
)

// CommitChange is one commit in the checkout dir as a landed change, so its
// diff against its first parent can be read like a merge's.
func CommitChange(dir, sha string) Change {
	return Change{ID: "commit:" + sha, Stage: StageLanded, State: StateMerged, Head: sha, Commits: []Commit{{SHA: sha}}, dir: dir}
}

// LoadDetail reads one change's files and commits on a worker, with each
// file's patch if patch is set: from Git for landed commits or a local head,
// else from the forge serving its pull request, which always sends patches.
func LoadDetail(ctx context.Context, c Change, forges []Forge, patch bool) (Detail, error) {
	switch {
	case len(c.Commits) > 0 && c.dir != "":
		return landedDetail(ctx, c, patch)
	case c.ref != "" && c.base != "" && c.dir != "":
		files, reason, err := localFiles(ctx, c, patch)
		if err != nil {
			return Detail{}, err
		}
		commits, err := gitCommits(ctx, c.dir, c.base+".."+c.ref)
		return Detail{Files: files, Commits: commits, Reason: reason, Patched: patch}, err
	case c.Number > 0:
		if f := forgeFor(c, forges); f != nil {
			return f.Pull(ctx, c.Number)
		}
		return Detail{}, errors.New("no forge serves " + c.URL)
	}
	return Detail{}, errors.New("nothing to read for this change")
}

// landedDetail diffs each landed commit against its first parent and lists
// the commits it brought in, newest first.
func landedDetail(ctx context.Context, c Change, patch bool) (Detail, error) {
	d := Detail{Patched: patch}
	byPath := map[string]int{}
	for _, commit := range c.Commits {
		revisions := commit.SHA + "^1.." + commit.SHA
		files, err := gitFiles(ctx, c.dir, patch, commit.SHA+"^1", commit.SHA)
		if err != nil {
			// A root commit, such as a History commit, diffs against the empty tree.
			tree, ok := emptyTree(ctx, c.dir, commit.SHA)
			if !ok {
				return Detail{}, err
			}
			revisions = commit.SHA
			if files, err = gitFiles(ctx, c.dir, patch, tree, commit.SHA); err != nil {
				return Detail{}, err
			}
		}
		for _, f := range files {
			if i, ok := byPath[f.Path]; ok {
				d.Files[i].Additions += f.Additions
				d.Files[i].Deletions += f.Deletions
				d.Files[i].Patch, d.Files[i].Cut = TrimPatch(d.Files[i].Patch + f.Patch)
				d.Files[i].Cut = cmp.Or(d.Files[i].Cut, f.Cut)
				continue
			}
			byPath[f.Path] = len(d.Files)
			d.Files = append(d.Files, f)
		}
		commits, err := gitCommits(ctx, c.dir, revisions)
		if err != nil {
			return Detail{}, err
		}
		d.Commits = append(d.Commits, commits...)
	}
	slices.SortFunc(d.Files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return d, nil
}

// emptyTree is the repository's empty tree when sha is a root commit.
func emptyTree(ctx context.Context, dir, sha string) (string, bool) {
	if _, err := git.Run(ctx, dir, "rev-parse", "--verify", "--quiet", sha+"^1"); err == nil {
		return "", false
	}
	out, err := git.Run(ctx, dir, "hash-object", "-t", "tree", "/dev/null")
	return strings.TrimSpace(string(out)), err == nil
}

// localFiles diffs a branch against its merge base with the default branch.
// A worktree diffs its working tree instead, adding untracked files, unless
// repository config defines a filter, which reading the working tree would
// run.
func localFiles(ctx context.Context, c Change, patch bool) ([]File, string, error) {
	raw, err := git.Run(ctx, c.dir, "merge-base", c.base, c.ref)
	if err != nil {
		return nil, "", err
	}
	base := strings.TrimSpace(string(raw))
	if c.Worktree == "" {
		files, err := gitFiles(ctx, c.dir, patch, base, c.ref)
		return files, "", err
	}
	if filtered, err := git.RepoFilter(ctx, c.Worktree); err != nil || filtered {
		files, err := gitFiles(ctx, c.dir, patch, base, c.ref)
		return files, "uncommitted files skipped: repository config defines a filter", err
	}
	files, err := gitFiles(ctx, c.Worktree, patch, base)
	if err != nil {
		return nil, "", err
	}
	dirty, err := uncommitted(ctx, c.Worktree)
	if err != nil {
		return nil, "", err
	}
	byPath := map[string]int{}
	for i, f := range files {
		byPath[f.Path] = i
	}
	for path, untracked := range dirty {
		if i, ok := byPath[path]; ok {
			files[i].Status = FileUncommitted
		} else if untracked {
			files = append(files, untrackedFile(c.Worktree, path, patch))
		}
	}
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return files, "", nil
}

// Size sums files' added and deleted lines.
func Size(files []File) (additions, deletions int) {
	for _, f := range files {
		additions += f.Additions
		deletions += f.Deletions
	}
	return additions, deletions
}

// gitFiles diffs revs in dir, adding each file's patch if patch is set.
func gitFiles(ctx context.Context, dir string, patch bool, revs ...string) ([]File, error) {
	out, err := git.Run(ctx, dir, diffArgs(revs...)...)
	if err != nil || !patch {
		return parseDiff(out), err
	}
	files := parseDiff(out)
	args := []string{"diff", "--patch", "-M", "--no-color", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all", "--no-relative", "--src-prefix=a/", "--dst-prefix=b/"}
	out, err = git.Run(ctx, dir, append(append(args, revs...), "--")...)
	if err != nil {
		return nil, err
	}
	// Both runs list the same files in the same order, each patch opening
	// with its own diff --git line; paths come from the -z run, unquoted.
	patches := strings.Split(string(out), "\ndiff --git ")
	for i := range len(patches) - 1 {
		patches[i] += "\n"
	}
	if !strings.HasPrefix(string(out), "diff --git ") || len(patches) != len(files) {
		for i := range files {
			files[i].Cut = "diff unavailable"
		}
		return files, nil
	}
	for i, section := range patches {
		if at := strings.Index(section, "\n@@ "); at >= 0 {
			files[i].Patch, files[i].Cut = TrimPatch(section[at+1:])
		}
	}
	return files, nil
}

// TrimPatch cuts a patch to MaxPatchBytes at a line end, saying so.
func TrimPatch(patch string) (string, string) {
	if len(patch) <= MaxPatchBytes {
		return patch, ""
	}
	patch = patch[:MaxPatchBytes]
	return patch[:strings.LastIndexByte(patch, '\n')+1], "diff over 128 KiB cut"
}

func diffArgs(revs ...string) []string {
	args := []string{"diff", "--raw", "--numstat", "-z", "-M", "--no-color", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all"}
	return append(append(args, revs...), "--")
}

// parseDiff reads diff --raw --numstat -z: every raw entry, then the same
// entries' line counts in the same order.
func parseDiff(out []byte) []File {
	tokens := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	var files []File
	i := 0
	for ; i < len(tokens) && strings.HasPrefix(tokens[i], ":"); i++ {
		fields := strings.Fields(tokens[i])
		if len(fields) == 0 || i+1 >= len(tokens) {
			break
		}
		f := File{Status: FileModified}
		switch code := fields[len(fields)-1]; code[0] {
		case 'A':
			f.Status = FileAdded
		case 'D':
			f.Status = FileDeleted
		case 'R', 'C':
			if i+2 >= len(tokens) {
				return files
			}
			f.Status, f.From = FileRenamed, tokens[i+1]
			if code[0] == 'C' {
				f.Status = FileAdded
			}
			i++
		}
		i++
		f.Path = tokens[i]
		files = append(files, f)
	}
	for n := 0; n < len(files) && i < len(tokens); n++ {
		counts := strings.SplitN(tokens[i], "\t", 3)
		i++
		if len(counts) == 3 && counts[2] == "" {
			i += 2 // a rename's paths follow its counts
		}
		if len(counts) < 2 {
			continue
		}
		if counts[0] == "-" {
			files[n].Status = FileBinary
			continue
		}
		files[n].Additions, _ = strconv.Atoi(counts[0])
		files[n].Deletions, _ = strconv.Atoi(counts[1])
	}
	return files
}

// uncommitted lists a worktree's changed paths, true for untracked ones.
func uncommitted(ctx context.Context, worktree string) (map[string]bool, error) {
	out, err := git.Run(ctx, worktree, "status", "--porcelain", "-z", "--untracked-files=all", "--ignore-submodules=all", "--no-renames")
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for entry := range strings.SplitSeq(string(out), "\x00") {
		if len(entry) > 3 {
			paths[entry[3:]] = entry[:2] == "??"
		}
	}
	return paths, nil
}

// untrackedFile counts an untracked file's lines as Git would, reading only
// regular files and at most maxUntrackedBytes; a NUL in its first 8,000 bytes
// makes it binary. With patch set, its lines become one added hunk.
func untrackedFile(worktree, path string, patch bool) File {
	f := File{Path: path, Status: FileUncommitted}
	full := filepath.Join(worktree, filepath.FromSlash(path))
	if info, err := os.Lstat(full); err != nil || !info.Mode().IsRegular() {
		return f
	}
	file, err := os.Open(full)
	if err != nil {
		return f
	}
	defer file.Close()
	r := bufio.NewReaderSize(io.LimitReader(file, maxUntrackedBytes), 8000)
	head, _ := r.Peek(8000)
	if bytes.IndexByte(head, 0) >= 0 {
		f.Status = FileBinary
		return f
	}
	partial := false
	var body strings.Builder
	for {
		chunk, err := r.ReadSlice('\n')
		if len(chunk) > 0 {
			if patch && body.Len() < MaxPatchBytes {
				if !partial {
					body.WriteByte('+')
				}
				body.Write(chunk)
			}
			partial = chunk[len(chunk)-1] != '\n'
			if !partial {
				f.Additions++
			}
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			break
		}
	}
	if partial {
		f.Additions++
		body.WriteString("\n\\ No newline at end of file\n")
	}
	if patch && f.Additions > 0 {
		f.Patch, f.Cut = TrimPatch(fmt.Sprintf("@@ -0,0 +1,%d @@\n", f.Additions) + body.String())
	}
	return f
}

// gitCommits lists a revision range's commits, newest first.
func gitCommits(ctx context.Context, dir, revisions string) ([]Commit, error) {
	out, err := git.Run(ctx, dir, "log", "--no-color", fmt.Sprintf("--max-count=%d", maxDetailCommits), "--format=%H%x1f%cI%x1f%an%x1f%s", revisions, "--")
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		fields := strings.SplitN(line, "\x1f", 4)
		if len(fields) < 4 {
			continue
		}
		at, _ := time.Parse(time.RFC3339, fields[1])
		commits = append(commits, Commit{SHA: fields[0], Time: at.UTC(), Author: fields[2], Subject: fields[3]})
	}
	return commits, nil
}

// FileAt reads path as a change's Diff shows its new side, so a tour step
// outside every hunk can show its line: from the worktree, without leaving
// it, or else the head commit's blob, as when repository config defines a
// filter, at most maxUntrackedBytes either way.
func FileAt(ctx context.Context, c Change, path string) ([]byte, error) {
	if c.Worktree != "" && len(c.Commits) == 0 {
		if filtered, err := git.RepoFilter(ctx, c.Worktree); err == nil && !filtered {
			return worktreeFile(c.Worktree, path)
		}
	}
	if c.dir == "" || c.Head == "" {
		return nil, errors.New("no local copy of " + path)
	}
	object := c.Head + ":" + path
	size, err := git.Run(ctx, c.dir, "cat-file", "-s", object)
	if err != nil {
		return nil, err
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(size))); err != nil || n > maxUntrackedBytes {
		return nil, errors.New(path + " is too large to show")
	}
	return git.Run(ctx, c.dir, "cat-file", "blob", object)
}

func worktreeFile(worktree, path string) ([]byte, error) {
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// Stat first: opening a FIFO would block the worker.
	if info, err := root.Lstat(filepath.FromSlash(path)); err != nil || !info.Mode().IsRegular() {
		return nil, errors.New(path + " is not a regular file")
	}
	file, err := root.Open(filepath.FromSlash(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxUntrackedBytes+1))
	if err == nil && len(data) > maxUntrackedBytes {
		err = errors.New(path + " is too large to show")
	}
	return data, err
}
