package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

// Catches one failing or panicking provider hiding the others, duplicate claim
// rows from the event log and Worklease, and session entries escaping their
// session row.
func TestComposeIsolatesProvidersAndGroupsSessions(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 1, 1, 0, m, 0, 0, time.UTC) }
	result := Compose(context.Background(),
		Provider{Name: "events", Load: func(context.Context) ([]Entry, error) {
			return []Entry{
				{ID: "event:1", Time: at(1), Kind: "item.created", Summary: "created"},
				{ID: "claim:c1:acquired", Time: at(2), Kind: "claim.acquired", Session: "s1", Summary: "acquired"},
				{ID: "event:3", Time: at(3), Kind: "item.updated", Session: "s1", Summary: "status todo → doing"},
			}, nil
		}},
		Provider{Name: "claims", Load: func(context.Context) ([]Entry, error) {
			return claimEntries([]model.ClaimEpoch{{ID: "c1", Session: "s1", AcquiredAt: at(2), EndedAt: at(5), EndReason: "released"}}, at(9)), nil
		}},
		Provider{Name: "sessions", Load: func(context.Context) ([]Entry, error) {
			return []Entry{{ID: "session:s1", Time: at(2), Kind: "session", Session: "s1", Span: &Span{Start: at(2), End: at(5), State: "released"}, Summary: "claude"}}, nil
		}},
		Provider{Name: "git", Load: func(context.Context) ([]Entry, error) { return nil, errors.New("not in a Git repository") }},
		Provider{Name: "comments", Load: func(context.Context) ([]Entry, error) { panic("boom") }},
		Provider{Name: "partial", Load: func(context.Context) ([]Entry, error) {
			return []Entry{{ID: "p:1", Time: at(4), Kind: "note", Summary: "kept"}}, PartialError(errors.New("newest 1"))
		}},
	)
	var ids []string
	for _, e := range result.Entries {
		ids = append(ids, e.ID+"<"+e.Group)
	}
	want := []string{"event:1<", "session:s1<", "claim:c1:acquired<session:s1", "event:3<session:s1", "claim:c1:released<session:s1", "p:1<"}
	if !slices.Equal(ids, want) {
		t.Fatalf("entries\n got %v\nwant %v", ids, want)
	}
	if got := result.Entries[2].Provider; got != "events" {
		t.Fatalf("duplicate claim kept from %s, want events", got)
	}
	states := map[string]string{}
	for _, s := range result.Providers {
		states[s.Provider] = s.State
	}
	if states["events"] != OK || states["git"] != Unavailable || states["comments"] != Unavailable || states["partial"] != Partial {
		t.Fatalf("statuses %+v", result.Providers)
	}
}

// Catches paging that silently skips an entry added to a session on an earlier
// page: it sorts before the cursor, so the next page must refuse instead.
func TestPageRefusesChangesBeforeCursor(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 1, 1, 0, m, 0, 0, time.UTC) }
	entries := []Entry{
		{ID: "session:s1", Time: at(1), Kind: "session", Session: "s1", Span: &Span{Start: at(1)}},
		{ID: "event:2", Time: at(2), Kind: "item.updated", Session: "s1"},
		{ID: "event:3", Time: at(3), Kind: "item.updated"},
		{ID: "event:4", Time: at(4), Kind: "item.updated"},
	}
	compose := func(entries []Entry) Result {
		return Compose(context.Background(), Provider{Name: "events", Load: func(context.Context) ([]Entry, error) { return slices.Clone(entries), nil }})
	}
	first, err := compose(entries).Page("", 3)
	if err != nil || !first.More || first.Cursor == "" {
		t.Fatalf("first page %+v %v", first, err)
	}
	if next, err := compose(append(entries, Entry{ID: "event:5", Time: at(5), Kind: "item.updated"})).Page(first.Cursor, 3); err != nil || len(next.Entries) != 2 || next.Entries[0].ID != "event:4" {
		t.Fatalf("an entry after the cursor broke paging: %+v %v", next, err)
	}
	changed := compose(append(entries, Entry{ID: "event:5", Time: at(5), Kind: "item.updated", Session: "s1"}))
	if changed.Entries[2].ID != "event:5" {
		t.Fatalf("fixture: %v", changed.Entries)
	}
	var refusal *source.Error
	if _, err := changed.Page(first.Cursor, 3); !errors.As(err, &refusal) || refusal.Reason != "conflict" {
		t.Fatalf("skipped an entry before the cursor: %v", err)
	}
}

// Catches renderers or JSON that only understand sessions: a provider's own
// group kind nests its members and survives a JSON round trip.
func TestNewGroupKindNests(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 1, 1, 0, m, 0, 0, time.UTC) }
	result := Compose(context.Background(), Provider{Name: "ci", Load: func(context.Context) ([]Entry, error) {
		return []Entry{
			{ID: "ci:run:2", Time: at(3), Kind: "ci.step", Group: "ci:run", Summary: "test"},
			{ID: "ci:run", Time: at(1), Kind: "ci.run", Span: &Span{Start: at(1), State: "running"}, Summary: "build 7"},
			{ID: "ci:run:1", Time: at(2), Kind: "ci.step", Group: "ci:run", Summary: "lint"},
			{ID: "orphan", Time: at(0), Kind: "ci.step", Group: "ci:gone", Summary: "unknown group"},
		}, nil
	}})
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Result
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range decoded.Entries {
		ids = append(ids, e.ID+"<"+e.Group)
	}
	if want := []string{"orphan<", "ci:run<", "ci:run:1<ci:run", "ci:run:2<ci:run"}; !slices.Equal(ids, want) || decoded.Entries[1].Span == nil {
		t.Fatalf("entries %v span %v", ids, decoded.Entries[1].Span)
	}
}

// Catches git honoring the caller's GIT_* environment instead of the minimal
// one, losing history across a rename, and a cache that keeps serving the
// history from before HEAD moved.
func TestGitHistoryFollowsRenamesAndHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo, cache := t.TempDir(), t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=Tester", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Env = append(slices.DeleteFunc(os.Environ(), func(v string) bool { return strings.HasPrefix(v, "GIT_DIR=") }), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("old.md", "one\n")
	git("add", "old.md")
	git("commit", "-q", "-m", "create")
	git("mv", "old.md", "BL-1.md")
	git("commit", "-q", "-m", "rename")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere"))
	path := filepath.Join(repo, "BL-1.md")
	summaries := func() []string {
		t.Helper()
		entries, err := gitHistory(context.Background(), cache, "v1", path)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.Summary)
		}
		return out
	}
	if got := summaries(); !slices.Equal(got, []string{"rename", "create"}) {
		t.Fatalf("history %v", got)
	}
	write("other.md", "x\n")
	git("add", "other.md")
	git("commit", "-q", "-m", "unrelated")
	write("BL-1.md", "two\n")
	git("commit", "-q", "-am", "edit")
	if got := summaries(); !slices.Equal(got, []string{"edit", "rename", "create"}) {
		t.Fatalf("history after HEAD moved %v", got)
	}
}

// Catches an edit's summary or detail misreporting what changed: which
// criterion was checked, which labels came and went, where appended notes
// start, or category repeating status.
func TestEditEntriesSayWhatChanged(t *testing.T) {
	criteria := func(checked ...bool) []any {
		var out []any
		for i, c := range checked {
			out = append(out, map[string]any{"text": fmt.Sprintf("criterion %d", i), "checked": c})
		}
		return out
	}
	entries := eventEntries([]hooks.Event{
		{ID: "1", Type: "item.updated", Changes: map[string]hooks.Change{
			"status":   {From: "todo", To: "in-progress"},
			"category": {From: "todo", To: "active"},
			"labels":   {From: []any{"a", "b"}, To: []any{"b", "c"}},
		}},
		{ID: "2", Type: "item.updated", Changes: map[string]hooks.Change{"criteria": {From: criteria(false, false), To: criteria(false, true, false)}}},
		{ID: "3", Type: "item.updated", Changes: map[string]hooks.Change{
			"notes": {From: "one\ntwo\nthree", To: "one\ntwo 2\nthree\nfour"},
			"title": {From: "Short", To: "Shorter"},
		}},
	})
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Summary+"\n"+e.Detail)
	}
	want := []string{
		"labels +c -a; status todo → in-progress\n",
		"criteria 0/2 → 1/3\n  [x] criterion 1\n  + [ ] criterion 2",
		"notes changed; title Short → Shorter\nnotes\n  …\n  - two\n  + two 2\n  …\n  + four",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("entries\n got %q\nwant %q", got, want)
	}
}

// Catches History reading an ambiguous landed change as landed while the
// Changes tab shows it as unknown.
func TestLandedChangeSpan(t *testing.T) {
	commit := changes.Commit{SHA: "c1", Subject: "Merge branch 'bl-9-x'", Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	for state, want := range map[string]string{changes.StateMerged: "landed", changes.StateUnknown: changes.StateUnknown} {
		got := changeEntries([]changes.Change{{ID: "bl-9-x", Stage: changes.StageLanded, State: state, Commits: []changes.Commit{commit}}})
		if len(got) == 0 || got[0].Span == nil || got[0].Span.State != want {
			t.Fatalf("%s: %+v", state, got)
		}
	}
}
