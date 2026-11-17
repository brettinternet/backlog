package cli

import (
	"cmp"
	"context"
	"embed"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	urfave "github.com/urfave/cli/v3"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/instructions"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/brettinternet/backlog/internal/tui"
)

// completionMarker ends a completion request from a bl completion script:
// bl WORDS... CURRENT --generate-shell-completion, where CURRENT is the word
// being completed, possibly empty.
const completionMarker = "--generate-shell-completion"

//go:embed completion
var completionScripts embed.FS

// setFields are the fields bl set accepts, as setHelp lists them.
var setFields = strings.Fields("title status type priority assignees labels milestone parent blocked_by related duplicate_of rank due refs files")

func completionCommand(out io.Writer) *urfave.Command {
	cmd := &urfave.Command{Name: "completion", Usage: "Print a shell completion script", Category: categoryProject, Description: completionHelp,
		Action: groupHelp("use bl completion bash|zsh|fish|pwsh")}
	for _, shell := range []string{"bash", "zsh", "fish", "pwsh"} {
		cmd.Commands = append(cmd.Commands, &urfave.Command{Name: shell, Usage: "Print the " + shell + " completion script", Action: func(context.Context, *urfave.Command) error {
			script, err := completionScripts.ReadFile("completion/" + shell)
			if err != nil {
				return err
			}
			_, err = out.Write(script)
			return err
		}})
	}
	return cmd
}

type candidate struct{ value, description string }

// completer reads config and cached items only, never hooks, actions,
// adapters, credential helpers, Worklease, Git, or a provider (design.md D16).
type completer struct {
	ctx       context.Context
	directory string
	cfg       *project.Config
	items     []model.Item
	loaded    bool
}

// complete answers one completion request. words follow "bl" and end with the
// word being completed. It prints one "VALUE\tDESCRIPTION" line per candidate.
func complete(ctx context.Context, root *urfave.Command, words []string, out io.Writer) error {
	c := &completer{ctx: ctx, directory: "."}
	current := words[len(words)-1]
	chain, path := []*urfave.Command{root}, ""
	var args []string
	pending, dash := "", false
	for _, word := range words[:len(words)-1] {
		switch {
		case pending != "":
			c.flag(pending, word)
			pending = ""
		case dash:
			args = append(args, word)
		case word == "--":
			dash = true
		case word == "-" || !strings.HasPrefix(word, "-"):
			if sub := chain[len(chain)-1].Command(word); sub != nil && len(args) == 0 {
				chain, path = append(chain, sub), strings.TrimSpace(path+" "+sub.Name)
				continue
			}
			args = append(args, word)
		default:
			name, value, valued := strings.Cut(strings.TrimLeft(word, "-"), "=")
			if f := lookupFlag(chain, name); f != nil && takesValue(f) {
				if valued {
					c.flag(name, value)
				} else {
					pending = name
				}
			}
		}
	}
	cmd := chain[len(chain)-1]
	var candidates []candidate
	switch {
	case dash:
	case pending != "":
		candidates = c.flagValues(path, pending, current)
	case strings.HasPrefix(current, "--") && strings.Contains(current, "="):
		name, value, _ := strings.Cut(current[2:], "=")
		for _, v := range c.flagValues(path, name, value) {
			candidates = append(candidates, candidate{"--" + name + "=" + v.value, v.description})
		}
	case strings.HasPrefix(current, "-") && !(queryCommand(path) && strings.Contains(current, ":")):
		candidates = flagCandidates(chain)
	case len(args) == 0 && len(cmd.VisibleCommands()) > 0:
		candidates = commandCandidates(cmd)
	default:
		candidates = c.arguments(root, path, args, current)
	}
	for _, v := range candidates {
		if !strings.HasPrefix(strings.ToLower(v.value), strings.ToLower(current)) {
			continue
		}
		line := v.value
		if v.description != "" {
			line += "\t" + textutil.Line(v.description)
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

func queryCommand(path string) bool {
	return path == "ls" || path == "next" || path == "tui" || path == "events"
}

// inherited lists the flags of the last command in chain, then the flags its
// ancestors don't keep local.
func inherited(chain []*urfave.Command) []urfave.Flag {
	var out []urfave.Flag
	for n := len(chain) - 1; n >= 0; n-- {
		for _, f := range chain[n].Flags {
			if local, ok := f.(urfave.LocalFlag); !ok || !local.IsLocal() || n == len(chain)-1 {
				out = append(out, f)
			}
		}
	}
	return out
}

func lookupFlag(chain []*urfave.Command, name string) urfave.Flag {
	for _, f := range inherited(chain) {
		if slices.Contains(f.Names(), name) {
			return f
		}
	}
	return nil
}

func takesValue(f urfave.Flag) bool {
	doc, ok := f.(urfave.DocGenerationFlag)
	return ok && doc.TakesValue()
}

func flagCandidates(chain []*urfave.Command) []candidate {
	var out []candidate
	for _, f := range inherited(chain) {
		if visible, ok := f.(urfave.VisibleFlag); ok && !visible.IsVisible() {
			continue
		}
		usage := ""
		if doc, ok := f.(urfave.DocGenerationFlag); ok {
			usage = strings.ReplaceAll(doc.GetUsage(), "`", "")
		}
		for _, name := range f.Names() {
			dashes := "--"
			if len(name) == 1 {
				dashes = "-"
			}
			out = append(out, candidate{dashes + name, usage})
		}
	}
	return out
}

func commandCandidates(cmd *urfave.Command) []candidate {
	var out []candidate
	for _, sub := range cmd.VisibleCommands() {
		out = append(out, candidate{sub.Name, sub.Usage})
	}
	return out
}

func (c *completer) flag(name, value string) {
	if name == "C" {
		c.directory = value
	}
}

func (c *completer) config() project.Config {
	if c.cfg == nil {
		c.cfg = &project.Config{}
		directory, err := filepath.Abs(c.directory)
		if err != nil {
			return *c.cfg
		}
		if paths, err := project.UserPaths(); err == nil {
			if cfg, err := project.Resolve(paths, directory, nil); err == nil {
				c.cfg = &cfg
			}
		}
	}
	return *c.cfg
}

func (c *completer) snapshot() query.Snapshot {
	cfg := c.config()
	if !c.loaded {
		c.items, c.loaded = engine.CachedItems(c.ctx, cfg), true
	}
	return query.Snapshot{Items: c.items, Settings: cfg.Settings, Me: cfg.Identities()}
}

func (c *completer) arguments(root *urfave.Command, path string, args []string, current string) []candidate {
	switch path {
	case "show", "history", "changes", "claim", "release", "start", "done", "edit", "renumber", "tour", "note", "comment", "handoff", "resume":
		if len(args) == 0 {
			return c.ids(current)
		}
	case "archive", "unarchive", "move":
		return c.ids(current)
	case "ac":
		if len(args) == 0 {
			return c.ids(current)
		}
		if len(args) == 1 {
			return words("check", "uncheck", "add", "set", "rm")
		}
	case "action":
		if len(args) == 0 {
			var out []candidate
			for _, a := range c.config().Actions {
				out = append(out, candidate{a.Name, strings.Join(a.Run, " ")})
			}
			return out
		}
		if len(args) == 1 {
			return c.ids(current)
		}
	case "set":
		return c.set(args, current)
	case "ls", "next", "tui", "events":
		return c.query(current, query.Options{Hook: path == "events"})
	case "instructions":
		if len(args) == 0 {
			return words(instructions.Names()...)
		}
	case "help":
		cmd := root
		for _, name := range args {
			if cmd = cmd.Command(name); cmd == nil {
				return nil
			}
		}
		return commandCandidates(cmd)
	case "auth login", "auth logout", "auth status":
		accounts := c.config().Accounts
		if len(args) == 0 {
			providers := make([]string, 0, len(accounts))
			for provider := range accounts {
				providers = append(providers, provider)
			}
			slices.Sort(providers)
			return words(providers...)
		}
		if len(args) == 1 {
			var out []candidate
			for _, a := range accounts[args[0]] {
				out = append(out, candidate{a.Name, a.User})
			}
			return out
		}
	}
	return nil
}

func (c *completer) flagValues(path, name, current string) []candidate {
	cfg := c.config()
	var out []candidate
	switch name {
	case "view", "V":
		if path == "tui" {
			out = words(tui.BuiltinViews()...)
			for _, v := range cfg.SavedViews {
				out = append(out, candidate{v.Name, v.Query})
			}
		} else {
			for _, v := range cfg.Views {
				out = append(out, candidate{v.Name, v.Query})
			}
		}
	case "workspace":
		for _, w := range cfg.Workspaces {
			out = append(out, candidate{w.Name, ""})
		}
	case "source":
		for _, s := range cfg.Sources {
			out = append(out, candidate{s.ID, s.Type})
		}
	case "status", "s":
		if path == "new" {
			for _, s := range cfg.Statuses {
				out = append(out, candidate{s.Key, s.Name})
			}
		}
	case "sort", "group":
		// Sort keys are comma-separated, each optionally reversed with -.
		before, chosen := "", []string(nil)
		if name == "sort" {
			before, current, chosen = lastEntry(current)
			for i := range chosen {
				chosen[i] = strings.TrimPrefix(chosen[i], "-")
			}
			if strings.HasPrefix(current, "-") {
				before, current = before+"-", current[1:]
			}
		}
		for _, v := range c.snapshot().Complete(name, current, query.Options{}) {
			if !picked(chosen, v.Value) {
				out = append(out, candidate{before + v.Value, v.Name})
			}
		}
	case "before", "after":
		out = c.ids(current)
	}
	return out
}

func words(values ...string) []candidate {
	out := make([]candidate, len(values))
	for i, v := range values {
		out[i] = candidate{value: v}
	}
	return out
}

// ids lists item IDs, open items first, each with its title. A numeric word
// completes a prefixed source's unpadded numeric shorthand; an ID in several sources
// is qualified as source:ID.
func (c *completer) ids(current string) []candidate {
	items := slices.Clone(c.snapshot().Items)
	slices.SortStableFunc(items, func(a, b model.Item) int {
		closed := func(i model.Item) bool { return i.Category == model.Done || i.Category == model.Canceled }
		if closed(a) != closed(b) {
			if closed(a) {
				return 1
			}
			return -1
		}
		return cmp.Compare(a.Ref.ID, b.Ref.ID)
	})
	prefixes := map[string]string{}
	for _, s := range c.config().Sources {
		if s.Prefix != "" {
			prefixes[s.ID] = s.Prefix + "-"
		}
	}
	numeric := current != "" && strings.Trim(current, "0123456789") == ""
	sources := map[string]int{}
	for _, i := range items {
		sources[strings.ToLower(i.Ref.ID)]++
	}
	out := make([]candidate, 0, len(items))
	for _, i := range items {
		id := i.Ref.ID
		if sources[strings.ToLower(id)] > 1 {
			id = i.Ref.Source + ":" + id
		} else if p := prefixes[i.Ref.Source]; numeric && p != "" && strings.HasPrefix(id, p) {
			id = strings.TrimPrefix(id, p)
			// BL-012 resolves from 12; keep the padding only if the word has it.
			if !strings.HasPrefix(current, "0") {
				if id = strings.TrimLeft(id, "0"); id == "" {
					id = "0"
				}
			}
		}
		out = append(out, candidate{id, i.Title})
	}
	return out
}

// set completes IDs until the first assignment, then FIELD=, then values:
// statuses the first item's source accepts, the query's values for other
// fields, and IDs for relations. A list value completes after its last comma.
func (c *completer) set(args []string, current string) []candidate {
	field, value, assigning := strings.Cut(current, "=")
	if !assigning {
		var out []candidate
		for _, f := range setFields {
			out = append(out, candidate{f + "=", ""})
		}
		if !slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, "=") }) {
			out = append(out, c.ids(current)...)
		}
		return out
	}
	before := field + "="
	name := strings.TrimRight(field, "+-")
	var chosen []string
	if engine.ListField(name) {
		var head string
		head, value, chosen = lastEntry(value)
		before += head
	}
	var values []candidate
	switch name {
	case "status":
		values = c.statuses(args)
	case "parent", "blocked_by", "related", "duplicate_of":
		values = c.ids(value)
	case "type", "priority", "milestone", "labels", "assignees":
		queryField := strings.TrimSuffix(name, "s")
		if name == "milestone" || name == "type" {
			queryField = name
		}
		for _, v := range c.snapshot().Complete(queryField, value, query.Options{}) {
			values = append(values, candidate{v.Value, v.Name})
		}
	}
	out := make([]candidate, 0, len(values))
	for _, v := range values {
		if picked(chosen, v.value) {
			continue
		}
		out = append(out, candidate{before + v.value, v.description})
	}
	return out
}

// statuses are the configured statuses, narrowed by the source of the first
// ID argument when that source lists its own.
func (c *completer) statuses(args []string) []candidate {
	cfg := c.config()
	var allowed []string
	if len(args) > 0 {
		if item, ok := c.item(args[0]); ok {
			for _, s := range cfg.Sources {
				if s.ID == item.Ref.Source {
					allowed = s.Statuses
				}
			}
		}
	}
	var out []candidate
	for _, s := range cfg.Statuses {
		if len(allowed) == 0 || slices.Contains(allowed, s.Key) {
			out = append(out, candidate{s.Key, s.Name})
		}
	}
	return out
}

// item finds the one cached item an ID argument names: ID, source:ID, or a
// prefixed source's numeric shorthand.
func (c *completer) item(arg string) (model.Item, bool) {
	sourceID, id, qualified := strings.Cut(arg, ":")
	if !qualified {
		id = arg
	}
	prefixes := map[string]string{}
	for _, s := range c.config().Sources {
		prefixes[s.ID] = s.Prefix
	}
	var found []model.Item
	for _, i := range c.snapshot().Items {
		if qualified && i.Ref.Source != sourceID {
			continue
		}
		if strings.EqualFold(i.Ref.ID, id) || prefixes[i.Ref.Source] != "" && strings.EqualFold(i.Ref.ID, prefixes[i.Ref.Source]+"-"+id) {
			found = append(found, i)
		}
	}
	if len(found) != 1 {
		return model.Item{}, false
	}
	return found[0], true
}

// query completes one query term: field names, then values after field:,
// keeping a leading - and earlier comma-separated values.
func (c *completer) query(current string, options query.Options) []candidate {
	before := ""
	if strings.HasPrefix(current, "-") {
		before, current = "-", current[1:]
	}
	field, value, valued := strings.Cut(current, ":")
	if !valued {
		var out []candidate
		for _, v := range c.snapshot().Complete("", current, options) {
			out = append(out, candidate{before + v.Value, v.Name})
		}
		return out
	}
	head, value, chosen := lastEntry(value)
	before += field + ":" + head
	var out []candidate
	for _, v := range c.snapshot().Complete(field, value, options) {
		if !picked(chosen, v.Value) && !picked(chosen, query.Quote(v.Value)) {
			out = append(out, candidate{before + query.Quote(v.Value), v.Name})
		}
	}
	return out
}

// lastEntry splits a comma-separated word into its earlier entries, kept
// with the trailing comma, the entry being typed, and the entries chosen.
func lastEntry(word string) (head, last string, chosen []string) {
	i := strings.LastIndexByte(word, ',')
	if i < 0 {
		return "", word, nil
	}
	return word[:i+1], word[i+1:], strings.Split(word[:i], ",")
}

func picked(chosen []string, value string) bool {
	return slices.ContainsFunc(chosen, func(c string) bool { return strings.EqualFold(c, value) })
}
