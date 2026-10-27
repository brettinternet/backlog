package engine

import (
	"context"
	"testing"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/project"
)

// A provider attributes every comment to its token's user, so a remote comment
// needs no local author and must refuse one that names anyone else.
func TestCommentAuthorPerSource(t *testing.T) {
	t.Parallel()
	accounts := map[string][]project.AccountConfig{"github": {{Host: "github.com", User: "alice"}}}
	sources := []project.SourceConfig{{ID: "tasks", Type: "native"}, {ID: "gh", Type: "github", Account: "alice"}}
	for _, tc := range []struct {
		name, source, me, author, want, reason string
	}{
		{name: "remote default", source: "gh", want: "alice"},
		{name: "remote same user", source: "gh", author: "alice", want: "alice"},
		{name: "remote other author", source: "gh", me: "@bob", author: "@bob", reason: "rejected"},
		{name: "native me.default", source: "tasks", me: "@bob", want: "@bob"},
		{name: "native explicit", source: "tasks", me: "@bob", author: "@agent", want: "@agent"},
		{name: "native unset", source: "tasks", reason: "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := project.Config{Sources: sources, Accounts: accounts, Me: project.MeConfig{Default: tc.me}}
			changes := []model.Mutation{{Op: model.AppendComment, Comment: model.Comment{Author: tc.author, Body: "x"}}}
			got, err := ResolveChanges(context.Background(), cfg, tc.source, changes)
			if tc.reason != "" {
				if ErrorReason(err) != tc.reason {
					t.Fatalf("err = %v, want %s", err, tc.reason)
				}
				return
			}
			if err != nil || got[0].Comment.Author != tc.want {
				t.Fatalf("author = %v, %v; want %q", got, err, tc.want)
			}
		})
	}
}
