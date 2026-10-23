// Package beads adapts the bd 1.3.0 JSON CLI. The command and edge mapping is
// ported from Worklease v1.9.3 internal/queue/beads{,_write}.go.
package beads

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

type Options struct {
	ID, Path, Binary string
	Statuses         map[string]model.StatusCategory
}
type Source struct {
	options Options
	mu      sync.Mutex
}

// New validates the pinned executable and local embedded-Dolt boundary once.
// Refreshes thereafter make exactly one bulk call, never one call per item.
func New(o Options) (*Source, error) {
	if o.ID == "" || !filepath.IsAbs(o.Path) {
		return nil, source.Refuse("unavailable", "Beads requires an ID and absolute checkout path")
	}
	if o.Binary == "" {
		o.Binary = "bd"
	}
	metadataPath := filepath.Join(o.Path, ".beads", "metadata.json")
	stat, err := os.Lstat(metadataPath)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 16384 {
		return nil, source.Refuse("unavailable", "Beads requires safe local metadata")
	}
	data, err := os.ReadFile(metadataPath)
	var metadata struct {
		Mode string `json:"dolt_mode"`
	}
	if err != nil || json.Unmarshal(data, &metadata) != nil || metadata.Mode != "embedded" {
		return nil, source.Refuse("unavailable", "Beads requires local embedded Dolt metadata")
	}
	s := &Source{options: o}
	var version struct {
		Version string `json:"version"`
	}
	if err = s.run(context.Background(), false, &version, "version"); err != nil {
		return nil, err
	}
	if version.Version != "1.3.0" {
		return nil, source.Refuse("unavailable", "bd 1.3.0 required")
	}
	var remote struct {
		Value string `json:"value"`
	}
	if err = s.run(context.Background(), false, &remote, "config", "get", "sync.remote"); err != nil {
		return nil, err
	}
	if remote.Value != "" {
		return nil, source.Refuse("unavailable", "Beads configured Dolt remotes are not supported")
	}
	return s, nil
}
func (s *Source) Info() source.Info {
	return source.Info{ID: s.options.ID, Kind: "beads", Display: s.options.ID, Capabilities: model.Capabilities{model.Set: true, model.Batch: true, model.AppendComment: true}}
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, fmt.Errorf("provider output limit")
	}
	return b.Buffer.Write(p)
}
func (s *Source) run(ctx context.Context, write bool, target any, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	argv := []string{"--json", "--sandbox", "-C", s.options.Path}
	if write {
		argv = append(argv, "--dolt-auto-commit=on")
	} else {
		argv = append(argv, "--readonly")
	}
	cmd := exec.CommandContext(ctx, s.options.Binary, append(argv, args...)...)
	cmd.Dir = s.options.Path
	cmd.WaitDelay = time.Second
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "BEADS_") && !strings.HasPrefix(key, "BD_") && !strings.HasPrefix(key, "DOLT_") && !strings.HasPrefix(key, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "BD_NON_INTERACTIVE=1", "BD_LAST_TOUCHED_FALLBACK=0")
	stdout, stderr := &boundedBuffer{limit: 64 << 20}, &boundedBuffer{limit: 4096}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 13 {
			return source.Refuse("conflict", "Beads status or assignee changed")
		}
		return source.Refuse("unavailable", "bd command failed or exceeded time/output limit")
	}
	if target != nil && json.Unmarshal(stdout.Bytes(), target) != nil {
		return source.Refuse("unavailable", "bd returned invalid JSON")
	}
	return nil
}

type dependency struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
}
type issue struct {
	ID              string       `json:"id"`
	Title           string       `json:"title"`
	Status          string       `json:"status"`
	Description     string       `json:"description"`
	Notes           string       `json:"notes"`
	Type            string       `json:"issue_type"`
	Assignee        string       `json:"assignee"`
	Priority        *int         `json:"priority"`
	Labels          []string     `json:"labels"`
	Created         time.Time    `json:"created_at"`
	Updated         time.Time    `json:"updated_at"`
	Dependencies    []dependency `json:"dependencies"`
	DependencyCount int          `json:"dependency_count"`
}

func validID(id string) bool {
	return id != "" && !strings.HasPrefix(id, "-") && !strings.ContainsAny(id, "\x00\r\n\x1b")
}
func (s *Source) Load(ctx context.Context, _ source.Cursor) (source.Delta, error) {
	var rows []json.RawMessage
	if err := s.run(ctx, false, &rows, "list", "--all", "--limit", "0"); err != nil {
		return source.Delta{}, err
	}
	if rows == nil {
		return source.Delta{}, source.Refuse("unavailable", "bd omitted issue array")
	}
	delta := source.Delta{Coverage: source.Complete, Items: make([]model.Item, 0, len(rows))}
	seen := map[string]bool{}
	for _, raw := range rows {
		var row issue
		if json.Unmarshal(raw, &row) != nil || !validID(row.ID) || row.Title == "" || row.Status == "" || seen[row.ID] || row.DependencyCount > len(row.Dependencies) {
			return source.Delta{}, source.Refuse("unavailable", "bd returned incomplete or invalid issues")
		}
		seen[row.ID] = true
		item, err := s.item(row, raw)
		if err != nil {
			return source.Delta{}, err
		}
		delta.Items = append(delta.Items, item)
	}
	return delta, nil
}
func (s *Source) item(row issue, raw []byte) (model.Item, error) {
	item := model.Item{Ref: model.Ref{Source: s.options.ID, ID: row.ID}, Title: row.Title, Type: row.Type, Status: row.Status, Description: row.Description, Notes: row.Notes, Labels: row.Labels, Created: row.Created, Updated: row.Updated}
	item.Category = s.options.Statuses[row.Status]
	if item.Category == "" {
		switch row.Status {
		case "open":
			item.Category = model.Todo
		case "in_progress":
			item.Category = model.Active
		case "closed":
			item.Category = model.Done
		case "tombstone":
			item.Category = model.Canceled
		default:
			item.Category = model.Backlog
		}
	}
	if row.Assignee != "" {
		item.Assignees = []string{row.Assignee}
	}
	if row.Priority != nil {
		switch *row.Priority {
		case 0:
			item.Priority = model.Urgent
		case 1:
			item.Priority = model.High
		case 2:
			item.Priority = model.Medium
		case 3, 4:
			item.Priority = model.Low
		default:
			return model.Item{}, source.Refuse("unavailable", "bd returned invalid priority")
		}
	}
	if err := json.Unmarshal(raw, &item.Fields); err != nil {
		return model.Item{}, err
	}
	// Hash provider JSON canonically, including unknown fields and dependency changes.
	canonical, _ := json.Marshal(item.Fields)
	item.Version = fmt.Sprintf("%x", sha256.Sum256(canonical))
	for _, dep := range row.Dependencies {
		if dep.IssueID != row.ID || !validID(dep.DependsOnID) || dep.Type == "" {
			return model.Item{}, source.Refuse("unavailable", "bd returned malformed dependencies")
		}
		target := model.Ref{Source: s.options.ID, ID: dep.DependsOnID}
		kind := model.Related
		switch dep.Type {
		case "blocks":
			kind = model.BlockedBy
			item.BlockedBy = append(item.BlockedBy, target)
		case "parent-child":
			kind = model.Parent
			if item.Parent != nil {
				return model.Item{}, source.Refuse("unavailable", "bd returned multiple parents")
			}
			item.Parent = &target
		default:
			item.Related = append(item.Related, target)
		}
		item.Edges = append(item.Edges, model.Edge{Kind: kind, Target: target})
	}
	return item, nil
}

// Read uses the same bulk schema as Load: bd show has a different, lossy edge
// schema. This also makes displayed and preflight versions identical.
func (s *Source) Read(ctx context.Context, id string) (model.Item, error) {
	if !validID(id) {
		return model.Item{}, source.Refuse("invalid-id", "invalid Beads ID")
	}
	delta, err := s.Load(ctx, source.Cursor{})
	if err != nil {
		return model.Item{}, err
	}
	for _, item := range delta.Items {
		if item.Ref.ID == id {
			return item, nil
		}
	}
	return model.Item{}, source.Refuse("not-found", id)
}
func updateArgs(m model.Mutation) ([]string, error) {
	changes := []model.Mutation{m}
	if m.Op == model.Batch {
		changes = m.Changes
	}
	if len(changes) == 0 {
		return nil, source.Refuse("rejected", "empty update")
	}
	args := []string{"update", m.Ref.ID}
	seen := map[string]bool{}
	for _, change := range changes {
		if change.Op != model.Set || seen[change.Field] || (change.Ref != (model.Ref{}) && change.Ref != m.Ref) {
			return nil, source.Refuse("unavailable", "Beads supports field sets only in update batches")
		}
		seen[change.Field] = true
		flag := "--" + change.Field
		value, ok := change.Value.(string)
		switch change.Field {
		case "title", "status", "type":
		case "priority":
			switch model.Priority(value) {
			case model.Urgent:
				value = "0"
			case model.High:
				value = "1"
			case model.Medium:
				value = "2"
			case model.Low:
				value = "3"
			default:
				ok = false
			}
		case "assignees":
			list, valid := change.Value.([]string)
			ok = valid && len(list) <= 1
			value = ""
			if len(list) == 1 {
				value = list[0]
			}
			flag = "--assignee"
		case "labels":
			list, valid := change.Value.([]string)
			ok = valid
			for _, label := range list {
				if strings.Contains(label, ",") {
					ok = false
				}
			}
			value = strings.Join(list, ",")
			flag = "--set-labels"
		default:
			return nil, source.Refuse("unavailable", "unsupported Beads field")
		}
		if !ok || strings.ContainsRune(value, 0) {
			return nil, source.Refuse("rejected", "invalid Beads field value")
		}
		// Equals binds leading-dash text as data, never as an option.
		args = append(args, flag+"="+value)
	}
	return args, nil
}
func (s *Source) Apply(ctx context.Context, m model.Mutation) (model.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.Ref.Source != s.options.ID || !validID(m.Ref.ID) {
		return model.Item{}, source.Refuse("invalid-id", "item outside Beads source")
	}
	var args []string
	var err error
	if m.Op == model.AppendComment {
		if m.Comment.Body == "" {
			return model.Item{}, source.Refuse("rejected", "comment body required")
		}
		args = []string{"comment", m.Ref.ID, "--", m.Comment.Body}
		if m.Comment.Author != "" {
			args = append([]string{"--actor=" + m.Comment.Author}, args...)
		}
	} else {
		args, err = updateArgs(m)
		if err != nil {
			return model.Item{}, err
		}
	}
	before, err := s.Read(ctx, m.Ref.ID)
	if err != nil {
		return model.Item{}, err
	}
	if m.ExpectedVersion == "" || m.ExpectedVersion != before.Version {
		return model.Item{}, source.Refuse("conflict", "Beads item changed or expected version missing")
	}
	if m.Op != model.AppendComment {
		assignee := ""
		if len(before.Assignees) == 1 {
			assignee = before.Assignees[0]
		}
		args = append(args, "--if-status="+before.Status, "--if-assignee="+assignee)
	}
	if err = s.run(ctx, true, nil, args...); err != nil {
		return model.Item{}, err
	}
	return s.Read(ctx, m.Ref.ID)
}
func (s *Source) Prepare(ctx context.Context, m model.Mutation) (source.Prepared, error) {
	if _, err := updateArgs(m); err != nil {
		return source.Prepared{}, err
	}
	before, err := s.Read(ctx, m.Ref.ID)
	if err != nil {
		return source.Prepared{}, err
	}
	return source.PrepareFields(before, m)
}
func (s *Source) Watch(ctx context.Context, changed func()) error {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			changed()
		}
	}
}

var _ source.Source = (*Source)(nil)
