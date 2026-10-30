package changes

import (
	"bufio"
	"bytes"
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
)

// File is one file a change touches, with From the old path of a rename.
type File struct {
	Path, From, Status   string
	Additions, Deletions int
}

// Detail is what a change touches: its files and its commits, newest first.
// Reason says what it leaves out, such as a worktree's uncommitted files.
type Detail struct {
	Files   []File
	Commits []Commit
	Reason  string
}

const (
	// maxDetailCommits bounds the commits read for one change.
	maxDetailCommits = 1000
	// maxUntrackedBytes bounds how much of an untracked file is read to count
	// its lines.
	maxUntrackedBytes = 4 << 20
)

// LoadDetail reads one change's files and commits on a worker: from Git for
// landed commits or a local head, else from the forge serving its pull
// request.
func LoadDetail(ctx context.Context, c Change, forges []Forge) (Detail, error) {
	switch {
	case len(c.Commits) > 0 && c.dir != "":
		return landedDetail(ctx, c)
	case c.ref != "" && c.base != "" && c.dir != "":
		files, reason, err := localFiles(ctx, c)
		if err != nil {
			return Detail{}, err
		}
		commits, err := gitCommits(ctx, c.dir, c.base+".."+c.ref)
		return Detail{Files: files, Commits: commits, Reason: reason}, err
	case c.Number > 0:
		for _, f := range forges {
			if strings.HasPrefix(strings.ToLower(c.URL), strings.ToLower(f.Web())+"/pull/") {
				return f.Pull(ctx, c.Number)
			}
		}
		return Detail{}, errors.New("no forge serves " + c.URL)
	}
	return Detail{}, errors.New("nothing to read for this change")
}

// landedDetail diffs each landed commit against its first parent and lists
// the commits it brought in, newest first.
func landedDetail(ctx context.Context, c Change) (Detail, error) {
	var d Detail
	byPath := map[string]int{}
	for _, commit := range c.Commits {
		out, err := git.Run(ctx, c.dir, diffArgs(commit.SHA+"^1", commit.SHA)...)
		if err != nil {
			return Detail{}, err
		}
		for _, f := range parseDiff(out) {
			if i, ok := byPath[f.Path]; ok {
				d.Files[i].Additions += f.Additions
				d.Files[i].Deletions += f.Deletions
				continue
			}
			byPath[f.Path] = len(d.Files)
			d.Files = append(d.Files, f)
		}
		commits, err := gitCommits(ctx, c.dir, commit.SHA+"^1.."+commit.SHA)
		if err != nil {
			return Detail{}, err
		}
		d.Commits = append(d.Commits, commits...)
	}
	slices.SortFunc(d.Files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return d, nil
}

// localFiles diffs a branch against its merge base with the default branch.
// A worktree diffs its working tree instead, adding untracked files, unless
// repository config defines a filter, which reading the working tree would
// run.
func localFiles(ctx context.Context, c Change) ([]File, string, error) {
	raw, err := git.Run(ctx, c.dir, "merge-base", c.base, c.ref)
	if err != nil {
		return nil, "", err
	}
	base := strings.TrimSpace(string(raw))
	if c.Worktree == "" {
		out, err := git.Run(ctx, c.dir, diffArgs(base, c.ref)...)
		return parseDiff(out), "", err
	}
	if filtered, err := git.RepoFilter(ctx, c.Worktree); err != nil || filtered {
		out, err := git.Run(ctx, c.dir, diffArgs(base, c.ref)...)
		return parseDiff(out), "uncommitted files skipped: repository config defines a filter", err
	}
	out, err := git.Run(ctx, c.Worktree, diffArgs(base)...)
	if err != nil {
		return nil, "", err
	}
	files := parseDiff(out)
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
			files = append(files, untrackedFile(c.Worktree, path))
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
// makes it binary.
func untrackedFile(worktree, path string) File {
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
	r := bufio.NewReader(io.LimitReader(file, maxUntrackedBytes))
	head, _ := r.Peek(8000)
	if bytes.IndexByte(head, 0) >= 0 {
		f.Status = FileBinary
		return f
	}
	partial := false
	for {
		chunk, err := r.ReadSlice('\n')
		if len(chunk) > 0 {
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
