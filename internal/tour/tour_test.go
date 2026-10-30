package tour

import (
	"strings"
	"testing"
)

// Parse accepts a tour an agent writes from the schema and refuses, with the
// reason, anything the TUI couldn't place or that could name a file outside
// the repository.
func TestParse(t *testing.T) {
	t.Parallel()
	ref := strings.Repeat("a", 40)
	valid := `{"ref":"` + ref + `","title":"Fix","steps":[{"file":"cmd/main.go","line":3,"description":"**Why**","criterion":1,"side":"old"}]}`
	if got, err := Parse([]byte(valid)); err != nil || got.Steps[0].Side != SideOld || got.Steps[0].Criterion != 1 {
		t.Fatalf("valid tour: %+v %v", got, err)
	}
	sel := func(line string, from, to string) string {
		return `{"ref":"` + ref + `","steps":[{"file":"a.go",` + line + `"selection":{"start":{"line":` + from + `,"character":1},"end":{"line":` + to + `,"character":9}},"description":"x"}]}`
	}
	for _, data := range []string{sel("", "4", "9"), sel(`"line":4,`, "4", "503")} {
		if got, err := Parse([]byte(data)); err != nil || got.Steps[0].Line != 4 || got.Steps[0].End() < 9 {
			t.Fatalf("selection %s: %+v %v", data, got, err)
		}
	}
	for _, c := range []struct{ data, want string }{
		{sel(`"line":5,`, "4", "9"), "step 1: line must equal selection.start.line"},
		{sel("", "9", "4"), "step 1: selection ends before it starts"},
		{sel("", "4", "504"), "step 1: selection covers more than 500 lines"},
		{sel("", "4.5", "9"), "cannot unmarshal number 4.5"},
		{sel("", "0", "9"), "step 1: selection.start.line"},
		{`{"ref":"` + ref + `","steps":[{"file":"a.go","line":1,"description":"x","criteria":1}]}`, "unknown field"},
		{`{"ref":"HEAD","steps":[{"file":"a.go","line":1,"description":"x"}]}`, "full head commit SHA"},
		{`{"ref":"` + ref + `","steps":[]}`, "no steps"},
		{`{"ref":"` + ref + `","steps":[{"file":"../a.go","line":1,"description":"x"}]}`, "step 1: file"},
		{`{"ref":"` + ref + `","steps":[{"file":"/etc/passwd","line":1,"description":"x"}]}`, "step 1: file"},
		{`{"ref":"` + ref + `","steps":[{"file":"a.go","line":0,"description":"x"}]}`, "step 1: line"},
		{`{"ref":"` + ref + `","steps":[{"file":"a.go","line":1,"description":" "}]}`, "step 1: description"},
		{`{"ref":"` + ref + `","steps":[{"file":"a.go","line":1,"description":"x","side":"left"}]}`, "step 1: side"},
		{valid + `{}`, "more than one JSON value"},
	} {
		if _, err := Parse([]byte(c.data)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Parse(%s) = %v, want %q", c.data, err, c.want)
		}
	}
}
