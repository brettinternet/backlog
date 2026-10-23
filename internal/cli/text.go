package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/textutil"
)

// Human output is aligned and sanitized. Color is added only on a terminal,
// never under NO_COLOR, and uses the terminal's own palette like the TUI.
type palette bool

func paletteFor(out io.Writer) palette {
	return palette(terminal(out) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb")
}

func (p palette) paint(sgr, text string) string {
	if !p || text == "" {
		return text
	}
	return "\x1b[" + sgr + "m" + text + "\x1b[0m"
}

func (p palette) category(c model.StatusCategory, text string) string {
	switch c {
	case model.Active:
		return p.paint("33", text)
	case model.Done:
		return p.paint("32", text)
	}
	return text
}

func (p palette) priority(v model.Priority, text string) string {
	switch v {
	case model.Urgent:
		return p.paint("1;31", text)
	case model.High:
		return p.paint("33", text)
	}
	return text
}

// pad left-aligns already painted text to width terminal cells of plain text.
func pad(painted, plain string, width int) string {
	return painted + strings.Repeat(" ", max(0, width-ansi.StringWidth(plain)))
}

// refs names items by bare ID unless the project has several sources;
// workspace rows also name their project.
type refs struct{ qualify bool }

func refsFor(cfg project.Config) refs {
	return refs{qualify: cfg.Workspace != nil || len(cfg.Sources) > 1}
}

func (r refs) item(i readItem) string {
	if i.Project != "" {
		source := i.Source
		if index := strings.LastIndex(source, "/"); index >= 0 {
			source = source[index+1:]
		}
		return textutil.Line(filepath.Base(i.Project) + " " + source + ":" + i.ID)
	}
	return r.ref(model.Ref{Source: i.Source, ID: i.ID}, i.Source)
}

// ref qualifies a reference stored on an item of local source when needed.
func (r refs) ref(ref model.Ref, local string) string {
	if ref.Source == "" || ref.Source == local && !r.qualify {
		return textutil.Line(ref.ID)
	}
	return textutil.Line(ref.Source + ":" + ref.ID)
}

// itemTable aligns ID, status, priority, and title across every row it will
// print, truncating rows to the terminal width (80 columns off-terminal).
type itemTable struct {
	out                  io.Writer
	p                    palette
	refs                 refs
	width                int
	id, status, priority int
}

func newItemTable(out io.Writer, cfg project.Config, groups ...[]readItem) itemTable {
	t := itemTable{out: out, p: paletteFor(out), refs: refsFor(cfg), width: outputWidth(out)}
	for _, items := range groups {
		for _, i := range items {
			t.id = max(t.id, ansi.StringWidth(t.refs.item(i)))
			t.status = max(t.status, ansi.StringWidth(textutil.Line(i.Status)))
			t.priority = max(t.priority, ansi.StringWidth(string(i.Priority)))
		}
	}
	return t
}

func (t itemTable) write(i readItem) error {
	id, status := t.refs.item(i), textutil.Line(i.Status)
	line := pad(id, id, t.id) + "  " + pad(t.p.category(i.Category, status), status, t.status)
	if t.priority > 0 {
		priority := string(i.Priority)
		line += "  " + pad(t.p.priority(i.Priority, priority), priority, t.priority)
	}
	line += "  " + textutil.Line(i.Title)
	_, err := fmt.Fprintln(t.out, ansi.Truncate(line, t.width, "…"))
	return err
}

// writeItemRow prints one item as a table row.
func writeItemRow(out io.Writer, cfg project.Config, i readItem) error {
	return newItemTable(out, cfg, []readItem{i}).write(i)
}

func (t itemTable) writeAll(items []readItem) error {
	for _, i := range items {
		if err := t.write(i); err != nil {
			return err
		}
	}
	return nil
}

// writeEmpty tells a person at a terminal that nothing matched; pipes get no
// output, so line counts stay exact.
func writeEmpty(out io.Writer, message string) error {
	if !terminal(out) {
		return nil
	}
	_, err := fmt.Fprintln(out, message)
	return err
}

func writeNext(out io.Writer, cfg project.Config, data nextData) error {
	table := newItemTable(out, cfg, data.Items)
	if err := table.writeAll(data.Items); err != nil {
		return err
	}
	if len(data.Items) == 0 && len(data.Explanations) == 0 {
		return writeEmpty(out, "No ready items.")
	}
	if len(data.Explanations) == 0 {
		return nil
	}
	if len(data.Items) > 0 {
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}
	p, names := table.p, refsFor(cfg)
	id, state := 0, 0
	for _, e := range data.Explanations {
		id = max(id, ansi.StringWidth(names.ref(e.Ref, e.Ref.Source)))
		state = max(state, len(e.Readiness))
	}
	for _, e := range data.Explanations {
		ref, readiness := names.ref(e.Ref, e.Ref.Source), string(e.Readiness)
		colored := readiness
		switch e.Readiness {
		case engine.Ready:
			colored = p.paint("32", readiness)
		case engine.Blocked:
			colored = p.paint("31", readiness)
		case engine.Waiting:
			colored = p.paint("33", readiness)
		}
		// Reasons are the point of --explain, so these lines wrap rather than truncate.
		if _, err := fmt.Fprintln(out, pad(ref, ref, id)+"  "+pad(colored, readiness, state)+"  "+textutil.Line(e.Text())); err != nil {
			return err
		}
	}
	return nil
}

// sectionTitles are the native file headings, in file order.
var sectionTitles = [][2]string{
	{"description", "Description"}, {"criteria", "Acceptance criteria"}, {"dod", "Definition of done"},
	{"plan", "Plan"}, {"notes", "Notes"}, {"summary", "Summary"}, {"comments", "Comments"},
}

// writeShow prints the item's set fields, aligned, then its nonempty sections.
func writeShow(out io.Writer, cfg project.Config, i model.Item, sections map[string]string) error {
	p, names := paletteFor(out), refsFor(cfg)
	ref := func(r *model.Ref) string {
		if r == nil {
			return ""
		}
		return names.ref(*r, i.Ref.Source)
	}
	list := func(values []model.Ref) string {
		out := make([]string, len(values))
		for n, v := range values {
			out[n] = names.ref(v, i.Ref.Source)
		}
		return strings.Join(out, ", ")
	}
	timestamp := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format(time.RFC3339)
	}
	fields := [][2]string{
		{"status", i.Status}, {"priority", string(i.Priority)}, {"type", i.Type},
		{"assignees", strings.Join(i.Assignees, ", ")}, {"labels", strings.Join(i.Labels, ", ")},
		{"milestone", i.Milestone}, {"due", i.Due}, {"parent", ref(i.Parent)},
		{"children", list(i.Children)}, {"blocked by", list(i.BlockedBy)}, {"blocks", list(i.Blocks)},
		{"related", list(i.Related)}, {"duplicate of", ref(i.DuplicateOf)},
		{"refs", strings.Join(i.Refs, ", ")}, {"files", strings.Join(i.Files, ", ")},
		{"created", timestamp(i.Created)}, {"updated", timestamp(i.Updated)}, {"url", i.URL},
	}
	label := 0
	for _, f := range fields {
		if f[1] != "" {
			label = max(label, len(f[0]))
		}
	}
	var b strings.Builder
	b.WriteString(p.paint("1", names.ref(i.Ref, i.Ref.Source)+"  "+textutil.Line(i.Title)) + "\n")
	for _, f := range fields {
		if f[1] == "" {
			continue
		}
		value := textutil.Line(f[1])
		switch f[0] {
		case "status":
			value = p.category(i.Category, value)
		case "priority":
			value = p.priority(i.Priority, value)
		}
		b.WriteString(fmt.Sprintf("%-*s  %s\n", label, f[0], value))
	}
	for _, s := range sectionTitles {
		if text := sections[s[0]]; text != "" {
			b.WriteString("\n" + p.paint("1", "## "+s[1]) + "\n" + textutil.Sanitize(text) + "\n")
		}
	}
	_, err := io.WriteString(out, b.String())
	return err
}

// writeDoctor prints one aligned line per check; a fix follows only the
// checks that need action.
func writeDoctor(out io.Writer, checks []doctorCheck) error {
	p, name := paletteFor(out), 0
	for _, c := range checks {
		name = max(name, len(c.Name))
	}
	var b strings.Builder
	for _, c := range checks {
		status := c.Status
		switch status {
		case "pass":
			status = p.paint("32", status)
		case "warn":
			status = p.paint("33", status)
		case "fail":
			status = p.paint("31", status)
		}
		b.WriteString(fmt.Sprintf("%s  %-*s  %s\n", status, name, c.Name, textutil.Line(c.Message)))
		if c.Status != "pass" && c.Fix != "" {
			b.WriteString(strings.Repeat(" ", name+8) + "Fix: " + textutil.Line(c.Fix) + "\n")
		}
	}
	_, err := io.WriteString(out, b.String())
	return err
}
