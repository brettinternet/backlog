package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
	urfave "github.com/urfave/cli/v3"
)

type writeOutcome struct {
	ID    string    `json:"id"`
	Item  *readItem `json:"item,omitempty"`
	Error *Error    `json:"error,omitempty"`
}
type writeData struct {
	Outcomes []writeOutcome `json:"outcomes"`
}

func writeError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	var refusal *source.Error
	if errors.As(err, &refusal) {
		reason := refusal.Reason
		switch reason {
		case "conflict", "not-found", "unavailable", "rejected":
		default:
			reason = "rejected"
		}
		return &Error{Reason: reason, Message: refusal.Message}
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return &Error{Reason: "unavailable", Message: err.Error()}
	}
	return &Error{Reason: "rejected", Message: err.Error()}
}
func writeUsage(message string) error { return &Error{Reason: "usage", Message: message} }

func writeCommands(directory *string, in io.Reader, out, errOut io.Writer) []*urfave.Command {
	flag := func() []urfave.Flag {
		return []urfave.Flag{&urfave.StringFlag{Name: "if-version", Usage: "Refuse a stale item version"}}
	}
	body := func(text string) (string, error) {
		if text != "-" {
			return text, nil
		}
		data, err := io.ReadAll(in)
		return string(data), err
	}
	batch := func(ctx context.Context, cmd *urfave.Command, cfg project.Config, ids []string, changes []model.Mutation) error {
		if len(ids) == 0 {
			return writeUsage(cmd.Name + " requires an ID")
		}
		if cmd.IsSet("if-version") && (len(ids) != 1 || cmd.String("if-version") == "") {
			return writeUsage("--if-version requires exactly one ID and a nonempty version")
		}
		data := writeData{Outcomes: []writeOutcome{}}
		var first *Error
		for _, id := range ids {
			item, err := writeItem(ctx, cfg, id)
			if err == nil {
				version := item.Version
				if cmd.IsSet("if-version") {
					version = cmd.String("if-version")
				}
				m := model.Mutation{Op: model.Batch, Changes: changes, Ref: item.Ref, ExpectedVersion: version}
				if len(changes) == 1 {
					m = changes[0]
					m.Ref = item.Ref
					m.ExpectedVersion = version
				}
				item, err = engine.ApplyWrite(ctx, cfg, m)
			}
			outcome := writeOutcome{ID: id}
			if err != nil {
				outcome.Error = writeError(err)
				if first == nil {
					first = outcome.Error
				}
			} else {
				summary := itemData(item)
				outcome.Item = &summary
			}
			data.Outcomes = append(data.Outcomes, outcome)
		}
		if !cmd.Bool("json") {
			for _, r := range data.Outcomes {
				if r.Error != nil {
					if err := writeLine(out, r.ID+": "+r.Error.Reason+": "+r.Error.Message); err != nil {
						return err
					}
				} else if err := writeLine(out, r.Item.Source+":"+r.Item.ID+" "+r.Item.Version); err != nil {
					return err
				}
			}
		}
		if first != nil {
			failure := *first
			failure.Data = data
			return &failure
		}
		if cmd.Bool("json") {
			return success(out, cmd.Name, data)
		}
		return nil
	}
	commands := []*urfave.Command{}
	commands = append(commands, &urfave.Command{Name: "new", Usage: "Create an item using quick-add tokens", Flags: append(flag(), &urfave.StringFlag{Name: "body"}, &urfave.StringSliceFlag{Name: "ac"}, &urfave.StringFlag{Name: "source"}), Action: func(ctx context.Context, cmd *urfave.Command) error {
		if !cmd.Args().Present() {
			return writeUsage("new requires a title")
		}
		cfg, err := loadConfig(cmd, *directory)
		if err != nil {
			return err
		}
		item, err := query.QuickAdd(strings.Join(cmd.Args().Slice(), " "), cfg.Me.Default)
		if err != nil {
			return writeUsage(err.Error())
		}
		if item.Type == "" {
			item.Type = cfg.Defaults.Type
		}
		for _, c := range cfg.Defaults.DOD {
			item.DoD = append(item.DoD, model.Criterion{Text: c})
		}
		for _, c := range cmd.StringSlice("ac") {
			if strings.TrimSpace(c) == "" || strings.ContainsAny(c, "\r\n") {
				return writeUsage("--ac requires nonempty single-line text")
			}
			item.Criteria = append(item.Criteria, model.Criterion{Text: c})
		}
		item.Description, err = body(cmd.String("body"))
		if err != nil {
			return err
		}
		for _, s := range cfg.Statuses {
			if s.Category == "todo" {
				item.Status = s.Key
				break
			}
		}
		if item.Status == "" && len(cfg.Statuses) > 0 {
			item.Status = cfg.Statuses[0].Key
		}
		sourceID := cmd.String("source")
		if sourceID == "" {
			if len(cfg.Sources) != 1 {
				return writeUsage("new requires --source with multiple or no sources")
			}
			sourceID = cfg.Sources[0].ID
		}
		item, err = engine.ApplyWrite(ctx, cfg, model.Mutation{Op: model.Create, Ref: model.Ref{Source: sourceID}, Item: &item, ExpectedVersion: cmd.String("if-version")})
		if err != nil {
			return writeError(err)
		}
		if cmd.Bool("json") {
			return success(out, "new", itemData(item))
		}
		return writeLine(out, item.Ref.Source+":"+item.Ref.ID+" "+item.Version)
	}})
	commands = append(commands, &urfave.Command{Name: "set", Usage: "Set fields atomically per item", ArgsUsage: "ID... FIELD=V FIELD+=V FIELD-=V", Flags: flag(), Action: func(ctx context.Context, cmd *urfave.Command) error {
		ids := []string{}
		changes := []model.Mutation{}
		for _, arg := range cmd.Args().Slice() {
			if !strings.Contains(arg, "=") {
				if len(changes) > 0 {
					return writeUsage("IDs must precede field assignments")
				}
				ids = append(ids, arg)
				continue
			}
			field, value, _ := strings.Cut(arg, "=")
			op := model.Set
			if strings.HasSuffix(field, "+") {
				op = model.Add
				field = strings.TrimSuffix(field, "+")
			} else if strings.HasSuffix(field, "-") {
				op = model.Remove
				field = strings.TrimSuffix(field, "-")
			}
			switch field {
			case "label":
				field = "labels"
			case "assignee":
				field = "assignees"
			}
			var v any = value
			if op == model.Set && engine.ListField(field) {
				values := []string{}
				if value != "" {
					values = strings.Split(value, ",")
				}
				v = values
			}
			changes = append(changes, model.Mutation{Op: op, Field: field, Value: v})
		}
		if len(changes) == 0 {
			return writeUsage("set requires field assignments")
		}
		cfg, err := loadConfig(cmd, *directory)
		if err != nil {
			return err
		}
		return batch(ctx, cmd, cfg, ids, changes)
	}})
	for _, name := range []string{"start", "done", "archive", "unarchive"} {
		commands = append(commands, &urfave.Command{Name: name, Usage: "Apply " + name + " to items", Flags: flag(), Action: func(ctx context.Context, cmd *urfave.Command) error {
			ids := cmd.Args().Slice()
			if (cmd.Name == "start" || cmd.Name == "done") && len(ids) != 1 {
				return writeUsage(cmd.Name + " requires exactly one ID")
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			m := model.Mutation{Op: model.Operation(cmd.Name)}
			if cmd.Name == "start" || cmd.Name == "done" {
				status := cfg.Workflow.Start
				if cmd.Name == "done" {
					status = cfg.Workflow.Done
				}
				if status == "" {
					return &Error{Reason: "rejected", Message: "workflow." + cmd.Name + " is not configured"}
				}
				m = model.Mutation{Op: model.Set, Field: "status", Value: status}
			}
			return batch(ctx, cmd, cfg, ids, []model.Mutation{m})
		}})
	}
	for _, name := range []string{"note", "comment"} {
		flags := flag()
		if name == "comment" {
			flags = append(flags, &urfave.StringFlag{Name: "author", Usage: "Comment author (defaults to me.default)"})
		}
		commands = append(commands, &urfave.Command{Name: name, Usage: "Append text or stdin (-)", Flags: flags, Action: func(ctx context.Context, cmd *urfave.Command) error {
			args := cmd.Args().Slice()
			if len(args) < 2 {
				return writeUsage(cmd.Name + " requires ID and TEXT or -")
			}
			text, err := body(strings.Join(args[1:], " "))
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) == "" {
				return writeUsage("text must not be empty")
			}
			cfg, err := loadConfig(cmd, *directory)
			if err != nil {
				return err
			}
			m := model.Mutation{Op: model.AppendNote, Text: text}
			if cmd.Name == "comment" {
				author := cmd.String("author")
				if author == "" {
					author = cfg.Me.Default
				}
				if author == "" {
					return &Error{Reason: "rejected", Message: "comment requires --author or me.default"}
				}
				m = model.Mutation{Op: model.AppendComment, Comment: model.Comment{Author: author, Body: text, Time: time.Now()}}
			}
			return batch(ctx, cmd, cfg, args[:1], []model.Mutation{m})
		}})
	}
	commands = append(commands, &urfave.Command{Name: "ac", Usage: "Check, uncheck, add, or remove acceptance criteria", Flags: append(flag(), &urfave.StringFlag{Name: "evidence"}), Action: func(ctx context.Context, cmd *urfave.Command) error {
		args := cmd.Args().Slice()
		if len(args) < 3 {
			return writeUsage("ac requires ID, check|uncheck|add|rm, and text or index")
		}
		ops := map[string]model.Operation{"check": model.CheckCriterion, "uncheck": model.UncheckCriterion, "add": model.AddCriterion, "rm": model.RemoveCriterion}
		op, ok := ops[args[1]]
		if !ok {
			return writeUsage("ac operation must be check, uncheck, add, or rm")
		}
		if cmd.IsSet("evidence") && (op != model.CheckCriterion || len(args) != 3 || strings.TrimSpace(cmd.String("evidence")) == "" || strings.ContainsAny(cmd.String("evidence"), "\r\n")) {
			return writeUsage("--evidence requires check with exactly one index and nonempty single-line text")
		}
		changes := []model.Mutation{}
		if op == model.AddCriterion {
			text := strings.Join(args[2:], " ")
			if strings.TrimSpace(text) == "" || strings.ContainsAny(text, "\r\n") {
				return writeUsage("criterion must be nonempty and single-line")
			}
			changes = append(changes, model.Mutation{Op: op, Section: "Acceptance criteria", Text: text})
		} else {
			if op == model.RemoveCriterion && len(args) != 3 {
				return writeUsage("ac rm takes one index")
			}
			for _, raw := range args[2:] {
				n, err := strconv.Atoi(raw)
				if err != nil || n < 1 {
					return writeUsage("criterion indices must be positive integers")
				}
				m := model.Mutation{Op: op, Section: "Acceptance criteria", Index: n}
				if cmd.IsSet("evidence") {
					v := cmd.String("evidence")
					m.Evidence = &v
				}
				changes = append(changes, m)
			}
		}
		cfg, err := loadConfig(cmd, *directory)
		if err != nil {
			return err
		}
		return batch(ctx, cmd, cfg, args[:1], changes)
	}})
	commands = append(commands, &urfave.Command{Name: "edit", Usage: "Edit the whole file or one section in $EDITOR", Flags: append(flag(), &urfave.StringFlag{Name: "section"}), Action: func(ctx context.Context, cmd *urfave.Command) error {
		if cmd.Args().Len() != 1 {
			return writeUsage("edit requires exactly one ID")
		}
		if cmd.IsSet("if-version") && cmd.String("if-version") == "" {
			return writeUsage("--if-version must not be empty")
		}
		section := cmd.String("section")
		if section != "" && (!validSection(section) || section == "comments") {
			return writeUsage("editable sections: description criteria dod plan notes summary; comments are append-only")
		}
		cfg, err := loadConfig(cmd, *directory)
		if err != nil {
			return err
		}
		item, err := writeItem(ctx, cfg, cmd.Args().First())
		if err != nil {
			return err
		}
		version := item.Version
		if cmd.IsSet("if-version") && cmd.String("if-version") != version {
			return &Error{Reason: "conflict", Message: "expected version does not match current content"}
		}
		original, err := readNativeBytes(cfg, item)
		if err != nil {
			return writeError(err)
		}
		// Bind the editor buffer to the bytes actually read, not an earlier lookup.
		doc, err := format.Parse(original)
		if err != nil {
			return writeError(err)
		}
		if doc.Version() != version {
			return &Error{Reason: "conflict", Message: "item changed before opening editor"}
		}
		content := original
		if section != "" {
			names := map[string]string{"description": "Description", "criteria": "Acceptance criteria", "dod": "Definition of done", "plan": "Plan", "notes": "Notes", "summary": "Summary"}
			content = []byte(doc.SectionText(names[section]) + "\n")
		}
		file, err := os.CreateTemp("", "bl-edit-*.md")
		if err != nil {
			return err
		}
		path := file.Name()
		_, err = file.Write(content)
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		retain := func(err error) error {
			e := writeError(err)
			e.Message += "; edited buffer retained at " + path
			e.Details = map[string]any{"editedPath": path}
			return e
		}
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		process := exec.CommandContext(ctx, "/bin/sh", "-c", editor+` "$1"`, "bl-edit", path)
		process.Dir = *directory
		process.Stdin = in
		process.Stdout = errOut
		process.Stderr = errOut
		if err = process.Run(); err != nil {
			return retain(err)
		}
		edited, err := os.ReadFile(path)
		if err != nil {
			return retain(err)
		}
		if string(edited) == string(content) {
			_ = os.Remove(path)
			if cmd.Bool("json") {
				return success(out, "edit", itemData(item))
			}
			return writeLine(out, item.Ref.Source+":"+item.Ref.ID+" unchanged")
		}
		m := model.Mutation{Op: model.Restore, Ref: item.Ref, ExpectedVersion: version, Original: edited}
		if section != "" {
			names := map[string]string{"description": "Description", "criteria": "Acceptance criteria", "dod": "Definition of done", "plan": "Plan", "notes": "Notes", "summary": "Summary"}
			m.Op = model.ReplaceSection
			m.Section = names[section]
			m.Text = string(edited)
		}
		item, err = engine.ApplyWrite(ctx, cfg, m)
		if err != nil {
			return retain(err)
		}
		_ = os.Remove(path)
		if cmd.Bool("json") {
			return success(out, "edit", itemData(item))
		}
		return writeLine(out, item.Ref.Source+":"+item.Ref.ID+" "+item.Version)
	}})
	return commands
}

func writeItem(ctx context.Context, cfg project.Config, id string) (model.Item, error) {
	matches, err := engine.ReadDetail(ctx, cfg, id)
	if err != nil {
		return model.Item{}, writeError(err)
	}
	if len(matches) == 0 {
		return model.Item{}, &Error{Reason: "not-found", Message: "item not found: " + id}
	}
	if len(matches) > 1 {
		names := []string{}
		for _, i := range matches {
			names = append(names, i.Ref.Source+":"+i.Ref.ID)
		}
		return model.Item{}, &Error{Reason: "usage", Message: "ambiguous ID; matches: " + strings.Join(names, ", "), Details: map[string]any{"matches": names}}
	}
	return matches[0], nil
}

// Whole-file editing is native-only. Never derive a path from user-supplied ID.
func readNativeBytes(cfg project.Config, item model.Item) ([]byte, error) {
	for _, c := range cfg.Sources {
		if c.ID == item.Ref.Source && c.Type == "native" {
			root := c.Path
			if !filepath.IsAbs(root) {
				root = filepath.Join(cfg.Discovery.ControlRoot, root)
			}
			for _, dir := range []string{root, filepath.Join(root, "archive")} {
				path := filepath.Join(dir, item.Ref.ID+".md")
				info, err := os.Lstat(path)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return nil, err
				}
				if !info.Mode().IsRegular() {
					return nil, fmt.Errorf("item is not a regular file")
				}
				return os.ReadFile(path)
			}
			return nil, source.Refuse("not-found", item.Ref.ID)
		}
	}
	return nil, fmt.Errorf("whole-file editing requires a native source")
}
