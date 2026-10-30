package history

import (
	"fmt"
	"strings"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/textutil"
)

// changeEntries make one group per linked change (design.md D43). Its head
// spans the change from its first event to its merge, close, or last landed
// commit; its members are those events and the commits it landed, which share
// IDs with the git provider's so a commit appears once.
func changeEntries(cs []changes.Change) []Entry {
	var out []Entry
	for _, c := range cs {
		id := "change:" + c.ID
		var members []Entry
		for _, e := range c.Events {
			members = append(members, Entry{ID: id + ":" + e.Kind, Time: e.Time, Kind: "change." + e.Kind, Group: id, Summary: changeEvent(c, e.Kind)})
		}
		for _, commit := range c.Commits {
			members = append(members, Entry{ID: commitID(commit.SHA), Time: commit.Time, Kind: "commit", Group: id, Summary: textutil.Line(commit.Subject), Detail: commit.SHA})
		}
		span := &Span{State: c.State}
		if c.Stage == changes.StageLanded && c.State == changes.StateMerged {
			span.State = "landed"
		}
		ended := c.State == changes.StateMerged || c.State == changes.StateClosed
		for _, m := range members {
			if m.Time.IsZero() {
				continue
			}
			if span.Start.IsZero() || m.Time.Before(span.Start) {
				span.Start = m.Time
			}
			if ended && m.Time.After(span.End) {
				span.End = m.Time
			}
		}
		if span.Start.IsZero() {
			span.Start = c.Updated
		}
		// A change with no known time has no place on a timeline; the Changes
		// tab still lists it.
		if span.Start.IsZero() {
			continue
		}
		out = append(out, Entry{ID: id, Time: span.Start, Kind: "change", Span: span, Summary: changeSummary(c, span.State), Detail: changeDetail(c)})
		out = append(out, members...)
	}
	return out
}

func changeSummary(c changes.Change, state string) string {
	label := textutil.Line(changes.Label(c))
	parts := []string{label}
	if title := textutil.Line(c.Title); title != "" && title != label {
		parts = append(parts, title)
	}
	parts = append(parts, state)
	if c.Checks != "" {
		parts = append(parts, "checks "+c.Checks)
	}
	switch c.Review {
	case changes.ReviewApproved:
		parts = append(parts, "approved")
	case changes.ReviewChanges:
		parts = append(parts, "changes requested")
	case changes.ReviewRequested:
		parts = append(parts, "review requested")
	}
	if c.Reason != "" {
		parts = append(parts, textutil.Line(c.Reason))
	}
	return strings.Join(parts, " · ")
}

func changeEvent(c changes.Change, kind string) string {
	switch kind {
	case "worktree":
		return "worktree " + textutil.Line(c.Worktree)
	case "branch":
		return "branch " + textutil.Line(c.Branch)
	case "opened":
		if c.Number > 0 {
			return "opened " + changes.Label(c)
		}
	}
	return kind
}

// changeDetail aligns the change's locations after the longest key, as a
// session's detail does.
func changeDetail(c changes.Change) string {
	var detail []string
	for _, field := range [][2]string{{"url", c.URL}, {"branch", c.Branch}, {"head", c.Head}, {"worktree", c.Worktree}, {"linked by", c.Link}} {
		if field[1] != "" {
			detail = append(detail, fmt.Sprintf("%-10s %s", field[0]+":", textutil.Line(field[1])))
		}
	}
	return strings.Join(detail, "\n")
}
