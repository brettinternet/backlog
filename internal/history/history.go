// Package history composes an item's timeline from its events, claim history,
// comments, and the Git history of its file (design.md D40). Each provider
// reports its own status, so one that fails never hides the others.
package history

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/brettinternet/backlog/internal/source"
)

// Entry is one row of an item's history. ID is stable across loads, so it can
// be a paging cursor or an expanded-row key. An entry with Span heads a group;
// Group names the head an entry nests under. Resumable marks a session row
// whose agent bl resume can reopen.
type Entry struct {
	ID        string    `json:"id"`
	Time      time.Time `json:"time"`
	Kind      string    `json:"kind"`
	Actor     string    `json:"actor,omitempty"`
	Session   string    `json:"session,omitempty"`
	Group     string    `json:"group,omitempty"`
	Span      *Span     `json:"span,omitempty"`
	Resumable bool      `json:"resumable,omitempty"`
	Summary   string    `json:"summary"`
	Detail    string    `json:"detail,omitempty"`
	Provider  string    `json:"provider"`
}

// Span is the extent of a group. End is zero while it is open.
type Span struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end,omitzero"`
	State string    `json:"state,omitempty"`
}

const (
	OK          = "ok"
	Partial     = "partial"
	Unavailable = "unavailable"
)

// Status is how one provider fared.
type Status struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
}

// Result is a composed history, oldest first, with every group head followed
// by its members.
type Result struct {
	Entries   []Entry  `json:"entries"`
	Providers []Status `json:"providers"`
}

// Provider loads one kind of entry. An error wrapped by PartialError keeps the
// returned entries; any other error makes the provider unavailable.
type Provider struct {
	Name string
	Load func(context.Context) ([]Entry, error)
}

type partialError struct{ err error }

func (e partialError) Error() string { return e.err.Error() }
func (e partialError) Unwrap() error { return e.err }

// PartialError marks entries a provider returned as incomplete.
func PartialError(err error) error { return partialError{err} }

// Compose runs providers concurrently and merges their entries. When two
// providers report the same entry ID, the earlier provider's entry wins.
func Compose(ctx context.Context, providers ...Provider) Result {
	loaded := make([][]Entry, len(providers))
	statuses := make([]Status, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Go(func() {
			entries, err := load(ctx, p)
			statuses[i] = Status{Provider: p.Name, State: OK}
			var partial partialError
			switch {
			case errors.As(err, &partial):
				statuses[i].State, statuses[i].Reason = Partial, partial.Error()
			case err != nil:
				statuses[i].State, statuses[i].Reason, entries = Unavailable, err.Error(), nil
			}
			for j := range entries {
				entries[j].Provider = cmp.Or(entries[j].Provider, p.Name)
			}
			loaded[i] = entries
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	var entries []Entry
	for _, list := range loaded {
		for _, e := range list {
			if !seen[e.ID] {
				seen[e.ID] = true
				entries = append(entries, e)
			}
		}
	}
	return Result{Entries: arrange(entries), Providers: statuses}
}

func load(ctx context.Context, p Provider) (entries []Entry, err error) {
	defer func() {
		if r := recover(); r != nil {
			entries, err = nil, fmt.Errorf("panic: %v", r)
		}
	}()
	return p.Load(ctx)
}

// arrange orders top-level entries and group heads by time, and puts each
// head's members right after it. A session's entries join its session head.
func arrange(entries []Entry) []Entry {
	heads := map[string]bool{}
	for _, e := range entries {
		if e.Span != nil {
			heads[e.ID] = true
		}
	}
	for i, e := range entries {
		if e.Group == "" && e.Span == nil && e.Session != "" && heads[sessionID(e.Session)] {
			entries[i].Group = sessionID(e.Session)
		}
		if !heads[entries[i].Group] {
			entries[i].Group = ""
		}
	}
	order := func(a, b Entry) int {
		return cmp.Or(a.Time.Compare(b.Time), compareBool(a.Span == nil, b.Span == nil), strings.Compare(a.ID, b.ID))
	}
	slices.SortStableFunc(entries, order)
	members := map[string][]Entry{}
	var top []Entry
	for _, e := range entries {
		if e.Group != "" {
			members[e.Group] = append(members[e.Group], e)
		} else {
			top = append(top, e)
		}
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range top {
		out = append(out, e)
		out = append(out, members[e.ID]...)
	}
	return out
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

func sessionID(id string) string { return "session:" + id }

// SessionOf returns the session an entry ID heads, if it is a session row.
func SessionOf(id string) (string, bool) { return strings.CutPrefix(id, "session:") }

// Page is one page of a Result, as bl history and the MCP history tool return
// it.
type Page struct {
	Entries   []Entry  `json:"entries"`
	Providers []Status `json:"providers"`
	Cursor    string   `json:"cursor,omitempty"`
	More      bool     `json:"more"`
}

// Page returns up to limit entries after the entry named by cursor. A limit of
// 0 returns every remaining entry. The cursor also fingerprints the entries up
// to it: a session's entries follow its row, so one added to an earlier
// session lands before the cursor, and paging on would skip it. Page then
// refuses with conflict, and the caller pages again from the start.
func (r Result) Page(cursor string, limit int) (Page, error) {
	start := 0
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(raw) <= fingerprintSize {
			return Page{}, source.Refuse("not-found", "unknown history cursor")
		}
		id := string(raw[fingerprintSize:])
		i := slices.IndexFunc(r.Entries, func(e Entry) bool { return e.ID == id })
		if i < 0 {
			return Page{}, source.Refuse("not-found", "unknown history cursor")
		}
		if string(raw[:fingerprintSize]) != fingerprint(r.Entries[:i+1]) {
			return Page{}, source.Refuse("conflict", "history changed before the cursor; page again from the start")
		}
		start = i + 1
	}
	page := Page{Entries: r.Entries[start:], Providers: r.Providers}
	if limit > 0 && len(page.Entries) > limit {
		page.Entries, page.More = page.Entries[:limit], true
		page.Cursor = base64.RawURLEncoding.EncodeToString([]byte(fingerprint(r.Entries[:start+limit]) + page.Entries[limit-1].ID))
	}
	if page.Entries == nil {
		page.Entries = []Entry{}
	}
	return page, nil
}

// Label names where an entry came from, usually within LabelWidth cells, so a
// reader can tell agent work, edits, commits, and remote activity apart.
func Label(e Entry) string {
	switch {
	case e.Kind == "commit":
		return "git"
	case e.Kind == "comment":
		return "comment"
	case e.Kind == "session" && e.Actor != "":
		return "agent"
	case e.Kind == "session.handoff":
		return "handoff"
	case e.Kind == "activity":
		return e.Provider
	case strings.HasPrefix(e.Kind, "item."):
		return "item"
	}
	kind, _, _ := strings.Cut(e.Kind, ".")
	return cmp.Or(kind, e.Provider)
}

// LabelWidth is the widest built-in Label, which a label column pads to.
const LabelWidth = 7

const fingerprintSize = 12

// fingerprint identifies the IDs of entries, in order.
func fingerprint(entries []Entry) string {
	h := sha256.New()
	for _, e := range entries {
		h.Write([]byte(e.ID))
		h.Write([]byte{0})
	}
	return string(h.Sum(nil)[:fingerprintSize])
}
