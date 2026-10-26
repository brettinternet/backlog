// Package itemjson defines the JSON item shapes shared by bl --json and MCP.
package itemjson

import (
	"strings"

	"github.com/brettinternet/backlog/internal/model"
)

type Summary struct {
	Project  string               `json:"project,omitempty"`
	Source   string               `json:"source"`
	ID       string               `json:"id"`
	Title    string               `json:"title"`
	Status   string               `json:"status"`
	Category model.StatusCategory `json:"category"`
	Priority model.Priority       `json:"priority"`
	Version  string               `json:"version"`
	// StatusName is the provider display name of an opaque Status, for text output.
	StatusName string `json:"-"`
}

func NewSummary(i model.Item) Summary {
	return Summary{i.Project, i.Ref.Source, i.Ref.ID, i.Title, i.Status, i.Category, i.Priority, i.Version, i.Names[i.Status]}
}

func Summaries(items []model.Item) []Summary {
	out := make([]Summary, 0, len(items))
	for _, i := range items {
		out = append(out, NewSummary(i))
	}
	return out
}

type Detail struct {
	Resource string            `json:"resource,omitempty"`
	Item     Summary           `json:"item"`
	Metadata map[string]any    `json:"metadata"`
	Sections map[string]string `json:"sections"`
	Criteria []model.Criterion `json:"criteria,omitempty"`
	DoD      []model.Criterion `json:"dod,omitempty"`
	// Claims and ClaimsError report the resource's Worklease history.
	Claims      []model.ClaimEpoch `json:"claims,omitempty"`
	ClaimsError string             `json:"claimsError,omitempty"`
}

func NewDetail(i model.Item, resource string) Detail {
	return Detail{Resource: resource, Item: NewSummary(i), Metadata: Metadata(i), Sections: Sections(i), Criteria: i.Criteria, DoD: i.DoD}
}

// Metadata writes edge targets as source:id, resolving local refs to the
// item's own source.
func Metadata(i model.Item) map[string]any {
	ref := func(r *model.Ref) string {
		if r == nil {
			return ""
		}
		source := r.Source
		if source == "" {
			source = i.Ref.Source
		}
		return source + ":" + r.ID
	}
	refs := func(values []model.Ref) []string {
		out := []string{}
		for _, v := range values {
			out = append(out, ref(&v))
		}
		return out
	}
	return map[string]any{"type": i.Type, "rank": i.Rank, "assignees": i.Assignees, "labels": i.Labels, "milestone": i.Milestone, "due": i.Due, "parent": ref(i.Parent), "children": refs(i.Children), "blockedBy": refs(i.BlockedBy), "blocks": refs(i.Blocks), "related": refs(i.Related), "duplicateOf": ref(i.DuplicateOf), "refs": i.Refs, "files": i.Files, "created": i.Created, "updated": i.Updated, "url": i.URL, "fields": i.Fields}
}

func Sections(i model.Item) map[string]string {
	checks := func(values []model.Criterion) string {
		lines := []string{}
		for _, c := range values {
			mark := " "
			if c.Checked {
				mark = "x"
			}
			line := "- [" + mark + "] " + c.Text
			if c.Evidence != "" {
				line += "\n  Evidence: " + c.Evidence
			}
			lines = append(lines, line)
		}
		return strings.Join(lines, "\n")
	}
	comments := []string{}
	for _, c := range i.Comments {
		comments = append(comments, c.Time.Format("2006-01-02T15:04:05Z07:00")+" "+c.Author+"\n"+c.Body)
	}
	return map[string]string{"description": i.Description, "criteria": checks(i.Criteria), "dod": checks(i.DoD), "plan": i.Plan, "notes": i.Notes, "summary": i.Summary, "comments": strings.Join(comments, "\n\n")}
}
