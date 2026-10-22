package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	urfave "github.com/urfave/cli/v3"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/textutil"
)

type readItem struct {
	Source   string               `json:"source"`
	ID       string               `json:"id"`
	Title    string               `json:"title"`
	Status   string               `json:"status"`
	Category model.StatusCategory `json:"category"`
	Priority model.Priority       `json:"priority"`
	Version  string               `json:"version"`
}

func itemData(i model.Item) readItem {
	return readItem{i.Ref.Source, i.Ref.ID, i.Title, i.Status, i.Category, i.Priority, i.Version}
}

type readGroup struct {
	Value string     `json:"value"`
	Items []readItem `json:"items"`
}
type listData struct {
	Groups []readGroup `json:"groups"`
}
type showData struct {
	Item     readItem          `json:"item"`
	Metadata map[string]any    `json:"metadata"`
	Sections map[string]string `json:"sections"`
	Criteria []model.Criterion `json:"criteria,omitempty"`
	DoD      []model.Criterion `json:"dod,omitempty"`
}
type nextExplanation struct {
	engine.Explanation
	Included bool `json:"included"`
}
type nextData struct {
	Items        []readItem        `json:"items"`
	Explanations []nextExplanation `json:"explanations,omitempty"`
}

func readCommands(directory *string, out, errOut io.Writer) []*urfave.Command {
	load := func(ctx context.Context, cfg project.Config, archived bool) (engine.ReadSnapshot, error) {
		snapshot, err := engine.LoadReads(ctx, cfg, archived)
		if err != nil {
			return snapshot, &Error{Reason: "unavailable", Message: err.Error()}
		}
		for _, warning := range append(cfg.Discovery.Warnings, snapshot.Warnings...) {
			fmt.Fprintf(errOut, "bl: warning: %s\n", textutil.Line(warning))
		}
		for _, detected := range cfg.Discovery.Detected {
			fmt.Fprintf(errOut, "bl: detected %s (read-only); use bl import or an explicit source\n", textutil.Line(detected.Type))
		}
		return snapshot, nil
	}
	limitFlag := func(n int) urfave.Flag {
		return &urfave.IntFlag{Name: "n", Value: n, Usage: "Maximum items (0 means all)"}
	}
	return []*urfave.Command{
		{Name: "ls", Usage: "List items matching QUERY", ArgsUsage: "[QUERY]", Flags: []urfave.Flag{
			&urfave.StringFlag{Name: "view", Usage: "Saved view name"}, &urfave.StringFlag{Name: "sort", Usage: "Query sort keys"}, &urfave.StringFlag{Name: "group", Usage: "Query grouping field"}, limitFlag(0),
		}, Action: func(ctx context.Context, cmd *urfave.Command) error {
			if cmd.Int("n") < 0 {
				return &Error{Reason: "usage", Message: "-n must be nonnegative"}
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			text := strings.Join(cmd.Args().Slice(), " ")
			if name := cmd.String("view"); name != "" {
				found := false
				for _, v := range cfg.Views {
					if v.Name == name {
						found = true
						text = v.Query + " " + text
						if v.Sort != "" {
							text += " sort:" + v.Sort
						}
						if v.Group != "" {
							text += " group:" + v.Group
						}
						break
					}
				}
				if !found {
					return &Error{Reason: "usage", Message: "unknown view " + name}
				}
			}
			if cmd.IsSet("sort") {
				text += " sort:" + cmd.String("sort")
			}
			if cmd.IsSet("group") {
				text += " group:" + cmd.String("group")
			}
			// query.md: with no is: term, ls adds is:open; is:archived loads archive/.
			parsed, err := query.Parse(text, query.Options{})
			if err != nil {
				return &Error{Reason: "usage", Message: err.Error()}
			}
			hasIs, archived := false, false
			for _, t := range parsed.Terms {
				if t.Field != "is" {
					continue
				}
				hasIs = true
				for _, v := range t.Values {
					archived = archived || !t.Negated && strings.EqualFold(v.Text, "archived")
				}
			}
			if !hasIs {
				text += " is:open"
			}
			s, err := load(ctx, cfg, archived)
			if err != nil {
				return err
			}
			q, err := s.Parse(text)
			if err != nil {
				return &Error{Reason: "usage", Message: err.Error()}
			}
			items := []model.Item{}
			for _, i := range s.Query.Items {
				if q.Match(i, s.Query, nil) {
					items = append(items, i)
				}
			}
			s.Query.Sort(items, q.Sort)
			if n := cmd.Int("n"); n > 0 && len(items) > n {
				items = items[:n]
			}
			data := listData{Groups: []readGroup{}}
			for _, g := range s.Query.Group(items, q.Group) {
				group := readGroup{Value: g.Value, Items: []readItem{}}
				for _, i := range g.Items {
					group.Items = append(group.Items, itemData(i))
				}
				data.Groups = append(data.Groups, group)
			}
			if cmd.Bool("json") {
				return success(out, "ls", data)
			}
			for _, g := range data.Groups {
				if q.Group != "" && q.Group != "none" {
					if err := writeLine(out, g.Value); err != nil {
						return err
					}
				}
				if err := writeTable(out, g.Items); err != nil {
					return err
				}
			}
			return nil
		}},
		{Name: "show", Usage: "Show one item", ArgsUsage: "ID", Flags: []urfave.Flag{&urfave.StringFlag{Name: "section", Usage: "description, criteria, dod, plan, notes, summary, or comments"}}, Action: func(ctx context.Context, cmd *urfave.Command) error {
			if cmd.Args().Len() != 1 {
				return &Error{Reason: "usage", Message: "show requires exactly one ID"}
			}
			section := cmd.String("section")
			if section != "" && !validSection(section) {
				return &Error{Reason: "usage", Message: "unknown section " + section}
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			matches, err := engine.ReadDetail(ctx, cfg, cmd.Args().First())
			if e := duplicateFiles(err); e != nil {
				return e
			}
			if err != nil {
				return &Error{Reason: "unavailable", Message: err.Error()}
			}
			if len(matches) == 0 {
				return &Error{Reason: "not-found", Message: "item not found: " + cmd.Args().First()}
			}
			if len(matches) > 1 {
				names := []string{}
				for _, i := range matches {
					names = append(names, i.Ref.Source+":"+i.Ref.ID)
				}
				return &Error{Reason: "usage", Message: "ambiguous ID; matches: " + strings.Join(names, ", "), Details: map[string]any{"matches": names}}
			}
			i := matches[0]
			sections := itemSections(i)
			if section != "" {
				sections = map[string]string{section: sections[section]}
			}
			if cmd.Bool("json") {
				return success(out, "show", showData{Item: itemData(i), Metadata: itemMetadata(i), Sections: sections, Criteria: i.Criteria, DoD: i.DoD})
			}
			if section != "" {
				return writeLine(out, sections[section])
			}
			if err := writeLine(out, i.Ref.Source+":"+i.Ref.ID+"  "+i.Title+" ["+i.Status+"]"); err != nil {
				return err
			}
			metadata := itemMetadata(i)
			for _, name := range strings.Fields("type rank assignees labels milestone due parent children blockedBy blocks related duplicateOf refs files created updated url") {
				value := metadata[name]
				if t, ok := value.(time.Time); ok {
					value = t.Format(time.RFC3339)
				}
				if err := writeLine(out, fmt.Sprintf("%s: %v", name, value)); err != nil {
					return err
				}
			}
			for _, name := range strings.Fields("description criteria dod plan notes summary comments") {
				if sections[name] != "" {
					if err := writeLine(out, "\n## "+name+"\n"+sections[name]); err != nil {
						return err
					}
				}
			}
			return nil
		}},
		{Name: "next", Usage: "Rank ready items matching QUERY", ArgsUsage: "[QUERY]", Flags: []urfave.Flag{limitFlag(1), &urfave.BoolFlag{Name: "explain", Usage: "Include every included and excluded item with reasons"}}, Action: func(ctx context.Context, cmd *urfave.Command) error {
			if cmd.Int("n") < 0 {
				return &Error{Reason: "usage", Message: "-n must be nonnegative"}
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			s, err := load(ctx, cfg, false)
			if err != nil {
				return err
			}
			q, err := s.Parse(strings.Join(cmd.Args().Slice(), " "))
			if err != nil {
				return &Error{Reason: "usage", Message: err.Error()}
			}
			if len(q.Sort) > 0 || q.Group != "" {
				return &Error{Reason: "usage", Message: "next uses readiness ranking; sort and group are not supported"}
			}
			byRef := map[model.Ref]model.Item{}
			for _, i := range s.Query.Items {
				byRef[i.Ref] = i
			}
			data := nextData{Items: []readItem{}}
			for _, e := range s.Ranking.Items {
				i := byRef[e.Ref]
				match := q.Match(i, s.Query, nil)
				included := e.Readiness == engine.Ready && match && (cmd.Int("n") == 0 || len(data.Items) < cmd.Int("n"))
				if included {
					data.Items = append(data.Items, itemData(i))
				} else if e.Readiness == engine.Ready {
					code := "limit"
					if !match {
						code = "query"
					}
					e.Reasons = append(append([]engine.Reason{}, e.Reasons...), engine.Reason{Code: code})
				}
				if cmd.Bool("explain") {
					data.Explanations = append(data.Explanations, nextExplanation{e, included})
				}
			}
			if cmd.Bool("json") {
				return success(out, "next", data)
			}
			if err := writeTable(out, data.Items); err != nil {
				return err
			}
			for _, e := range data.Explanations {
				codes := []string{}
				for _, r := range e.Reasons {
					codes = append(codes, r.Code)
				}
				if err := writeLine(out, e.Ref.Source+":"+e.Ref.ID+"  "+e.Text()+" ["+strings.Join(codes, ", ")+"]"); err != nil {
					return err
				}
			}
			return nil
		}},
	}
}

func itemMetadata(i model.Item) map[string]any {
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

// duplicateFiles reports an ID stored by two files (active and archived) as
// ambiguous rather than unavailable; bl renumber accepts a path selector.
func duplicateFiles(err error) *Error {
	var refusal *source.Error
	if !errors.As(err, &refusal) || refusal.Reason != "duplicate-id" {
		return nil
	}
	return &Error{Reason: "usage", Message: "ambiguous ID " + refusal.Message + ": active and archived files share it; resolve with bl renumber"}
}

func validSection(s string) bool {
	for _, v := range strings.Fields("description criteria dod plan notes summary comments") {
		if s == v {
			return true
		}
	}
	return false
}
func itemSections(i model.Item) map[string]string {
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
func writeLine(out io.Writer, s string) error {
	_, err := fmt.Fprintln(out, textutil.Sanitize(s))
	return err
}
func outputWidth(out io.Writer) int {
	if f, ok := out.(*os.File); ok && term.IsTerminal(f.Fd()) {
		if w, _, err := term.GetSize(f.Fd()); err == nil && w > 0 {
			return w
		}
	}
	return 80
}
func writeTable(out io.Writer, items []readItem) error {
	width := outputWidth(out)
	for _, i := range items {
		line := fmt.Sprintf("%s:%s  [%s] %s  %s", i.Source, i.ID, i.Status, i.Priority, i.Title)
		if _, err := fmt.Fprintln(out, ansi.Truncate(textutil.Line(line), width, "…")); err != nil {
			return err
		}
	}
	return nil
}
