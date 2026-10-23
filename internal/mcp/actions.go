package mcp

import (
	"context"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ActionArgs struct {
	Name    string `json:"name" jsonschema:"Configured action name"`
	ID      string `json:"id" jsonschema:"Item ID or source-qualified ref"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"Explicitly acknowledge the action confirmation"`
}

func (s *Server) action(ctx context.Context, _ *mcp.CallToolRequest, args ActionArgs) (*mcp.CallToolResult, any, error) {
	if args.Name == "" || args.ID == "" {
		return failed[any]("usage", "name and id are required")
	}
	ctx = engine.WithCoordination(ctx, s.coordinator, model.CallerContext{})
	result, err := engine.Action(engine.WithEventOrigin(ctx, "mcp"), s.cfg, args.Name, args.ID, args.Confirm)
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	if !result.Detached && result.Event.Outcome != "ok" {
		return failed[any]("rejected", "action "+result.Event.Outcome+": "+result.Message)
	}
	return nil, result, nil
}
