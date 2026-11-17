package changes

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/brettinternet/backlog/internal/cache"
	"github.com/brettinternet/backlog/internal/textutil"
)

// CheckDetail is what the check view shows beyond a check's row: a check
// run's steps, annotations, and output, and a failed GitHub Actions job's log
// excerpt, or why there is none. A commit status's description is its
// Summary. Workflow is the Actions run the job belongs to, which re-runs name.
type CheckDetail struct {
	Title          string       `json:"title,omitempty"`
	Summary        string       `json:"summary,omitempty"`
	Workflow       int64        `json:"workflowRun,omitempty"`
	Steps          []Step       `json:"steps,omitempty"`
	Annotations    []Annotation `json:"annotations,omitempty"`
	Log            string       `json:"log,omitempty"`
	LogUnavailable string       `json:"logUnavailable,omitempty"`
}

// Step is one step of an Actions job, with its exact state word.
type Step struct {
	Number      int       `json:"number"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	URL         string    `json:"url,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitzero"`
	CompletedAt time.Time `json:"completedAt,omitzero"`
}

// Annotation is a check run's note on a file line. Level is failure,
// warning, or notice.
type Annotation struct {
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Level   string `json:"level"`
	Title   string `json:"title,omitempty"`
	Message string `json:"message"`
	URL     string `json:"url,omitempty"`
}

// CheckReader is a forge that reads a check run's detail on demand, for the
// check shown or a failing run bl changes --detail reports.
type CheckReader interface {
	// Check reads check's steps, annotations, output, and log excerpt,
	// redacted, reusing a completed run's unless fresh.
	Check(ctx context.Context, check Check, fresh bool) (CheckDetail, error)
}

// LoadCheck reads check's detail from the forge serving c's pull request, on
// a worker. A commit status, or a forge that can't read checks, has only what
// its row says.
func LoadCheck(ctx context.Context, c Change, check Check, forges []Forge, fresh bool) (CheckDetail, error) {
	f := forgeFor(c, forges)
	reader, ok := f.(CheckReader)
	if check.ID == "" || !ok {
		return check.CheckDetail, nil
	}
	d, err := reader.Check(ctx, check, fresh)
	if err != nil {
		return check.CheckDetail, err
	}
	return d, nil
}

// maxFailures bounds the failing runs bl changes --detail reads.
const maxFailures = 5

// AddFailures reads the detail of the first maxFailures failing check runs
// of each open pull request in cs, for bl changes --detail and MCP; a run
// whose read fails says why as its logUnavailable.
func AddFailures(ctx context.Context, cs []Change, forges []Forge) {
	for i := range cs {
		c := &cs[i]
		// CheckRuns is the forge's kept review; detail goes on a copy.
		c.CheckRuns = slices.Clone(c.CheckRuns)
		read := 0
		for j := range c.CheckRuns {
			check := &c.CheckRuns[j]
			if check.State != ChecksFailing || check.ID == "" || read == maxFailures || ctx.Err() != nil {
				continue
			}
			read++
			d, err := LoadCheck(ctx, *c, *check, forges, false)
			if err != nil {
				d.LogUnavailable = "check detail unavailable: " + err.Error()
			}
			check.CheckDetail = d
		}
	}
}

// Log excerpt bounds: the lines kept, each line's length, and the most read
// before giving up.
const (
	ExcerptLines    = 100
	ExcerptLineMax  = 2 << 10
	MaxLogBytes     = 32 << 20
	logErrorMarker  = "##[error]"
	logWarnMarker   = "##[warning]"
	logReadChunkMax = 64 << 10
)

// ErrLogTooLarge is a log past MaxLogBytes without an error line.
var ErrLogTooLarge = errors.New("log over 32 MiB")

var logTimestamp = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z ?`)

// Excerpt streams a job log, untrusted, keeping at most ExcerptLines lines:
// those up to and including the first ##[error] line, or the last ones when
// there is none. Each line loses its timestamp and terminal controls and is
// cut to ExcerptLineMax bytes. Past MaxLogBytes it stops with ErrLogTooLarge.
func Excerpt(r io.Reader) ([]string, error) {
	reader := bufio.NewReaderSize(io.LimitReader(r, MaxLogBytes+1), logReadChunkMax)
	var lines []string
	total := 0
	var line []byte
	keep := func() bool {
		text := string(line)
		if total == len(line) {
			text = strings.TrimPrefix(text, "\uFEFF")
		}
		text = textutil.Line(logTimestamp.ReplaceAllString(strings.TrimRight(text, "\r\n"), ""))
		if len(text) > ExcerptLineMax {
			text = truncateLine(text, ExcerptLineMax) + "…"
		}
		if len(lines) == ExcerptLines {
			lines = slices.Delete(lines, 0, 1)
		}
		lines = append(lines, text)
		line = line[:0]
		return strings.HasPrefix(text, logErrorMarker)
	}
	for {
		chunk, err := reader.ReadSlice('\n')
		total += len(chunk)
		if total > MaxLogBytes {
			return nil, ErrLogTooLarge
		}
		// A long line keeps only its first bytes, with room for the timestamp.
		if room := ExcerptLineMax + 64 - len(line); room > 0 {
			line = append(line, chunk[:min(len(chunk), room)]...)
		}
		switch {
		case err == nil:
			if keep() {
				return lines, nil
			}
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			if len(line) > 0 {
				keep()
			}
			return lines, nil
		default:
			return nil, err
		}
	}
}

// truncateLine cuts s to at most n bytes at a rune boundary.
func truncateLine(s string, n int) string {
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// LogLevel is an excerpt line's marker: error, warning, or "".
func LogLevel(line string) string {
	switch {
	case strings.HasPrefix(line, logErrorMarker):
		return "error"
	case strings.HasPrefix(line, logWarnMarker):
		return "warning"
	}
	return ""
}

// maxChecks bounds the completed check runs a forge keeps.
const maxChecks = 20

type checkEntry struct {
	Fetched time.Time
	Detail  CheckDetail
}

type checkCache struct {
	Repo   string
	Checks map[int64]checkEntry
}

// Checks keeps a forge's completed check runs' detail by check run ID, in
// memory and in a .checks.json beside its pull request list. A re-run is a
// new check run, so each attempt has its own entry.
type Checks struct {
	mu         sync.Mutex
	path, repo string
	kept       map[int64]checkEntry
}

// NewChecks keeps repo's check detail beside the pull request list at
// pullsPath, or only in memory when it is empty.
func NewChecks(pullsPath, repo string) *Checks {
	c := &Checks{repo: repo}
	if pullsPath != "" {
		c.path = strings.TrimSuffix(pullsPath, ".pulls.json") + ".checks.json"
	}
	return c
}

// Get reuses run's kept detail unless fresh, else reads it; read, which
// must redact, says whether the detail is final and may be kept.
func (c *Checks) Get(run int64, fresh bool, read func() (CheckDetail, bool, error)) (CheckDetail, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kept == nil {
		var kept checkCache
		if data, err := os.ReadFile(c.path); err == nil && json.Unmarshal(data, &kept) == nil && kept.Repo == c.repo {
			c.kept = kept.Checks
		}
		if c.kept == nil {
			c.kept = map[int64]checkEntry{}
		}
	}
	if kept, ok := c.kept[run]; ok && !fresh && run != 0 {
		return kept.Detail, nil
	}
	d, final, err := read()
	if err != nil || !final || run == 0 {
		return d, err
	}
	c.kept[run] = checkEntry{Fetched: time.Now(), Detail: d}
	if len(c.kept) > maxChecks {
		runs := make([]int64, 0, len(c.kept))
		for r := range c.kept {
			runs = append(runs, r)
		}
		slices.SortFunc(runs, func(a, b int64) int { return c.kept[b].Fetched.Compare(c.kept[a].Fetched) })
		for _, r := range runs[maxChecks:] {
			delete(c.kept, r)
		}
	}
	if c.path != "" {
		if data, err := json.Marshal(checkCache{Repo: c.repo, Checks: c.kept}); err == nil {
			cache.WriteFile(c.path, data)
		}
	}
	return d, nil
}
