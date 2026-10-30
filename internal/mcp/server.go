// Package mcp exposes backlog operations over the Model Context Protocol.
package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/history"
	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/instructions"
	"github.com/brettinternet/backlog/internal/itemjson"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
	"github.com/brettinternet/backlog/internal/query"
	"github.com/brettinternet/backlog/internal/source"
)

const InstructionsURI = "bl://instructions"

type Server struct {
	server      *mcp.Server
	cfg         project.Config
	coordinator model.Coordinator
	// writes serializes read-then-write tools so concurrent calls without a
	// version don't conflict with each other.
	writes sync.Mutex
}

func New(cfg project.Config) *Server {
	s := &Server{cfg: cfg, coordinator: engine.NewCoordinator(cfg), server: mcp.NewServer(&mcp.Implementation{Name: "bl", Version: "dev"}, nil)}
	mcp.AddTool(s.server, &mcp.Tool{Name: "claim", Description: "Claim an item with explicit caller ownership."}, s.claim)
	mcp.AddTool(s.server, &mcp.Tool{Name: "release", Description: "Release the caller matching claim."}, s.release)
	mcp.AddTool(s.server, &mcp.Tool{Name: "heartbeat", Description: "Renew a caller-managed claim."}, s.heartbeat)
	mcp.AddTool(s.server, &mcp.Tool{Name: "next", Description: "Return ready backlog items in readiness order."}, s.next)
	mcp.AddTool(s.server, &mcp.Tool{Name: "query", Description: "Search backlog items using the bl query language."}, s.query)
	mcp.AddTool(s.server, &mcp.Tool{Name: "show", Description: "Read one backlog item."}, s.show)
	mcp.AddTool(s.server, &mcp.Tool{Name: "history", Description: "Read one item's history: events, claims, sessions, comments, linked changes, and Git commits, oldest first, with each provider's status."}, s.history)
	mcp.AddTool(s.server, &mcp.Tool{Name: "changes", Description: "Read one item's linked changes: worktrees, branches, pull requests, and landed commits, furthest first, with stage, state, checks, review, URL, and each provider's status."}, s.changes)
	mcp.AddTool(s.server, &mcp.Tool{Name: "tour", Description: "Store a CodeTour file for the item's linked change whose head equals the tour's ref, for the TUI Diff tab, or, without file, read the tours stored for its linked changes. The format is in bl instructions tours."}, s.tour)
	mcp.AddTool(s.server, &mcp.Tool{Name: "create", Description: "Create a backlog item."}, s.create)
	mcp.AddTool(s.server, &mcp.Tool{Name: "update", Description: "Update fields on one backlog item."}, s.update)
	mcp.AddTool(s.server, &mcp.Tool{Name: "append", Description: "Append a note or comment to one backlog item."}, s.append)
	mcp.AddTool(s.server, &mcp.Tool{Name: "criteria", Description: "Change acceptance criteria or definition-of-done items on one backlog item."}, s.criteria)
	mcp.AddTool(s.server, &mcp.Tool{Name: "events", Description: "Read project events after a cursor, filtered by an optional event query. Poll with since set to the returned cursor."}, s.events)
	mcp.AddTool(s.server, &mcp.Tool{Name: "action", Description: "Run a configured action on one item. Set confirm to acknowledge confirmation."}, s.action)
	s.server.AddResource(&mcp.Resource{URI: InstructionsURI, Name: "bl instructions", Description: "Version-matched bl agent instructions", MIMEType: "text/markdown"}, s.instructions)
	return s
}

func (s *Server) Run(ctx context.Context, transport mcp.Transport) error {
	return s.server.Run(ctx, transport)
}
func (s *Server) Stdio(ctx context.Context) error { return s.Run(ctx, &mcp.StdioTransport{}) }

// Streams serves MCP over caller-owned streams, as used by cli.Run.
func (s *Server) Streams(ctx context.Context, in io.Reader, out io.Writer) error {
	return s.Run(ctx, &mcp.IOTransport{Reader: io.NopCloser(in), Writer: nopWriteCloser{out}})
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func (s *Server) instructions(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if req.Params == nil || req.Params.URI != InstructionsURI {
		return nil, mcp.ResourceNotFoundError(InstructionsURI)
	}
	text, _ := instructions.Read("overview")
	if len(s.cfg.Sources) > 0 {
		sources := make([]string, len(s.cfg.Sources))
		for i, source := range s.cfg.Sources {
			sources[i] = source.ID + " (" + source.Type + ")"
		}
		text += "\nConfigured sources, for `source:` filters: " + strings.Join(sources, ", ") + ".\n"
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: InstructionsURI, MIMEType: "text/markdown", Text: text}}}, nil
}

type ItemResult struct {
	Item       itemjson.Summary         `json:"item"`
	Transition *engine.TransitionResult `json:"transition,omitempty"`
}
type ItemsResult struct {
	Items []itemjson.Summary `json:"items"`
}
type NextClaimResult struct {
	Items        []itemjson.Summary       `json:"items"`
	Explanations []engine.NextExplanation `json:"explanations,omitempty"`
	engine.NextOutcome
}
type NextArgs struct {
	Limit   int    `json:"limit,omitempty" jsonschema:"Maximum number of ready items (0 means all); claim always selects at most one."`
	Query   string `json:"query,omitempty"`
	Claim   bool   `json:"claim,omitempty"`
	Start   bool   `json:"start,omitempty"`
	Explain bool   `json:"explain,omitempty"`
	Session string `json:"session,omitempty" jsonschema:"Required for claim; keep one stable session for the whole work loop."`
	Run     string `json:"run,omitempty"`
	AgentArgs
}
type QueryArgs struct {
	Query string `json:"query" jsonschema:"bl query-language expression."`
}
type EventsArgs struct {
	Since string `json:"since,omitempty" jsonschema:"Exclusive event ID; pass the previous cursor to continue."`
	Query string `json:"query,omitempty" jsonschema:"bl events query; event terms changed:, hook:, action:, and outcome: are enabled."`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum events to return (0 or omitted: 100)."`
}
type EventsResult struct {
	Events []hooks.Event `json:"events"`
	Cursor string        `json:"cursor,omitempty"`
	More   bool          `json:"more"`
}

// defaultEventLimit bounds an events call that omits limit.
const defaultEventLimit = 100

type ShowArgs struct {
	ID string `json:"id"`
}

type ChangesArgs struct {
	ID string `json:"id"`
}

type TourArgs struct {
	ID   string `json:"id"`
	File string `json:"file,omitempty" jsonschema:"Path to a CodeTour .tour file to store; omit to read the stored tours. See bl instructions tours."`
}

type HistoryArgs struct {
	ID     string `json:"id"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum entries to return (0 or omitted: all)."`
	Cursor string `json:"cursor,omitempty" jsonschema:"Exclusive entry ID; pass the previous cursor to continue."`
}

// AgentArgs identify the agent behind a session on the events a call records.
// MCP never reads them from the server's environment (design.md D40).
type AgentArgs struct {
	Agent        string `json:"agent,omitempty" jsonschema:"Agent kind running this session, such as claude, codex, or pi."`
	AgentSession string `json:"agentSession,omitempty" jsonschema:"The agent harness's own session ID; defaults to session."`
}

func callerContext(session, run string, agent AgentArgs) model.CallerContext {
	return model.CallerContext{Session: session, Run: run, Agent: agent.Agent, AgentSession: agent.AgentSession}
}

// withCaller binds a write's caller so its claim rules and events see it.
func (s *Server) withCaller(ctx context.Context, args WriteArgs) context.Context {
	return engine.WithClaimEvents(engine.WithCoordination(ctx, s.coordinator, callerContext(args.Session, args.Run, args.AgentArgs)), s.cfg)
}

type WriteArgs struct {
	Session string `json:"session,omitempty"`
	Run     string `json:"run,omitempty"`
	AgentArgs
	Version string `json:"version,omitempty" jsonschema:"Expected current item version; stale values are rejected without writing."`
}
type CreateArgs struct {
	WriteArgs
	Title       string   `json:"title"`
	Status      string   `json:"status,omitempty" jsonschema:"Configured status key; may be draft."`
	Type        string   `json:"type,omitempty"`
	Priority    string   `json:"priority,omitempty"`
	Description string   `json:"description,omitempty"`
	Criteria    []string `json:"criteria,omitempty"`
	Assignees   []string `json:"assignees,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Milestone   string   `json:"milestone,omitempty"`
	Due         string   `json:"due,omitempty"`
	Parent      string   `json:"parent,omitempty"`
	BlockedBy   []string `json:"blocked_by,omitempty"`
	Source      string   `json:"source,omitempty"`
	Sprint      string   `json:"sprint,omitempty" jsonschema:"current, next, or a sprint name or key (Linear)."`
}
type UpdateArgs struct {
	WriteArgs
	Confirm   bool           `json:"confirm,omitempty" jsonschema:"Acknowledge replacement of a Jira single assignee."`
	ID        string         `json:"id"`
	Operation string         `json:"operation,omitempty" jsonschema:"start, done, archive, unarchive, or replace_section. start and done set the workflow status the item's source accepts."`
	Fields    map[string]any `json:"fields,omitempty" jsonschema:"Field values matching bl set fields; suffix list fields with + or - for add/remove."`
	Section   string         `json:"section,omitempty" jsonschema:"Editable section for replace_section: description, criteria, dod, plan, notes, or summary."`
	Text      string         `json:"text,omitempty" jsonschema:"Replacement text for replace_section; empty clears the section."`
}
type AppendArgs struct {
	WriteArgs
	ID     string `json:"id"`
	Kind   string `json:"kind" jsonschema:"note, comment, or handoff (needs session; records this session's handoff)"`
	Text   string `json:"text"`
	Author string `json:"author,omitempty"`
}
type CriteriaArgs struct {
	WriteArgs
	ID        string `json:"id"`
	Operation string `json:"operation" jsonschema:"check, uncheck, add, set, or remove"`
	Index     int    `json:"index,omitempty"`
	Text      string `json:"text,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
	List      string `json:"list,omitempty" jsonschema:"criteria (default) or dod"`
}

func (s *Server) next(ctx context.Context, _ *mcp.CallToolRequest, args NextArgs) (*mcp.CallToolResult, any, error) {
	if args.Limit < 0 {
		return failed[any]("usage", "limit must be nonnegative")
	}
	if args.Start && !args.Claim {
		return failed[any]("usage", "start requires claim")
	}
	ctx = engine.WithClaimEvents(engine.WithEventOrigin(engine.WithCoordination(ctx, s.coordinator, callerContext(args.Session, args.Run, args.AgentArgs)), "mcp"), s.cfg)
	if args.Claim {
		s.writes.Lock()
		claimed, err := engine.NextClaim(ctx, s.cfg, engine.NextClaimOptions{Query: args.Query, Start: args.Start, Explain: args.Explain})
		s.writes.Unlock()
		result := NextClaimResult{itemjson.Summaries(claimed.Items), claimed.Explanations, claimed.NextOutcome}
		if err != nil {
			data := struct {
				NextClaimResult
				Reason  string `json:"reason"`
				Message string `json:"message"`
			}{result, reason(err), engine.ErrorMessage(err)}
			b, _ := json.Marshal(data)
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, data, nil
		}
		return nil, result, nil
	}
	snap, err := engine.LoadReads(ctx, s.cfg, false, args.Query)
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	parsed, err := snap.Parse(args.Query)
	if err != nil {
		return failed[any]("usage", err.Error())
	}
	if len(parsed.Sort) > 0 || parsed.Group != "" {
		return failed[any]("usage", "next uses readiness ranking; sort and group are not supported")
	}
	byRef := make(map[model.Ref]model.Item, len(snap.Query.Items))
	for _, i := range snap.Query.Items {
		byRef[i.Ref] = i
	}
	items := []model.Item{}
	for _, ranked := range snap.Ranking.Items {
		if ranked.Readiness == engine.Ready {
			if item, ok := byRef[ranked.Ref]; ok && parsed.Match(item, snap.Query, nil) {
				items = append(items, item)
				if args.Limit > 0 && len(items) >= args.Limit {
					break
				}
			}
		}
	}
	if args.Explain {
		included := map[model.Ref]bool{}
		for _, item := range items {
			included[item.Ref] = true
		}
		explanations := []engine.NextExplanation{}
		for _, explanation := range snap.Ranking.Items {
			if explanation.Readiness == engine.Ready && !included[explanation.Ref] {
				code := "limit"
				if !parsed.Match(byRef[explanation.Ref], snap.Query, nil) {
					code = "query"
				}
				explanation.Reasons = append(slices.Clone(explanation.Reasons), engine.Reason{Code: code})
			}
			explanations = append(explanations, engine.NextExplanation{Explanation: explanation, Included: included[explanation.Ref]})
		}
		return nil, struct {
			ItemsResult
			Explanations []engine.NextExplanation `json:"explanations"`
		}{ItemsResult{itemjson.Summaries(items)}, explanations}, nil
	}
	return nil, ItemsResult{itemjson.Summaries(items)}, nil
}
func (s *Server) query(ctx context.Context, _ *mcp.CallToolRequest, args QueryArgs) (*mcp.CallToolResult, any, error) {
	initial, err := query.Parse(args.Query, query.Options{})
	if err != nil {
		return failed[any]("usage", err.Error())
	}
	open, archived := initial.Scope()
	text := args.Query
	if open {
		text += " is:open"
	}
	ctx = engine.WithCoordination(ctx, s.coordinator, model.CallerContext{})
	snap, err := engine.LoadReads(ctx, s.cfg, archived, text)
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	parsed, err := snap.Parse(text)
	if err != nil {
		return failed[any]("usage", err.Error())
	}
	items := []model.Item{}
	for _, i := range snap.Query.Items {
		if parsed.Match(i, snap.Query, nil) {
			items = append(items, i)
		}
	}
	snap.Query.Sort(items, parsed.Sort)
	return nil, ItemsResult{itemjson.Summaries(items)}, nil
}
func (s *Server) events(ctx context.Context, _ *mcp.CallToolRequest, args EventsArgs) (*mcp.CallToolResult, any, error) {
	if args.Limit < 0 {
		return failed[any]("usage", "limit must be nonnegative")
	}
	if args.Limit == 0 {
		args.Limit = defaultEventLimit
	}
	q, err := query.Parse(args.Query, query.Options{Hook: true})
	if err != nil {
		return failed[any]("usage", err.Error())
	}
	if _, err = engine.LoadReads(ctx, s.cfg, false); err != nil {
		return failed[any](reason(err), err.Error())
	}
	page, err := engine.ReadEvents(ctx, s.cfg, args.Since, engine.EventMatcher(s.cfg, q), args.Limit)
	if err != nil {
		return failed[any](reason(err), engine.ErrorMessage(err))
	}
	return nil, EventsResult{page.Events, page.Cursor, page.More}, nil
}
func (s *Server) show(ctx context.Context, _ *mcp.CallToolRequest, args ShowArgs) (*mcp.CallToolResult, any, error) {
	if args.ID == "" {
		return failed[any]("usage", "id is required")
	}
	item, err := readItem(ctx, s.cfg, args.ID)
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	resource := ""
	if s.coordinator != nil {
		resource, err = s.coordinator.Resource(item)
		if err != nil {
			return failed[any](reason(err), err.Error())
		}
	}
	detail := itemjson.NewDetail(item, resource)
	detail.Claims, detail.ClaimsError = engine.ClaimHistory(ctx, s.coordinator, resource)
	detail.Sessions, detail.SessionsError = engine.ItemSessions(ctx, s.cfg, item, detail.Claims)
	return nil, detail, nil
}
func (s *Server) history(ctx context.Context, _ *mcp.CallToolRequest, args HistoryArgs) (*mcp.CallToolResult, any, error) {
	if args.ID == "" {
		return failed[any]("usage", "id is required")
	}
	item, err := readItem(ctx, s.cfg, args.ID)
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	page, err := history.Load(ctx, s.cfg, s.coordinator, item, engine.LinkedChanges(s.cfg, item.Ref)).Page(args.Cursor, args.Limit)
	if err != nil {
		return failed[any](reason(err), engine.ErrorMessage(err))
	}
	return nil, page, nil
}
func (s *Server) changes(ctx context.Context, _ *mcp.CallToolRequest, args ChangesArgs) (*mcp.CallToolResult, any, error) {
	if args.ID == "" {
		return failed[any]("usage", "id is required")
	}
	item, err := readItem(ctx, s.cfg, args.ID)
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	result, err := engine.ItemChanges(ctx, s.cfg, item.Ref)
	if err != nil {
		return failed[any](reason(err), engine.ErrorMessage(err))
	}
	return nil, result, nil
}
func (s *Server) tour(ctx context.Context, _ *mcp.CallToolRequest, args TourArgs) (*mcp.CallToolResult, any, error) {
	if args.ID == "" {
		return failed[any]("usage", "id is required")
	}
	if args.File == "" {
		result, err := engine.ReadTours(ctx, s.cfg, args.ID)
		if err != nil {
			return failed[any](reason(err), engine.ErrorMessage(err))
		}
		return nil, result, nil
	}
	result, err := engine.StoreTourFile(ctx, s.cfg, args.ID, args.File)
	if err != nil {
		return failed[any](reason(err), engine.ErrorMessage(err))
	}
	return nil, result, nil
}
func (s *Server) create(ctx context.Context, _ *mcp.CallToolRequest, args CreateArgs) (*mcp.CallToolResult, any, error) {
	ctx = s.withCaller(ctx, args.WriteArgs)
	if args.Title == "" {
		return failed[any]("usage", "title is required")
	}
	if args.Version != "" {
		return failed[any]("conflict", "a new item has no existing version")
	}
	status := args.Status
	if status == "" {
		for _, v := range s.cfg.Statuses {
			if v.Category == "todo" {
				status = v.Key
				break
			}
		}
	}
	if status == "" && len(s.cfg.Statuses) > 0 {
		status = s.cfg.Statuses[0].Key
	}
	sourceID, err := engine.CreateSource(ctx, s.cfg, args.Source, "", nil)
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	item := model.Item{Ref: model.Ref{Source: sourceID}, Title: args.Title, Status: status, Type: args.Type, Priority: model.Priority(args.Priority), Description: args.Description, Assignees: args.Assignees, Labels: args.Labels, Milestone: args.Milestone, Due: args.Due, Sprint: args.Sprint}
	if args.Parent != "" {
		parent, _, err := engine.ResolveRef(ctx, s.cfg, sourceID, model.Ref{ID: args.Parent})
		if err != nil {
			return failed[any](reason(err), err.Error())
		}
		item.Parent = &parent
	}
	for _, ref := range args.BlockedBy {
		blocked, _, err := engine.ResolveRef(ctx, s.cfg, sourceID, model.Ref{ID: ref})
		if err != nil {
			return failed[any](reason(err), err.Error())
		}
		item.BlockedBy = append(item.BlockedBy, blocked)
	}
	for _, c := range args.Criteria {
		if strings.TrimSpace(c) == "" || strings.ContainsAny(c, "\r\n") {
			return failed[any]("usage", "criteria must be nonempty and single-line")
		}
		item.Criteria = append(item.Criteria, model.Criterion{Text: c})
	}
	engine.ApplyCreateDefaults(s.cfg, sourceID, &item)
	if err := engine.ValidateItem(s.cfg, item); err != nil {
		return failed[any](reason(err), err.Error())
	}
	created, err := engine.ApplyWrite(engine.WithEventOrigin(ctx, "mcp"), s.cfg, model.Mutation{Op: model.Create, Ref: item.Ref, Item: &item})
	if ref := engine.ErrorCreated(err); ref != "" {
		b, _ := json.Marshal(map[string]string{"reason": reason(err), "message": engine.ErrorMessage(err), "created": ref})
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
	}
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	return nil, ItemResult{Item: itemjson.NewSummary(created)}, nil
}
func (s *Server) update(ctx context.Context, _ *mcp.CallToolRequest, args UpdateArgs) (*mcp.CallToolResult, any, error) {
	s.writes.Lock()
	defer s.writes.Unlock()
	ctx = s.withCaller(ctx, args.WriteArgs)
	var transition engine.TransitionResult
	ctx = engine.WithTransitionResult(ctx, &transition)
	if args.ID == "" || (len(args.Fields) == 0 && args.Operation == "") {
		return failed[any]("usage", "id and fields or operation are required")
	}
	item, err := readItem(ctx, s.cfg, args.ID)
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	version, err := expected(item, args.Version)
	if err != nil {
		return failed[any]("conflict", err.Error())
	}
	if args.Operation != "" && len(args.Fields) != 0 {
		return failed[any]("usage", "fields and operation cannot be combined")
	}
	if args.Operation == "start" || args.Operation == "done" {
		// Resolved per item source, then written like a status field update.
		category := map[string]model.StatusCategory{"start": model.Active, "done": model.Done}[args.Operation]
		status, err := engine.WorkflowStatus(ctx, s.cfg, item, category)
		if err != nil {
			return failed[any](reason(err), err.Error())
		}
		args.Operation, args.Fields = "", map[string]any{"status": status}
	}
	if args.Operation != "" {
		var op model.Operation
		section := ""
		switch args.Operation {
		case "archive":
			op = model.Archive
		case "unarchive":
			op = model.Unarchive
		case "replace_section":
			op = model.ReplaceSection
			sections := map[string]string{"description": "Description", "criteria": "Acceptance criteria", "dod": "Definition of done", "plan": "Plan", "notes": "Notes", "summary": "Summary"}
			var ok bool
			section, ok = sections[args.Section]
			if !ok {
				return failed[any]("rejected", "section must be description, criteria, dod, plan, notes, or summary")
			}
		default:
			return failed[any]("rejected", "operation must be start, done, archive, unarchive, or replace_section")
		}
		updated, err := engine.ApplyWrite(engine.WithEventOrigin(ctx, "mcp"), s.cfg, model.Mutation{Op: op, Ref: item.Ref, ExpectedVersion: version, Section: section, Text: args.Text})
		if err != nil {
			return failed[any](reason(err), err.Error())
		}
		return nil, ItemResult{Item: itemjson.NewSummary(updated)}, nil
	}
	changes := make([]model.Mutation, 0, len(args.Fields))
	// JSON objects are unordered; sorted keys make a batch deterministic and put
	// a field's set before its + and - changes.
	for _, field := range slices.Sorted(maps.Keys(args.Fields)) {
		value := args.Fields[field]
		op := model.Set
		if strings.HasSuffix(field, "+") {
			op, field = model.Add, strings.TrimSuffix(field, "+")
		} else if strings.HasSuffix(field, "-") {
			op, field = model.Remove, strings.TrimSuffix(field, "-")
		}
		if field == "label" {
			field = "labels"
		}
		if field == "assignee" {
			field = "assignees"
		}
		if engine.ListField(field) {
			values := []string{}
			switch v := value.(type) {
			case []any:
				for _, raw := range v {
					text, ok := raw.(string)
					if !ok {
						return failed[any]("rejected", "list field values must be strings")
					}
					values = append(values, text)
				}
			case []string:
				values = v
			case string:
				if v != "" {
					values = strings.Split(v, ",")
				}
			default:
				return failed[any]("rejected", "list field values must be strings")
			}
			if op == model.Set {
				value = values
			} else {
				for _, entry := range values {
					changes = append(changes, model.Mutation{Op: op, Field: field, Value: entry})
				}
				continue
			}
		}
		changes = append(changes, model.Mutation{Op: op, Field: field, Value: value})
	}
	changes, err = engine.ResolveChanges(ctx, s.cfg, item.Ref.Source, changes)
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	m := model.Mutation{Op: model.Batch, Changes: changes}
	// Like the CLI, one change is sent unbatched for sources without batches.
	if len(changes) == 1 {
		m = changes[0]
	}
	m.Ref, m.ExpectedVersion, m.Confirmed = item.Ref, version, args.Confirm
	updated, err := engine.ApplyWrite(engine.WithEventOrigin(ctx, "mcp"), s.cfg, m)
	if err != nil {
		return failedTransition(err, updated, transition)
	}
	result := ItemResult{Item: itemjson.NewSummary(updated)}
	if transition.Status != "" {
		result.Transition = &transition
	}
	return nil, result, nil
}
func (s *Server) append(ctx context.Context, _ *mcp.CallToolRequest, args AppendArgs) (*mcp.CallToolResult, any, error) {
	ctx = s.withCaller(ctx, args.WriteArgs)
	s.writes.Lock()
	defer s.writes.Unlock()
	ctx = engine.WithEventOrigin(ctx, "mcp")
	if args.ID == "" || strings.TrimSpace(args.Text) == "" {
		return failed[any]("usage", "id and nonempty text are required")
	}
	if args.Kind == "handoff" {
		return s.handoff(ctx, args)
	}
	item, err := readItem(ctx, s.cfg, args.ID)
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	version, err := expected(item, args.Version)
	if err != nil {
		return failed[any]("conflict", err.Error())
	}
	m := model.Mutation{Op: model.AppendNote, Text: args.Text}
	if args.Kind == "comment" {
		author, err := engine.CommentAuthor(s.cfg, item.Ref.Source, args.Author)
		if err != nil {
			return failed[any](reason(err), err.Error())
		}
		m = model.Mutation{Op: model.AppendComment, Comment: model.Comment{Author: author, Body: args.Text, Time: time.Now()}}
	} else if args.Kind != "note" {
		return failed[any]("rejected", "kind must be note, comment, or handoff")
	}
	updated, err := engine.ApplyWrite(ctx, s.cfg, model.Mutation{Op: model.Batch, Ref: item.Ref, ExpectedVersion: version, Changes: []model.Mutation{m}})
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	return nil, ItemResult{Item: itemjson.NewSummary(updated)}, nil
}

// HandoffResult mirrors bl handoff --json.
type HandoffResult struct {
	Event   string           `json:"event"`
	Comment string           `json:"comment,omitempty"`
	Item    itemjson.Summary `json:"item"`
}

func (s *Server) handoff(ctx context.Context, args AppendArgs) (*mcp.CallToolResult, any, error) {
	text := strings.TrimSpace(args.Text)
	if len(text) > engine.HandoffLimit {
		return failed[any]("invalid-value", fmt.Sprintf("handoff exceeds %d bytes", engine.HandoffLimit))
	}
	item, err := readItem(ctx, s.cfg, args.ID)
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	result, err := engine.RecordHandoff(ctx, s.cfg, item, cmp.Or(args.Version, item.Version), text)
	if err != nil {
		message := engine.ErrorMessage(err)
		if result.Event != "" {
			message += "; the handoff was recorded locally as event " + result.Event
		}
		return failed[any](reason(err), message)
	}
	return nil, HandoffResult{Event: result.Event, Comment: result.Comment, Item: itemjson.NewSummary(result.Item)}, nil
}
func (s *Server) criteria(ctx context.Context, _ *mcp.CallToolRequest, args CriteriaArgs) (*mcp.CallToolResult, any, error) {
	ctx = s.withCaller(ctx, args.WriteArgs)
	s.writes.Lock()
	defer s.writes.Unlock()
	ctx = engine.WithEventOrigin(ctx, "mcp")
	if args.ID == "" {
		return failed[any]("usage", "id is required")
	}
	item, err := readItem(ctx, s.cfg, args.ID)
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	version, err := expected(item, args.Version)
	if err != nil {
		return failed[any]("conflict", err.Error())
	}
	ops := map[string]model.Operation{"check": model.CheckCriterion, "uncheck": model.UncheckCriterion, "add": model.AddCriterion, "set": model.SetCriterion, "remove": model.RemoveCriterion}
	op, ok := ops[args.Operation]
	if !ok {
		return failed[any]("rejected", "operation must be check, uncheck, add, set, or remove")
	}
	lists := map[string]string{"": "Acceptance criteria", "criteria": "Acceptance criteria", "dod": "Definition of done"}
	section, ok := lists[args.List]
	if !ok {
		return failed[any]("usage", "list must be criteria or dod")
	}
	m := model.Mutation{Op: op, Section: section, Index: args.Index, Text: args.Text}
	if (op == model.AddCriterion || op == model.SetCriterion) && (strings.TrimSpace(args.Text) == "" || strings.ContainsAny(args.Text, "\r\n")) {
		return failed[any]("usage", "criterion must be nonempty and single-line")
	}
	if op != model.AddCriterion && args.Index < 1 {
		return failed[any]("usage", "criterion index must be positive")
	}
	if args.Evidence != "" {
		if op != model.CheckCriterion || strings.TrimSpace(args.Evidence) == "" || strings.ContainsAny(args.Evidence, "\r\n") {
			return failed[any]("usage", "evidence requires check and single-line text")
		}
		m.Evidence = &args.Evidence
	}
	updated, err := engine.ApplyWrite(ctx, s.cfg, model.Mutation{Op: model.Batch, Ref: item.Ref, ExpectedVersion: version, Changes: []model.Mutation{m}})
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	return nil, ItemResult{Item: itemjson.NewSummary(updated)}, nil
}

func readItem(ctx context.Context, cfg project.Config, id string) (model.Item, error) {
	matches, err := engine.ReadDetail(ctx, cfg, id)
	if err != nil {
		return model.Item{}, err
	}
	if len(matches) == 0 {
		return model.Item{}, source.Refuse("not-found", "item not found: "+id)
	}
	if len(matches) > 1 {
		return model.Item{}, source.Refuse("rejected", "ambiguous item ID: "+id)
	}
	return matches[0], nil
}
func expected(item model.Item, version string) (string, error) {
	if version != "" && version != item.Version {
		return "", fmt.Errorf("expected version does not match current content")
	}
	return item.Version, nil
}
func ErrorReason(err error) string { return engine.ErrorReason(err) }
func reason(err error) string      { return engine.ErrorReason(err) }
func failed[T any](reason, message string) (*mcp.CallToolResult, T, error) {
	b, _ := json.Marshal(map[string]string{"reason": reason, "message": message})
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, *new(T), nil
}

// ServeStdio starts the stdio server. Diagnostics must be written to stderr by callers.
func ServeStdio(ctx context.Context, cfg project.Config) error { return New(cfg).Stdio(ctx) }

func ServeStreams(ctx context.Context, cfg project.Config, in io.Reader, out io.Writer) error {
	return New(cfg).Streams(ctx, in, out)
}
