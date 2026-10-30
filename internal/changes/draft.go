package changes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/brettinternet/backlog/internal/cache"
)

// Review events a submission sends.
const (
	EventComment        = "comment"
	EventApprove        = "approve"
	EventRequestChanges = "request-changes"
)

// Draft is a pending review of one pull request, kept in XDG state until it
// is submitted or discarded so it survives restarts. Unknown says a
// submission was sent but its outcome is unknown, so it is never resent;
// Error is the last submission's refusal or conflict.
type Draft struct {
	URL      string        `json:"url"`
	Body     string        `json:"body,omitempty"`
	Comments []LineComment `json:"comments,omitempty"`
	Replies  []Reply       `json:"replies,omitempty"`
	Unknown  string        `json:"unknown,omitempty"`
	Error    string        `json:"error,omitempty"`
}

// LineComment is a drafted comment on a line of a file at Head, on the old
// side when Old.
type LineComment struct {
	Head string `json:"head"`
	Path string `json:"path"`
	Old  bool   `json:"old,omitempty"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// Where is the comment's file and line, with − for the old side.
func (c LineComment) Where() string {
	side := ""
	if c.Old {
		side = "−"
	}
	return c.Path + ":" + side + strconv.Itoa(c.Line)
}

// Reply is a drafted reply to the review thread Thread on Path's Line.
type Reply struct {
	Thread string `json:"thread"`
	Path   string `json:"path"`
	Line   int    `json:"line,omitempty"`
	Body   string `json:"body"`
}

// Empty reports a draft with nothing to send.
func (d Draft) Empty() bool {
	return strings.TrimSpace(d.Body) == "" && len(d.Comments) == 0 && len(d.Replies) == 0
}

// Stale counts the line comments drafted on another head than head, which
// never post: their lines may be other lines now.
func (d Draft) Stale(head string) int {
	n := 0
	for _, c := range d.Comments {
		if c.Head != head {
			n++
		}
	}
	return n
}

// Conflict is why d can't be sent against the pull request as current reads
// it: a line comment on another head, or a reply to a thread that is gone.
func (d Draft) Conflict(current Review) string {
	if n := d.Stale(current.Head); n > 0 {
		return "the pull request head moved since " + plural(n, "line comment") + " were drafted; edit or delete them"
	}
	for _, r := range d.Replies {
		if !slices.ContainsFunc(current.Threads, func(t Thread) bool { return t.ID == r.Thread }) {
			return "the thread on " + r.Path + " is gone; delete the reply"
		}
	}
	return ""
}

// Summary counts what d holds, such as "comment · 2 line comments".
func (d Draft) Summary() string {
	var parts []string
	if strings.TrimSpace(d.Body) != "" {
		parts = append(parts, "comment")
	}
	if len(d.Comments) > 0 {
		parts = append(parts, plural(len(d.Comments), "line comment"))
	}
	if len(d.Replies) > 0 {
		parts = append(parts, plural(len(d.Replies), "reply"))
	}
	if len(parts) == 0 {
		return "empty"
	}
	return strings.Join(parts, " · ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if strings.HasSuffix(noun, "y") {
		return strconv.Itoa(n) + " " + strings.TrimSuffix(noun, "y") + "ies"
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// WithBody is d with its review comment set to body. An edit clears the last
// refusal, since it answers it.
func (d Draft) WithBody(body string) Draft {
	d.Body, d.Error = strings.TrimSpace(body), ""
	return d
}

// WithComment is d with the comment on c's line set to c, or removed when
// c's body is blank.
func (d Draft) WithComment(c LineComment) Draft {
	c.Body = strings.TrimSpace(c.Body)
	d.Comments = slices.DeleteFunc(slices.Clone(d.Comments), func(o LineComment) bool {
		return o.Head == c.Head && o.Path == c.Path && o.Old == c.Old && o.Line == c.Line
	})
	if c.Body != "" {
		d.Comments = append(d.Comments, c)
	}
	d.Error = ""
	return d
}

// WithReply is d with the reply to r's thread set to r, or removed when r's
// body is blank.
func (d Draft) WithReply(r Reply) Draft {
	r.Body = strings.TrimSpace(r.Body)
	d.Replies = slices.DeleteFunc(slices.Clone(d.Replies), func(o Reply) bool { return o.Thread == r.Thread })
	if r.Body != "" {
		d.Replies = append(d.Replies, r)
	}
	d.Error = ""
	return d
}

// draftPath is url's draft file under the state directory.
func draftPath(stateDir, url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(stateDir, "reviews", hex.EncodeToString(sum[:16])+".json")
}

// LoadDraft reads url's draft from the state directory; none is an empty
// draft.
func LoadDraft(stateDir, url string) (Draft, error) {
	if stateDir == "" || url == "" {
		return Draft{}, errors.New("review drafts need a state directory and a pull request URL")
	}
	data, err := os.ReadFile(draftPath(stateDir, url))
	if errors.Is(err, os.ErrNotExist) {
		return Draft{URL: url}, nil
	}
	if err != nil {
		return Draft{}, err
	}
	var d Draft
	if err := json.Unmarshal(data, &d); err != nil || d.URL != url {
		return Draft{}, errors.New("review draft is unreadable: " + draftPath(stateDir, url))
	}
	return d, nil
}

// SaveDraft keeps d in the state directory, or removes it once it has
// nothing to send and nothing to report.
func SaveDraft(stateDir string, d Draft) error {
	if stateDir == "" || d.URL == "" {
		return errors.New("review drafts need a state directory and a pull request URL")
	}
	path := draftPath(stateDir, d.URL)
	if d.Empty() && d.Unknown == "" && d.Error == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return cache.WriteFile(path, data)
}

// ReviewWriter is a forge that posts reviews (D10): others leave review
// actions disabled.
type ReviewWriter interface {
	// SubmitReview sends d as one review with event, after a fresh read
	// rejects conflicts, in one non-retried call. It returns what is left to
	// send, with Unknown set when the outcome is unknown or Error when the
	// forge refused part of it.
	SubmitReview(ctx context.Context, number int, d Draft, event string) (Draft, error)
}

// SubmitReview sends c's draft through the forge serving its pull request.
// A draft whose last submission's outcome is unknown is never sent again.
func SubmitReview(ctx context.Context, c Change, forges []Forge, d Draft, event string) (Draft, error) {
	w, ok := forgeFor(c, forges).(ReviewWriter)
	switch {
	case c.Number == 0 || !ok:
		return d, errors.New("no forge posts reviews to " + c.URL)
	case d.Unknown != "":
		return d, errors.New("the last submission's outcome is unknown; check the pull request, then discard the draft")
	case d.Empty() && event == EventComment:
		return d, errors.New("nothing to submit")
	}
	return w.SubmitReview(ctx, c.Number, d, event)
}
