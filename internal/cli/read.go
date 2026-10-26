package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
	urfave "github.com/urfave/cli/v3"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/itemjson"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/textutil"
)

type readItem = itemjson.Summary

type readGroup struct {
	Value string     `json:"value"`
	Items []readItem `json:"items"`
	name  string     // provider display name of an opaque Value, for text only
}
type listData struct {
	Groups []readGroup `json:"groups"`
}
type nextExplanation = engine.NextExplanation
type nextData struct {
	Items        []readItem        `json:"items"`
	Explanations []nextExplanation `json:"explanations,omitempty"`
	*engine.NextOutcome
}

func readCommands(directory *string, out, errOut io.Writer) []*urfave.Command {
	load := func(ctx context.Context, cfg project.Config, archived bool, text string) (engine.ReadSnapshot, error) {
		snapshot, err := engine.LoadReads(ctx, cfg, archived, text)
		if err != nil {
			if auth := oauthCLIError(err); auth != nil {
				return snapshot, auth
			}
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
		return &urfave.IntFlag{Name: "n", Value: n, HideDefault: n == 0, Usage: "Print at most `N` items; 0 prints all"}
	}
	return []*urfave.Command{
		{Name: "ls", Usage: "List items", ArgsUsage: "[QUERY]", Description: lsHelp, Flags: []urfave.Flag{
			&urfave.StringFlag{Name: "view", Usage: "Apply the saved view `NAME`"},
			&urfave.StringFlag{Name: "sort", Usage: "Sort by comma-separated `KEYS`; prefix - to reverse"},
			&urfave.StringFlag{Name: "group", Usage: "Group by `FIELD`"},
			limitFlag(0),
		}, Action: func(ctx context.Context, cmd *urfave.Command) error {
			if cmd.Int("n") < 0 {
				return &Error{Reason: "usage", Message: "-n must be nonnegative"}
			}
			cfg, err := loadBrowseConfig(cmd, *directory, false)
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
			s, err := load(ctx, cfg, archived, text)
			if err != nil {
				return err
			}
			q, err := s.Parse(text)
			if err != nil {
				return &Error{Reason: "usage", Message: err.Error()}
			}
			items := make([]model.Item, 0, len(s.Query.Items))
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
			groups := []query.Group{{Items: items}}
			if q.Group != "" && q.Group != "none" {
				groups = s.Query.Group(items, q.Group)
			}
			for _, g := range groups {
				group := readGroup{Value: g.Value, Items: []readItem{}, name: groupName(q.Group, g)}
				for _, i := range g.Items {
					group.Items = append(group.Items, itemjson.NewSummary(i))
				}
				data.Groups = append(data.Groups, group)
			}
			if cmd.Bool("json") {
				return success(out, "ls", data)
			}
			if len(items) == 0 {
				return writeEmpty(out, "No items.")
			}
			all := make([][]readItem, len(data.Groups))
			for n, g := range data.Groups {
				all[n] = g.Items
			}
			table := newItemTable(out, cfg, all...)
			for n, g := range data.Groups {
				if q.Group != "" && q.Group != "none" {
					value := cmp.Or(g.name, g.Value, "none")
					header := table.p.paint("1", textutil.Line(value)) + fmt.Sprintf(" (%d)", len(g.Items))
					if n > 0 {
						header = "\n" + header
					}
					if _, err := fmt.Fprintln(out, header); err != nil {
						return err
					}
				}
				if err := table.writeAll(g.Items); err != nil {
					return err
				}
			}
			return nil
		}},
		{Name: "show", Usage: "Show an item", ArgsUsage: "ID", Flags: []urfave.Flag{&urfave.StringFlag{Name: "section", Aliases: []string{"s"}, Usage: "Print only section `NAME`: description, criteria, dod, plan, notes, summary, or comments"}}, Action: func(ctx context.Context, cmd *urfave.Command) error {
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
				if auth := oauthCLIError(err); auth != nil {
					return auth
				}
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
			sections := itemjson.Sections(i)
			if section != "" {
				sections = map[string]string{section: sections[section]}
			}
			resource := ""
			c, _ := engine.Coordination(ctx)
			if c != nil {
				var e error
				if resource, e = c.Resource(i); e != nil && cmd.Bool("json") {
					return writeError(e)
				}
			}
			detail := itemjson.Detail{Resource: resource, Item: itemjson.NewSummary(i), Metadata: itemjson.Metadata(i), Sections: sections, Criteria: i.Criteria, DoD: i.DoD}
			if section == "" {
				detail.Claims, detail.ClaimsError = engine.ClaimHistory(ctx, c, resource)
			}
			if cmd.Bool("json") {
				return success(out, "show", detail)
			}
			if section != "" {
				return writeLine(out, sections[section])
			}
			return writeShow(out, cfg, i, detail)
		}},
		{Name: "next", Usage: "Rank ready items", ArgsUsage: "[QUERY]", Flags: []urfave.Flag{limitFlag(1), &urfave.BoolFlag{Name: "explain", Usage: "Explain every included and excluded item"}, &urfave.BoolFlag{Name: "claim", Usage: "Claim the top ready item for --session, resuming its claim first"}, &urfave.BoolFlag{Name: "start", Usage: "Start the claimed item"}}, Action: func(ctx context.Context, cmd *urfave.Command) error {
			if cmd.Int("n") < 0 {
				return &Error{Reason: "usage", Message: "-n must be nonnegative"}
			}
			cfg, err := loadBrowseConfig(cmd, *directory, false)
			if err != nil {
				return err
			}
			if cfg.Workspace != nil && cmd.Bool("claim") {
				return &Error{Reason: "usage", Message: "workspace next is read-only; claim from the owning project"}
			}
			if cmd.Bool("start") && !cmd.Bool("claim") {
				return &Error{Reason: "usage", Message: "--start requires --claim"}
			}
			if cmd.Bool("claim") {
				result, err := engine.NextClaim(ctx, cfg, engine.NextClaimOptions{Query: strings.Join(cmd.Args().Slice(), " "), Start: cmd.Bool("start"), Explain: cmd.Bool("explain")})
				data := nextData{Items: []readItem{}, Explanations: result.Explanations, NextOutcome: &result.NextOutcome}
				for _, item := range result.Items {
					data.Items = append(data.Items, itemjson.NewSummary(item))
				}
				if err != nil {
					return &Error{Reason: engine.ErrorReason(err), Message: engine.ErrorMessage(err), Data: data}
				}
				if cmd.Bool("json") {
					return success(out, "next", data)
				}
				return writeNext(out, errOut, cfg, data)
			}
			s, err := load(ctx, cfg, false, strings.Join(cmd.Args().Slice(), " "))
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
			byRef := make(map[model.Ref]int, len(s.Query.Items))
			for n, i := range s.Query.Items {
				byRef[i.Ref] = n
			}
			data := nextData{Items: []readItem{}}
			for _, e := range s.Ranking.Items {
				i := s.Query.Items[byRef[e.Ref]]
				match := q.Match(i, s.Query, nil)
				included := e.Readiness == engine.Ready && match && (cmd.Int("n") == 0 || len(data.Items) < cmd.Int("n"))
				if included {
					data.Items = append(data.Items, itemjson.NewSummary(i))
				} else if e.Readiness == engine.Ready {
					code := "limit"
					if !match {
						code = "query"
					}
					e.Reasons = append(append([]engine.Reason{}, e.Reasons...), engine.Reason{Code: code})
				}
				if cmd.Bool("explain") {
					data.Explanations = append(data.Explanations, nextExplanation{Explanation: e, Included: included})
				}
			}
			if cmd.Bool("json") {
				return success(out, "next", data)
			}
			return writeNext(out, errOut, cfg, data)
		}},
	}
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

// groupName is the provider display name of an opaque status, assignee or sprint group value.
func groupName(field string, g query.Group) string {
	if field != "status" && field != "assignee" && field != "sprint" {
		return ""
	}
	for _, i := range g.Items {
		if name := i.Names[g.Value]; name != "" {
			return name
		}
	}
	return ""
}
