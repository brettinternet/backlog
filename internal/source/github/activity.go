package github

import (
	"cmp"
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

// timelineTypes are the issue timeline events History shows. Comments come
// from the comments provider, so ISSUE_COMMENT is left out.
var timelineTypes = []string{"LABELED_EVENT", "UNLABELED_EVENT", "ASSIGNED_EVENT", "UNASSIGNED_EVENT", "CLOSED_EVENT", "REOPENED_EVENT", "RENAMED_TITLE_EVENT", "CROSS_REFERENCED_EVENT", "CONNECTED_EVENT", "REFERENCED_EVENT"}

const subjectFields = `...on Issue{number title repository{nameWithOwner}} ...on PullRequest{number title repository{nameWithOwner}}`

const timelineQuery = `query($owner:String!,$repo:String!,$number:Int!,$types:[IssueTimelineItemsItemType!],$before:String){repository(owner:$owner,name:$repo){issue(number:$number){timelineItems(last:100,before:$before,itemTypes:$types){pageInfo{hasPreviousPage startCursor} nodes{__typename
...on LabeledEvent{id createdAt actor{login} label{name}}
...on UnlabeledEvent{id createdAt actor{login} label{name}}
...on AssignedEvent{id createdAt actor{login} assignee{...on Actor{login}}}
...on UnassignedEvent{id createdAt actor{login} assignee{...on Actor{login}}}
...on ClosedEvent{id createdAt actor{login} stateReason}
...on ReopenedEvent{id createdAt actor{login}}
...on RenamedTitleEvent{id createdAt actor{login} previousTitle currentTitle}
...on CrossReferencedEvent{id createdAt actor{login} source{` + subjectFields + `}}
...on ConnectedEvent{id createdAt actor{login} subject{` + subjectFields + `}}
...on ReferencedEvent{id createdAt actor{login} commit{abbreviatedOid messageHeadline} commitRepository{nameWithOwner}}
...on ProjectV2ItemStatusChangedEvent{id createdAt actor{login} previousStatus status project{title}}
}}}}}`

type login struct{ Login string }
type subject struct {
	Number     int
	Title      string
	Repository struct{ NameWithOwner string }
}
type timelineEvent struct {
	Typename                    string `json:"__typename"`
	ID                          string
	CreatedAt                   time.Time
	Actor                       *login
	Label                       *struct{ Name string }
	Assignee                    *login
	StateReason                 string
	PreviousTitle, CurrentTitle string
	Source, Subject             *subject
	Commit                      *struct{ AbbreviatedOid, MessageHeadline string }
	CommitRepository            *struct{ NameWithOwner string }
	PreviousStatus, Status      string
	Project                     *struct{ Title string }
}

// Activity reads an issue's timeline from the issue's own repository, newest
// pages first. Projects v2 status changes are requested only by a source bound
// to a project, whose token already reads projects. A draft has no timeline.
func (s *Source) Activity(ctx context.Context, item model.Item) ([]model.Activity, bool, error) {
	if draftID(item.Ref.ID) {
		return nil, false, nil
	}
	repo, n, err := s.parseID(item.Ref.ID)
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.authenticate(ctx)
	if err != nil {
		return nil, false, err
	}
	types := timelineTypes
	if s.o.Project != nil {
		types = append(types[:len(types):len(types)], "PROJECT_V2_ITEM_STATUS_CHANGED_EVENT")
	}
	vars := repoVars(repo)
	vars["number"], vars["types"] = n, types
	var events []timelineEvent
	for {
		var result struct {
			Repository *struct {
				Issue *struct {
					TimelineItems struct {
						PageInfo struct {
							HasPreviousPage bool
							StartCursor     string
						}
						Nodes []timelineEvent
					}
				}
			}
		}
		if err := s.query(ctx, c, timelineQuery, vars, &result, "repository", "issue"); err != nil {
			return nil, false, err
		}
		if result.Repository == nil || result.Repository.Issue == nil {
			return nil, false, source.Refuse("not-found", "issue unavailable")
		}
		page := result.Repository.Issue.TimelineItems
		events = append(page.Nodes, events...)
		if !page.PageInfo.HasPreviousPage {
			return timelineActivity(events), false, nil
		}
		if len(events) >= source.ActivityLimit || page.PageInfo.StartCursor == "" {
			return timelineActivity(events[len(events)-min(len(events), source.ActivityLimit):]), true, nil
		}
		vars["before"] = page.PageInfo.StartCursor
	}
}

func timelineActivity(events []timelineEvent) []model.Activity {
	out := make([]model.Activity, 0, len(events))
	for _, e := range events {
		a := model.Activity{ID: e.ID, Time: e.CreatedAt}
		if e.Actor != nil {
			a.Actor = e.Actor.Login
		}
		switch e.Typename {
		case "LabeledEvent", "UnlabeledEvent":
			if e.Label == nil {
				continue
			}
			a.Summary = strings.ToLower(strings.TrimSuffix(e.Typename, "Event")) + " " + e.Label.Name
		case "AssignedEvent", "UnassignedEvent":
			a.Summary = strings.ToLower(strings.TrimSuffix(e.Typename, "Event"))
			if e.Assignee != nil {
				a.Summary += " " + e.Assignee.Login
			}
		case "ClosedEvent":
			a.Summary = "closed"
			if e.StateReason != "" {
				a.Summary += " as " + strings.ReplaceAll(strings.ToLower(e.StateReason), "_", " ")
			}
		case "ReopenedEvent":
			a.Summary = "reopened"
		case "RenamedTitleEvent":
			a.Summary, a.Detail = "renamed", e.PreviousTitle+" → "+e.CurrentTitle
		case "CrossReferencedEvent", "ConnectedEvent":
			ref := e.Source
			a.Summary = "referenced from "
			if e.Typename == "ConnectedEvent" {
				ref, a.Summary = e.Subject, "linked "
			}
			if ref == nil || ref.Number == 0 {
				continue
			}
			a.Summary += ref.Repository.NameWithOwner + "#" + strconv.Itoa(ref.Number)
			a.Detail = ref.Title
		case "ReferencedEvent":
			if e.Commit == nil {
				continue
			}
			a.Summary = "referenced in commit " + e.Commit.AbbreviatedOid
			if e.CommitRepository != nil {
				a.Summary = "referenced in " + e.CommitRepository.NameWithOwner + "@" + e.Commit.AbbreviatedOid
			}
			a.Detail = e.Commit.MessageHeadline
		case "ProjectV2ItemStatusChangedEvent":
			a.Summary = "status " + cmp.Or(e.PreviousStatus, "∅") + " → " + cmp.Or(e.Status, "∅")
			if e.Project != nil {
				a.Detail = "project: " + e.Project.Title
			}
		default:
			continue
		}
		if a.ID == "" || a.Time.IsZero() {
			continue
		}
		out = append(out, a)
	}
	return out
}
