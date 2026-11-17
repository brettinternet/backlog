package changes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// Catches an excerpt that runs past the first error, keeps a timestamp or a
// terminal sequence from an untrusted log, holds a huge line or the whole
// log in memory, or reads a log without bound.
func TestExcerpt(t *testing.T) {
	var log strings.Builder
	log.WriteString("\uFEFF2026-01-01T00:00:00.1234567Z first\r\n")
	for i := range 150 {
		log.WriteString("2026-01-01T00:00:01Z line " + strconv.Itoa(i) + "\n")
	}
	log.WriteString("2026-01-01T00:00:02Z \x1b]52;c;cGF5bG9hZA==\x07" + strings.Repeat("é", 4000) + "\n")
	log.WriteString("2026-01-01T00:00:03Z ##[error]Process completed with exit code 1.\n")
	log.WriteString("after the error\n")
	lines, err := Excerpt(strings.NewReader(log.String()), nil)
	if err != nil || len(lines) != ExcerptLines || lines[len(lines)-1] != "##[error]Process completed with exit code 1." || lines[0] != "line 52" {
		t.Fatalf("excerpt: %v %d lines, first %q, last %q", err, len(lines), lines[0], lines[len(lines)-1])
	}
	long := lines[len(lines)-2]
	if strings.ContainsAny(long, "\x1b\x07") || len(long) > ExcerptLineMax+len("…") || !utf8.ValidString(long) || !strings.HasSuffix(long, "…") {
		t.Fatalf("long line: %d bytes %q", len(long), long[:40])
	}
	// Without an error line, the last lines show; past 32 MiB, none.
	lines, err = Excerpt(strings.NewReader("\uFEFFonly\nlast"), nil)
	if err != nil || strings.Join(lines, "|") != "only|last" {
		t.Fatalf("short log: %v %q", err, lines)
	}
	if _, err := Excerpt(io.LimitReader(repeatReader('x'), MaxLogBytes+1), nil); !errors.Is(err, ErrLogTooLarge) {
		t.Fatalf("huge log: %v", err)
	}
}

type repeatReader byte

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
		if i%1000 == 999 {
			p[i] = '\n'
		}
	}
	return len(p), nil
}

func FuzzExcerpt(f *testing.F) {
	f.Add("2026-01-01T00:00:00Z ok\n##[error]boom\nafter")
	f.Add("\uFEFF\x1b[31mred\r\n" + strings.Repeat("y", 5000))
	f.Fuzz(func(t *testing.T, log string) {
		lines, err := Excerpt(strings.NewReader(log), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(lines) > ExcerptLines {
			t.Fatalf("%d lines", len(lines))
		}
		for i, line := range lines {
			if len(line) > ExcerptLineMax+len("…") || strings.ContainsAny(line, "\x1b\x07\n\r") {
				t.Fatalf("line %d: %q", i, line)
			}
			if strings.HasPrefix(line, "##[error]") && i != len(lines)-1 {
				t.Fatalf("excerpt runs past the error line at %d", i)
			}
		}
	})
}

type checkForge struct {
	reviewForge
	reads *int
}

func (f checkForge) Check(_ context.Context, check Check, _ bool) (CheckDetail, error) {
	*f.reads++
	if check.ID == "CR_0" {
		return CheckDetail{}, errors.New("rate limited")
	}
	return CheckDetail{Steps: []Step{{Number: 1, Name: "Run lint", Status: CheckFailed}}, Log: "##[error]boom"}, nil
}

// Catches bl changes --detail reading every failing run of a large matrix,
// reading passing or status-only checks, or dropping a failed read silently
// instead of saying why in the JSON consumers read.
func TestAddFailures(t *testing.T) {
	reads := 0
	var checks []Check
	for i := range 7 {
		checks = append(checks, Check{ID: "CR_" + strconv.Itoa(i), Name: "job", State: ChecksFailing})
	}
	checks = append(checks, Check{ID: "CR_9", State: ChecksPassing}, Check{Name: "status", State: ChecksFailing})
	cs := []Change{{URL: "https://github.com/o/r/pull/1", CheckRuns: checks}}
	AddFailures(context.Background(), cs, []Forge{checkForge{reads: &reads}})
	got := cs[0].CheckRuns
	// The forge's kept review shares checks; its runs must stay as read.
	if reads != maxFailures || !strings.Contains(got[0].LogUnavailable, "rate limited") || got[1].Log == "" || got[5].Log != "" || got[7].Log != "" || checks[1].Log != "" {
		t.Fatalf("%d reads: %+v", reads, got)
	}
	data, err := json.Marshal(got[1])
	if err != nil || !strings.Contains(string(data), `"steps":[{"number":1,"name":"Run lint","status":"failed"}]`) || !strings.Contains(string(data), `"log":"##[error]boom"`) {
		t.Fatalf("json %s %v", data, err)
	}
}
