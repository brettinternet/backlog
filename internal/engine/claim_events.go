package engine

import (
	"context"
	"crypto/rand"

	"github.com/brettinternet/backlog/internal/hooks"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

type claimEventsKey struct{}

func WithClaimEvents(ctx context.Context, cfg project.Config) context.Context {
	return context.WithValue(ctx, claimEventsKey{}, cfg)
}
func recordClaimEvent(ctx context.Context, item model.Item, kind string, result *model.ClaimResult) {
	cfg, ok := ctx.Value(claimEventsKey{}).(project.Config)
	if !ok || cfg.Paths.StateDir == "" {
		return
	}
	origin := "cli"
	if e, ok := ctx.Value(eventContextKey{}).(eventContext); ok {
		origin = e.Origin
	}
	versions := []string{rand.Text()}
	if result.Claim != nil {
		versions = []string{result.Claim.ID}
	}
	e := makeEvent(ctx, cfg, item, kind, origin, versions)
	err := hooks.New(cfg.Paths.StateDir, cfg.Project.ID).Update(ctx, func(tx *hooks.Transaction) error { _, err := tx.Append(e); return err })
	if err != nil {
		result.EventError = err.Error()
	}
}
