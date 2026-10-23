package mcp

import (
	"context"
	"encoding/json"

	"github.com/brettinternet/backlog/internal/engine"
	"github.com/brettinternet/backlog/internal/itemjson"
	"github.com/brettinternet/backlog/internal/model"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type ClaimArgs struct {
	ID      string `json:"id"`
	Session string `json:"session"`
	Run     string `json:"run,omitempty"`
}
type HeartbeatArgs struct {
	Session string `json:"session"`
	Run     string `json:"run,omitempty"`
}

func (s *Server) claim(ctx context.Context, _ *sdk.CallToolRequest, args ClaimArgs) (*sdk.CallToolResult, any, error) {
	return s.coordinate(ctx, args, false)
}
func (s *Server) release(ctx context.Context, _ *sdk.CallToolRequest, args ClaimArgs) (*sdk.CallToolResult, any, error) {
	return s.coordinate(ctx, args, true)
}
func (s *Server) coordinate(ctx context.Context, args ClaimArgs, release bool) (*sdk.CallToolResult, any, error) {
	caller := model.CallerContext{Session: args.Session, Run: args.Run}
	if err := engine.RequireSession(caller); err != nil {
		return failed[any](reason(err), err.Error())
	}
	ctx = engine.WithClaimEvents(engine.WithEventOrigin(engine.WithCoordination(ctx, s.coordinator, caller), "mcp"), s.cfg)
	item, err := readItem(ctx, s.cfg, args.ID)
	if err != nil {
		return failed[any](reason(err), err.Error())
	}
	var result model.ClaimResult
	if release {
		result = engine.ReleaseItem(ctx, item)
	} else {
		result = engine.ClaimItem(ctx, item)
	}
	return claimResponse(result)
}
func (s *Server) heartbeat(ctx context.Context, _ *sdk.CallToolRequest, args HeartbeatArgs) (*sdk.CallToolResult, any, error) {
	ctx = engine.WithClaimEvents(engine.WithEventOrigin(engine.WithCoordination(ctx, s.coordinator, model.CallerContext{Session: args.Session, Run: args.Run}), "mcp"), s.cfg)
	return claimResponse(engine.Heartbeat(ctx))
}
func claimResponse(result model.ClaimResult) (*sdk.CallToolResult, any, error) {
	if result.Reason != "" {
		b, _ := json.Marshal(result)
		return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}}, result, nil
	}
	return nil, result, nil
}
func failedTransition(err error, item model.Item, transition engine.TransitionResult) (*sdk.CallToolResult, any, error) {
	if transition.Status == "" {
		return failed[any](reason(err), engine.ErrorMessage(err))
	}
	data := struct {
		Reason     string                  `json:"reason"`
		Message    string                  `json:"message"`
		Item       itemjson.Summary        `json:"item"`
		Transition engine.TransitionResult `json:"transition"`
	}{reason(err), engine.ErrorMessage(err), itemjson.NewSummary(item), transition}
	b, _ := json.Marshal(data)
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}}, data, nil
}
