package jira

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

type changelogItem struct{ Field, FromString, ToString string }

type changelogPage struct {
	StartAt, MaxResults, Total int
	IsLast                     bool
	Values                     []struct {
		ID      string
		Author  *struct{ DisplayName string }
		Created string
		Items   []changelogItem
	}
}

// Activity reads an issue's changelog, oldest first. Past ActivityLimit it
// skips ahead so the newest entries are kept.
func (s *Source) Activity(ctx context.Context, item model.Item) ([]model.Activity, bool, error) {
	id := item.Ref.ID
	if !numeric(id) {
		return nil, false, source.Refuse("invalid-id", "use the numeric Jira issue ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.authenticate(ctx)
	if err != nil {
		return nil, false, err
	}
	var out []model.Activity
	truncated := false
	for start := 0; ; {
		var page changelogPage
		if err := s.request(ctx, c, http.MethodGet, fmt.Sprintf("issue/%s/changelog?startAt=%d&maxResults=100", id, start), nil, &page); err != nil {
			return nil, false, err
		}
		if page.StartAt != start {
			return nil, false, source.Refuse("unavailable", "invalid Jira changelog page")
		}
		if start == 0 && page.Total > source.ActivityLimit {
			truncated, start = true, page.Total-source.ActivityLimit
			continue
		}
		for _, v := range page.Values {
			a, err := changelogActivity(v.ID, v.Created, v.Items)
			if err != nil {
				return nil, false, err
			}
			if v.Author != nil {
				a.Actor = v.Author.DisplayName
			}
			if a.Summary != "" {
				out = append(out, a)
			}
		}
		start += len(page.Values)
		if page.IsLast || len(page.Values) == 0 || start >= page.Total {
			return out, truncated, nil
		}
	}
}

func changelogActivity(id, created string, items []changelogItem) (model.Activity, error) {
	a := model.Activity{ID: id}
	if id == "" || timestamp(&a.Time, created) != nil {
		return a, source.Refuse("unavailable", "invalid Jira changelog entry")
	}
	a.Time = a.Time.UTC()
	parts := make([]string, 0, len(items))
	for _, item := range items {
		from, to := cmp.Or(item.FromString, "∅"), cmp.Or(item.ToString, "∅")
		field := strings.ToLower(item.Field)
		if strings.ContainsAny(from+to, "\r\n") || len(from)+len(to) > 60 {
			parts = append(parts, field+" changed")
		} else {
			parts = append(parts, field+" "+from+" → "+to)
		}
	}
	a.Summary = strings.Join(parts, "; ")
	return a, nil
}

var _ source.ActivityReader = (*Source)(nil)
