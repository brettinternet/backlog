package project

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type DetectedSource struct {
	Type string `json:"type"`
	Path string `json:"path"`
}
type Discovery struct {
	Directory    string           `json:"directory"`
	CheckoutRoot string           `json:"checkoutRoot,omitempty"`
	ControlRoot  string           `json:"controlRoot,omitempty"`
	GitDir       string           `json:"gitDir,omitempty"`
	CommonDir    string           `json:"commonDir,omitempty"`
	Warnings     []string         `json:"warnings,omitempty"`
	Detected     []DetectedSource `json:"detected,omitempty"`
}

// ProjectFile and LocalFile are the shared and private repository layers.
func ProjectFile(root string) string { return filepath.Join(root, "backlog.yaml") }
func LocalFile(root string) string   { return filepath.Join(root, "backlog.local.yaml") }

func rejectLegacyConfig(root string) error {
	if isFile(filepath.Join(root, ".config", "backlog.yaml")) {
		return fmt.Errorf("%s: legacy project config; run git mv .config/backlog.yaml backlog.yaml", root)
	}
	return nil
}

// Discover reads Git's on-disk layout, including linked worktrees, without
// starting git. Paths are absolute and symlinks are resolved once at entry.
func Discover(directory string) (Discovery, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return Discovery{}, err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return Discovery{}, err
	}
	d := Discovery{Directory: absolute}
	for root := absolute; ; root = filepath.Dir(root) {
		marker := filepath.Join(root, ".git")
		info, statErr := os.Stat(marker)
		if statErr == nil {
			d.CheckoutRoot = root
			if info.IsDir() {
				d.GitDir = marker
			} else {
				data, readErr := os.ReadFile(marker)
				if readErr != nil {
					return d, readErr
				}
				text := strings.TrimSpace(string(data))
				if !strings.HasPrefix(text, "gitdir: ") {
					return d, fmt.Errorf("%s: invalid gitdir file", marker)
				}
				d.GitDir = resolvePath(root, strings.TrimSpace(strings.TrimPrefix(text, "gitdir: ")))
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return d, statErr
		}
		// A bare Git directory has HEAD, objects, and config but no .git marker.
		if isFile(filepath.Join(root, "HEAD")) && isDir(filepath.Join(root, "objects")) && isFile(filepath.Join(root, "config")) {
			d.CheckoutRoot = root
			d.GitDir = root
			break
		}
		if parent := filepath.Dir(root); parent == root {
			return d, nil
		}
	}
	d.CommonDir = d.GitDir
	if data, readErr := os.ReadFile(filepath.Join(d.GitDir, "commondir")); readErr == nil {
		d.CommonDir = resolvePath(d.GitDir, strings.TrimSpace(string(data)))
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return d, readErr
	}
	d.CommonDir, err = filepath.EvalSymlinks(d.CommonDir)
	if err != nil {
		return d, err
	}
	d.ControlRoot = filepath.Dir(d.CommonDir)
	bare, err := gitConfigValue(filepath.Join(d.CommonDir, "config"), "core", "", "bare")
	if err != nil {
		return d, err
	}
	if strings.EqualFold(bare, "true") || filepath.Base(d.CommonDir) != ".git" {
		// Separate-git-dir repositories keep the primary working tree in core.worktree.
		worktree, readErr := gitConfigValue(filepath.Join(d.CommonDir, "config"), "core", "", "worktree")
		if readErr != nil {
			return d, readErr
		}
		if worktree != "" && !strings.EqualFold(bare, "true") {
			d.ControlRoot = resolvePath(d.CommonDir, worktree)
		} else {
			d.ControlRoot = d.CheckoutRoot
			d.Warnings = append(d.Warnings, "no primary worktree; using invoking checkout as control root")
		}
	}
	// root: checkout may select either checkout, so neither may keep the old path.
	for _, root := range []string{d.ControlRoot, d.CheckoutRoot} {
		if err := rejectLegacyConfig(root); err != nil {
			return d, err
		}
	}
	d.Detected = detectSources(d.ControlRoot)
	return d, nil
}
func resolvePath(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(base, path))
}
func isFile(path string) bool { info, err := os.Stat(path); return err == nil && !info.IsDir() }
func isDir(path string) bool  { info, err := os.Stat(path); return err == nil && info.IsDir() }
func detectSources(root string) []DetectedSource {
	if root == "" || isFile(ProjectFile(root)) || isFile(LocalFile(root)) {
		return nil
	}
	var result []DetectedSource
	for _, path := range []string{"backlog/config.yml", "backlog.config.yml"} {
		if isFile(filepath.Join(root, path)) {
			result = append(result, DetectedSource{Type: "backlog-md", Path: filepath.Join(root, path)})
		}
	}
	if isDir(filepath.Join(root, ".beads")) {
		result = append(result, DetectedSource{Type: "beads", Path: filepath.Join(root, ".beads")})
	}
	return result
}

// NormalizeRemote removes transport, credentials, port, and the .git suffix.
// Host names are case-insensitive; repository path case is preserved.
func NormalizeRemote(remote string) (string, error) {
	raw := strings.TrimSpace(remote)
	var host, path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("invalid origin URL: %w", err)
		}
		switch u.Scheme {
		case "ssh", "https", "http", "git":
		default:
			return "", fmt.Errorf("unsupported origin scheme %q", u.Scheme)
		}
		host = u.Hostname()
		path = u.Path
	} else {
		// SCP syntax is [user@]host:path, not a local filesystem path.
		before, after, ok := strings.Cut(raw, ":")
		if !ok || strings.ContainsAny(before, "/\\") {
			return "", fmt.Errorf("origin must identify a remote host and repository")
		}
		if at := strings.LastIndex(before, "@"); at >= 0 {
			before = before[at+1:]
		}
		host = before
		path = after
	}
	host = strings.ToLower(host)
	path = strings.Trim(strings.TrimSuffix(strings.TrimRight(path, "/"), ".git"), "/")
	if host == "" || path == "" || strings.ContainsAny(host+path, "\x00\r\n\t ") || strings.Contains(path, "..") {
		return "", fmt.Errorf("invalid origin host or repository path")
	}
	if !strings.Contains(path, "/") {
		return "", fmt.Errorf("origin requires owner/repository")
	}
	return host + "/" + path, nil
}

// localRemote follows Git's rule: without "://", a remote is SCP-like only when
// a colon precedes any slash; otherwise it is a local path.
func localRemote(raw string) bool {
	raw = strings.TrimSpace(raw)
	if scheme, _, ok := strings.Cut(raw, "://"); ok {
		return strings.EqualFold(scheme, "file")
	}
	before, _, ok := strings.Cut(raw, ":")
	return !ok || strings.ContainsAny(before, "/\\")
}
func Identity(d Discovery) (string, bool, error) {
	if d.CommonDir != "" {
		origin, err := gitConfigValue(filepath.Join(d.CommonDir, "config"), "remote", "origin", "url")
		if err != nil {
			return "", false, err
		}
		// A local-path or file:// origin names no shared identity; treat it as no remote.
		if origin != "" && !localRemote(origin) {
			id, err := NormalizeRemote(origin)
			if err != nil {
				return "", false, fmt.Errorf("remote.origin.url: %w; set project.id in backlog.yaml", err)
			}
			return id, false, nil
		}
	}
	return d.ControlRoot, true, nil
}

// Only repository-local facts are consulted: includes are not followed, so a
// committed repository cannot cause config inspection to read arbitrary files.
func gitConfigValue(path, wantSection, wantSubsection, wantKey string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	section, subsection, value := "", "", ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end < 0 {
				return "", fmt.Errorf("%s: invalid Git section", path)
			}
			header := strings.TrimSpace(line[1:end])
			section, subsection, _ = strings.Cut(header, " ")
			section = strings.ToLower(section)
			subsection = strings.TrimSpace(subsection)
			if subsection != "" {
				subsection, err = strconv.Unquote(subsection)
				if err != nil {
					return "", fmt.Errorf("%s: invalid Git subsection: %w", path, err)
				}
			} else if s, sub, ok := strings.Cut(section, "."); ok {
				section, subsection = s, sub
			}
			continue
		}
		if section != wantSection || subsection != wantSubsection {
			continue
		}
		key, raw, hasEquals := strings.Cut(line, "=")
		if !hasEquals {
			key, raw, _ = strings.Cut(line, " ")
		}
		key = strings.TrimSpace(key)
		if !strings.EqualFold(key, wantKey) {
			continue
		}
		value, err = gitConfigString(strings.TrimSpace(raw))
		if err != nil {
			return "", fmt.Errorf("%s: %s: %w", path, key, err)
		}
	}
	return value, scanner.Err()
}
func gitConfigString(raw string) (string, error) {
	var out strings.Builder
	quoted, escaped := false, false
	for _, r := range raw {
		if escaped {
			switch r {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case 'b':
				out.WriteByte('\b')
			case '\\', '"':
				out.WriteRune(r)
			default:
				return "", fmt.Errorf("invalid Git escape")
			}
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			continue
		}
		if !quoted && (r == '#' || r == ';') {
			break
		}
		out.WriteRune(r)
	}
	if quoted || escaped {
		return "", fmt.Errorf("unterminated Git string")
	}
	return strings.TrimSpace(out.String()), nil
}

// Branch reads the checkout's current branch from HEAD without starting git.
// A detached HEAD has none.
func (d Discovery) Branch() string {
	if d.GitDir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(d.GitDir, "HEAD"))
	if err != nil {
		return ""
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "ref: refs/heads/")
	// A reftable repository's HEAD file is a stub naming .invalid.
	if !ok || ref == ".invalid" {
		return ""
	}
	return ref
}
