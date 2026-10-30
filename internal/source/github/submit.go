package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/textutil"
)

var reviewEvents = map[string]string{
	changes.EventComment: "COMMENT", changes.EventApprove: "APPROVE", changes.EventRequestChanges: "REQUEST_CHANGES",
}

// SubmitReview sends d as one review in a single GraphQL document, after a
// fresh read rejects conflicts (D15): the review with its line comments
// pinned to the head they were drafted on, so a head that moves after the
// read never shifts them, and each thread reply, which GitHub can't attach
// to a new review. The document is never retried. A response names which
// parts posted; the rest stay in the draft with GitHub's reasons, and a
// failure after dispatch without one marks the draft's outcome unknown.
func (f *Forge) SubmitReview(ctx context.Context, number int, d changes.Draft, event string) (changes.Draft, error) {
	reject := func(err error) (changes.Draft, error) {
		d.Error = textutil.Line(err.Error())
		return d, err
	}
	gql, ok := reviewEvents[event]
	if !ok {
		return reject(source.Refuse("rejected", "unknown review event "+strconv.Quote(event)))
	}
	c, err := f.s.authenticate(ctx)
	if err != nil {
		return reject(err)
	}
	if reason := f.reviewBlocked(c); reason != "" {
		return reject(source.Refuse("rejected", reason))
	}
	current, err := f.readReview(ctx, number)
	if err != nil {
		return reject(err)
	}
	if reason := d.Conflict(current); reason != "" {
		return reject(source.Refuse("conflict", reason))
	}
	var params, fields []string
	vars := map[string]any{}
	review := event != changes.EventComment || d.Body != "" || len(d.Comments) > 0
	if review {
		threads := make([]map[string]any, 0, len(d.Comments))
		for _, comment := range d.Comments {
			side := "RIGHT"
			if comment.Old {
				side = "LEFT"
			}
			threads = append(threads, map[string]any{"path": comment.Path, "line": comment.Line, "side": side, "body": comment.Body})
		}
		vars["review"] = map[string]any{"pullRequestId": current.ID, "commitOID": current.Head, "body": d.Body, "event": gql, "threads": threads}
		params = append(params, "$review:AddPullRequestReviewInput!")
		fields = append(fields, "review:addPullRequestReview(input:$review){pullRequestReview{id}}")
	}
	for i, reply := range d.Replies {
		alias := "r" + strconv.Itoa(i)
		vars[alias] = map[string]any{"pullRequestReviewThreadId": reply.Thread, "body": reply.Body}
		params = append(params, "$"+alias+":AddPullRequestReviewThreadReplyInput!")
		fields = append(fields, alias+":addPullRequestReviewThreadReply(input:$"+alias+"){comment{id}}")
	}
	query := "mutation(" + strings.Join(params, ",") + "){" + strings.Join(fields, " ") + "}"
	response, err := f.s.request(ctx, c, http.MethodPost, f.s.endpoint(), map[string]any{"query": query, "variables": vars})
	if err != nil {
		if unsent(err) {
			return reject(err)
		}
		d.Unknown = "GitHub review sent but unconfirmed; check the pull request before sending it again"
		return d, source.Refuse("unknown-outcome", d.Unknown)
	}
	var result struct {
		Data   map[string]json.RawMessage
		Errors []struct {
			Message string
			Path    []any
		}
	}
	if json.Unmarshal(response.Body, &result) != nil || result.Data == nil && len(result.Errors) == 0 {
		d.Unknown = "GitHub review sent but its response was unreadable; check the pull request before sending it again"
		return d, source.Refuse("unknown-outcome", d.Unknown)
	}
	posted := func(alias string) bool {
		raw, ok := result.Data[alias]
		return ok && len(raw) > 0 && string(raw) != "null"
	}
	left := changes.Draft{URL: d.URL}
	if review && !posted("review") {
		left.Body, left.Comments = d.Body, d.Comments
	}
	for i, reply := range d.Replies {
		if !posted("r" + strconv.Itoa(i)) {
			left.Replies = append(left.Replies, reply)
		}
	}
	if left.Empty() && (!review || posted("review")) {
		return left, nil
	}
	var reasons []string
	for _, e := range result.Errors {
		reasons = append(reasons, textutil.Line(f.s.o.Secrets.Redact(e.Message)))
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "GitHub returned no result")
	}
	left.Error = "GitHub refused part of the review: " + strings.Join(reasons, "; ")
	return left, source.Refuse("rejected", left.Error)
}
