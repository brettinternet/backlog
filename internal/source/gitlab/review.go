package gitlab

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/textutil"
)

const reviewQuery = `query($path:ID!,$iid:String!){project(fullPath:$path){mergeRequest(iid:$iid){description diffHeadSha
reviewers{nodes{username mergeRequestInteraction{reviewState}}}
approvedBy{nodes{username}}
headPipeline{jobs(first:100){nodes{name status allowFailure webPath}}}
discussions(first:100){nodes{resolvable resolved notes(first:50){nodes{author{username} body createdAt url system position{filePath newLine oldLine}}}}}}}}`

type reviewNode struct {
	Description, DiffHeadSha string
	Reviewers                struct {
		Nodes []struct {
			Username                string
			MergeRequestInteraction *struct{ ReviewState string }
		}
	}
	ApprovedBy   struct{ Nodes []struct{ Username string } }
	HeadPipeline *struct {
		Jobs struct {
			Nodes []struct {
				Name, Status, WebPath string
				AllowFailure          bool
			}
		}
	}
	Discussions struct {
		Nodes []struct {
			Resolvable, Resolved bool
			Notes                struct{ Nodes []note }
		}
	}
}

type note struct {
	Author    *struct{ Username string }
	Body, URL string
	CreatedAt time.Time
	System    bool
	Position  *struct {
		FilePath         string
		NewLine, OldLine *int
	}
}

func (n note) comment() changes.Comment {
	author := "ghost"
	if n.Author != nil {
		author = n.Author.Username
	}
	return changes.Comment{Author: textutil.Line(author), Body: textutil.Sanitize(n.Body), URL: textutil.Line(n.URL), Time: n.CreatedAt.UTC()}
}

// reviewStates maps a reviewer's state; one not yet reviewing is waiting.
var reviewStates = map[string]string{
	"UNREVIEWED": changes.ReviewRequested, "REVIEW_STARTED": changes.ReviewRequested, "UNAPPROVED": changes.ReviewRequested,
	"REVIEWED": changes.ReviewCommented, "REQUESTED_CHANGES": changes.ReviewChanges, "APPROVED": changes.ReviewApproved,
}

// jobState maps a pipeline job's status; a job allowed to fail never fails
// the merge request.
func jobState(status string, allowFailure bool) string {
	switch status {
	case "SUCCESS":
		return changes.ChecksPassing
	case "SKIPPED":
		return changes.ChecksSkipped
	case "FAILED", "CANCELED", "CANCELING":
		if allowFailure {
			return changes.ChecksSkipped
		}
		return changes.ChecksFailing
	}
	return changes.ChecksPending
}

func (n reviewNode) review(host string) changes.Review {
	r := changes.Review{Head: n.DiffHeadSha, Body: textutil.Sanitize(n.Description)}
	states := map[string]string{}
	var order []string
	set := func(login, state string) {
		if login == "" || state == "" {
			return
		}
		if _, ok := states[login]; !ok {
			order = append(order, login)
		}
		states[login] = state
	}
	for _, n := range n.Reviewers.Nodes {
		if n.MergeRequestInteraction != nil {
			set(n.Username, reviewStates[n.MergeRequestInteraction.ReviewState])
		}
	}
	// An approver who isn't a reviewer still approved.
	for _, n := range n.ApprovedBy.Nodes {
		if _, ok := states[n.Username]; !ok {
			set(n.Username, changes.ReviewApproved)
		}
	}
	for _, login := range order {
		r.Reviewers = append(r.Reviewers, changes.Reviewer{Login: textutil.Line(login), State: states[login]})
	}
	for _, d := range n.Discussions.Nodes {
		var notes []note
		for _, n := range d.Notes.Nodes {
			if !n.System {
				notes = append(notes, n)
			}
		}
		if len(notes) == 0 {
			continue
		}
		// A resolvable discussion is a thread, on a diff line or not; any
		// other note is a comment.
		if !d.Resolvable {
			for _, n := range notes {
				r.Comments = append(r.Comments, n.comment())
			}
			continue
		}
		t := changes.Thread{Resolved: d.Resolved}
		if p := notes[0].Position; p != nil {
			t.Path = textutil.Line(p.FilePath)
			if line := p.NewLine; line != nil || p.OldLine != nil {
				if line == nil {
					line = p.OldLine
				}
				t.Line = *line
			}
		}
		for _, n := range notes {
			t.Comments = append(t.Comments, n.comment())
		}
		r.Threads = append(r.Threads, t)
	}
	if n.HeadPipeline != nil {
		for _, j := range n.HeadPipeline.Jobs.Nodes {
			check := changes.Check{Name: textutil.Line(j.Name), State: jobState(j.Status, j.AllowFailure)}
			if j.WebPath != "" {
				check.URL = textutil.Line("https://" + host + j.WebPath)
			}
			r.Checks = append(r.Checks, check)
		}
	}
	return r
}

// Review reads one merge request's description, pipeline jobs, reviewers,
// and discussions through the account's request queue, redacted before it is
// kept or shown. A read for the same head in the last minute is reused unless
// fresh; a failed read returns the last one kept with the error.
func (f *Forge) Review(ctx context.Context, number int, head string, fresh bool) (changes.Review, bool, error) {
	return f.reviews.Get(number, head, fresh, func() (changes.Review, error) { return f.readReview(ctx, number) })
}

func (f *Forge) readReview(ctx context.Context, number int) (changes.Review, error) {
	c, err := f.authenticate(ctx)
	if err != nil {
		return changes.Review{}, err
	}
	var result struct {
		Project *struct{ MergeRequest *reviewNode }
	}
	if err := f.query(ctx, c, reviewQuery, map[string]any{"path": f.o.Repo, "iid": strconv.Itoa(number)}, &result); err != nil {
		return changes.Review{}, err
	}
	if result.Project == nil || result.Project.MergeRequest == nil {
		return changes.Review{}, source.Refuse("not-found", "merge request unavailable")
	}
	r := result.Project.MergeRequest.review(f.o.Host)
	data, err := json.Marshal(r)
	if err == nil {
		r = changes.Review{}
		err = json.Unmarshal([]byte(f.o.Secrets.Redact(string(data))), &r)
	}
	if err != nil {
		return changes.Review{}, source.Refuse("rejected", "invalid merge request data")
	}
	return r, nil
}
