package changes

import (
	"cmp"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/brettinternet/backlog/internal/model"
)

// line collects what one change is made of before it is linked to items.
type line struct {
	key     string
	branch  *Branch
	name    string
	pr      *PullRequest
	commits []Commit
	links   map[model.Ref]evidence
	stage   string // set only for an unresolved refs URL
	reason  string
	url     string
}

// evidence is how a line links to one item. Ambiguous marks a link through
// an ID that names more than one item.
type evidence struct {
	kind      string
	ambiguous int
}

// Link derives each item's changes from d. It is linear in the items,
// branches, commits, and pull requests, so the projection worker can run it
// on every refresh.
func Link(items []Item, d Data) map[model.Ref][]Change {
	l := linker{ids: map[string][]model.Ref{}, nodes: map[string]model.Ref{}, lines: map[string]*line{}, data: d}
	for _, item := range items {
		id := strings.ToLower(item.Ref.ID)
		l.ids[id] = append(l.ids[id], item.Ref)
		if item.NodeID != "" {
			l.nodes[item.NodeID] = item.Ref
		}
	}
	for i := range d.Branches {
		b := &d.Branches[i]
		l.line("branch:"+b.Name, b.Name).branch = b
	}
	prs := slices.Clone(d.PullRequests)
	// The newest pull request on a head branch is that branch's change.
	slices.SortStableFunc(prs, func(a, b PullRequest) int { return b.Created.Compare(a.Created) })
	prBySHA, prByNumber, prByURL := map[string]*line{}, map[int]*line{}, map[string]*line{}
	for i := range prs {
		pr := &prs[i]
		// Only origin's pull requests are numbered as its merge subjects are,
		// and only those not from a fork have their head among its branches.
		origin := d.Repo != "" && strings.EqualFold(pr.Repo, d.Repo)
		local := origin && !pr.CrossRepository
		key := "pr:" + pr.URL
		name := ""
		if local {
			name = pr.Head
			if existing := l.lines["branch:"+pr.Head]; existing == nil || existing.pr == nil {
				key = "branch:" + pr.Head
			}
		}
		ln := l.line(key, name)
		ln.pr = pr
		if pr.MergeSHA != "" {
			prBySHA[pr.MergeSHA] = ln
		}
		if origin {
			prByNumber[pr.Number] = ln
		}
		prByURL[pullKey(pr.URL)] = ln
		if !local {
			l.branchIDs(ln, pr.Head, "branch")
		}
		for _, token := range titleTokens(pr.Title) {
			l.addID(ln, token, "title", true)
		}
		for _, token := range closingTokens(pr.Body) {
			l.addID(ln, token, "closes", true)
		}
		for _, node := range pr.Closes {
			if ref, ok := l.nodes[node]; ok {
				ln.link(ref, evidence{kind: "provider"})
			}
		}
	}
	commitLine := map[string]*line{}
	for _, c := range d.Landed {
		var ln *line
		switch {
		case prBySHA[c.SHA] != nil:
			ln = prBySHA[c.SHA]
		case c.PR != 0 && prByNumber[c.PR] != nil:
			ln = prByNumber[c.PR]
		case c.Branch != "":
			ln = l.line("branch:"+c.Branch, c.Branch)
		}
		if len(c.Trailers) > 0 && ln == nil {
			ln = l.line("trailer:"+strings.ToLower(c.Trailers[0]), "")
		}
		if ln == nil {
			commitLine[c.SHA] = nil
			continue
		}
		ln.commits = append(ln.commits, c)
		commitLine[c.SHA] = ln
		for _, value := range c.Trailers {
			l.addID(ln, strings.ToLower(value), "trailer", false)
		}
	}
	for _, ln := range l.lines {
		if ln.name != "" {
			kind := "branch"
			if ln.branch == nil && ln.pr == nil {
				kind = "merge"
			}
			l.branchIDs(ln, ln.name, kind)
		}
	}
	for _, item := range items {
		for _, raw := range item.Refs {
			l.explicit(item.Ref, raw, prByURL, commitLine)
		}
	}
	out := map[model.Ref][]Change{}
	for _, key := range l.order {
		ln := l.lines[key]
		if len(ln.links) == 0 {
			continue
		}
		base := l.change(ln)
		for ref, e := range ln.links {
			c := base
			c.Link = e.kind
			if e.ambiguous > 1 {
				c.State, c.Checks = StateUnknown, ""
				c.Reason = fmt.Sprintf("%s matches %d items", ref.ID, e.ambiguous)
			}
			out[ref] = append(out[ref], c)
		}
	}
	for ref := range out {
		slices.SortFunc(out[ref], order)
	}
	return out
}

type linker struct {
	ids   map[string][]model.Ref
	nodes map[string]model.Ref
	lines map[string]*line
	order []string
	data  Data
}

func (l *linker) line(key, name string) *line {
	if ln := l.lines[key]; ln != nil {
		return ln
	}
	ln := &line{key: key, name: name, links: map[model.Ref]evidence{}}
	l.lines[key] = ln
	l.order = append(l.order, key)
	return ln
}

// link keeps the first unambiguous evidence for ref.
func (ln *line) link(ref model.Ref, e evidence) {
	if old, ok := ln.links[ref]; !ok || old.ambiguous > 1 && e.ambiguous <= 1 {
		ln.links[ref] = e
	}
}

// addID links ln to every item whose lowercase ID is token. Prose (titles
// and bodies) links only IDs with a letter, so "#45" or "2024" never does.
func (l *linker) addID(ln *line, token, kind string, prose bool) {
	if prose && !strings.ContainsFunc(token, unicode.IsLetter) {
		return
	}
	refs := l.ids[token]
	for _, ref := range refs {
		ln.link(ref, evidence{kind: kind, ambiguous: len(refs)})
	}
}

// branchIDs links a branch whose name, or last "/" segment, starts with an
// item's lowercase ID followed by "-" or the end: bl-12-slug and
// feat/bl-12-slug link to BL-12.
func (l *linker) branchIDs(ln *line, name, kind string) {
	name = strings.ToLower(name)
	forms := []string{name}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		forms = append(forms, name[i+1:])
	}
	for _, form := range forms {
		if token := l.prefix(form); token != "" {
			l.addID(ln, token, kind, false)
		}
	}
}

// prefix is the longest item ID that s starts with at a "-" boundary.
func (l *linker) prefix(s string) string {
	for end := len(s); end > 0; end = strings.LastIndex(s[:end], "-") {
		if _, ok := l.ids[s[:end]]; ok {
			return s[:end]
		}
	}
	return ""
}

var (
	// pullURL is a GitHub pull request or a GitLab merge request, whose
	// project path may nest groups.
	pullURL   = regexp.MustCompile(`^https?://([^/]+)/([^/]+/[^/]+/pull|.+?/-/merge_requests)/(\d+)`)
	commitURL = regexp.MustCompile(`^https?://[^/]+/.+/commit/([0-9a-fA-F]{7,40})(?:[/?#]|$)`)
)

// pullKey identifies a pull request URL whatever its case or suffix.
func pullKey(raw string) string {
	m := pullURL.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1]+"/"+m[2]) + "#" + m[3]
}

// explicit links a refs URL naming a pull request or commit. One bl can't
// resolve shows as unknown rather than disappearing.
func (l *linker) explicit(ref model.Ref, raw string, prs map[string]*line, commits map[string]*line) {
	if u, err := url.Parse(strings.TrimSpace(raw)); err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return
	}
	if key := pullKey(raw); key != "" {
		if ln := prs[key]; ln != nil {
			ln.link(ref, evidence{kind: "ref"})
			return
		}
		ln := l.line("ref:"+key, "")
		ln.stage, ln.url, ln.reason = StagePR, strings.TrimSpace(raw), "pull request not in forge data"
		ln.link(ref, evidence{kind: "ref"})
		return
	}
	m := commitURL.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return
	}
	sha := strings.ToLower(m[1])
	for _, c := range l.data.Landed {
		if !strings.HasPrefix(c.SHA, sha) {
			continue
		}
		ln := commits[c.SHA]
		if ln == nil {
			ln = l.line("commit:"+c.SHA, "")
			ln.commits = []Commit{c}
			commits[c.SHA] = ln
		}
		ln.link(ref, evidence{kind: "ref"})
		return
	}
	ln := l.line("ref:"+sha, "")
	ln.stage, ln.url = StageLanded, strings.TrimSpace(raw)
	ln.reason = "commit not in the first-parent history of " + cmp.Or(l.data.Default, "the default branch")
	ln.link(ref, evidence{kind: "ref"})
}

// change builds a line's change before it is linked to each item.
func (l *linker) change(ln *line) Change {
	c := Change{ID: ln.key, Branch: ln.name, URL: ln.url, Reason: ln.reason, dir: l.data.Directory}
	if ln.stage != "" {
		c.Stage, c.State = ln.stage, StateUnknown
		return c
	}
	b, pr := ln.branch, ln.pr
	local := ""
	if b != nil {
		local = cmp.Or(b.Local, b.Remote)
		c.Head, c.Worktree = local, b.Worktree
		c.ref, c.base = b.RemoteRef, l.data.DefaultRef
		if b.Local != "" {
			c.ref = "refs/heads/" + b.Name
		}
		if !b.WorktreeCreated.IsZero() {
			c.Events = append(c.Events, Event{Kind: "worktree", Time: b.WorktreeCreated})
		}
		if !b.Created.IsZero() {
			c.Events = append(c.Events, Event{Kind: "branch", Time: b.Created})
		}
		if l.data.Web != "" && b.Remote != "" {
			c.URL = l.data.Web + "/tree/" + b.Name
		}
	}
	switch {
	case len(ln.commits) > 0:
		c.Stage, c.State = StageLanded, StateMerged
	case pr != nil:
		c.Stage, c.State = StagePR, pr.State
		if pr.Draft && pr.State == StateOpen {
			c.State = StateDraft
		}
	case b != nil && (b.Remote != "" || b.Worktree == ""):
		c.Stage, c.State = StageBranch, StateOpen
	default:
		c.Stage, c.State = StageWorktree, StateOpen
	}
	if pr != nil {
		c.Title, c.URL, c.Repo, c.Number = pr.Title, pr.URL, pr.Repo, pr.Number
		c.Additions, c.Deletions, c.Review, c.Checks = pr.Additions, pr.Deletions, pr.Review, pr.Checks
		c.Head = cmp.Or(c.Head, pr.HeadSHA)
		c.Events = append(c.Events, Event{Kind: "opened", Time: pr.Created})
		if !pr.Merged.IsZero() {
			c.Events = append(c.Events, Event{Kind: "merged", Time: pr.Merged})
		} else if !pr.Closed.IsZero() {
			c.Events = append(c.Events, Event{Kind: "closed", Time: pr.Closed})
		}
		open := pr.State == StateOpen
		switch {
		case pr.Stale != "":
			if c.Stage == StagePR {
				c.State, c.Reason = StateStale, pr.Stale
			}
			if c.Checks != "" {
				c.Checks = ChecksStale
			}
		case open && c.Checks != "" && local != "" && pr.HeadSHA != "" && local != pr.HeadSHA:
			c.Checks = ChecksStale
			c.Reason = "checks are for " + short(pr.HeadSHA) + ", not the branch head " + short(local)
		}
	}
	if len(ln.commits) > 0 {
		first := ln.commits[0]
		c.Head = cmp.Or(c.Head, first.SHA)
		c.Title = cmp.Or(c.Title, first.Subject)
		c.Commits = ln.commits
		if c.URL == "" && l.data.Web != "" {
			c.URL = l.data.Web + "/commit/" + first.SHA
		}
	}
	c.Title = cmp.Or(c.Title, ln.name)
	for _, e := range c.Events {
		c.Updated = latest(c.Updated, e.Time)
	}
	for _, commit := range c.Commits {
		c.Updated = latest(c.Updated, commit.Time)
	}
	if pr != nil {
		c.Updated = latest(c.Updated, pr.Updated)
	}
	return c
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func short(sha string) string { return sha[:min(len(sha), 7)] }

// titleTokens are the words of a title that could be item IDs.
func titleTokens(title string) []string {
	var out []string
	for word := range strings.FieldsFuncSeq(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.'
	}) {
		if word = strings.Trim(word, "-_."); word != "" {
			out = append(out, word)
		}
	}
	return out
}

var closing = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?|refs)\b:?[ \t]+([A-Za-z0-9][A-Za-z0-9_.-]*)`)

// closingTokens are the IDs a body names after a closing keyword: Closes,
// Fixes, Resolves, or Refs.
func closingTokens(body string) []string {
	var out []string
	for _, m := range closing.FindAllStringSubmatch(body, -1) {
		out = append(out, strings.ToLower(strings.TrimRight(m[1], "-_.")))
	}
	return out
}

// Label is a change's short name: #45, or !45 for a GitLab merge request,
// or its branch, or a commit's SHA.
func Label(c Change) string {
	switch {
	case c.Number > 0:
		return number(c.URL) + strconv.Itoa(c.Number)
	case c.Branch != "":
		return c.Branch
	case len(c.Commits) > 0:
		return short(c.Commits[0].SHA)
	}
	if m := pullURL.FindStringSubmatch(c.URL); m != nil {
		return number(c.URL) + m[3]
	}
	if m := commitURL.FindStringSubmatch(c.URL); m != nil {
		return short(strings.ToLower(m[1]))
	}
	return c.ID
}

// number is the prefix that numbers url's pull or merge request.
func number(url string) string {
	if strings.Contains(url, "/-/merge_requests/") {
		return "!"
	}
	return "#"
}
