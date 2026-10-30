package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/oauth"
	"github.com/brettinternet/backlog/internal/source/remote"
)

// Discover live installation grants before every OAuth source use, including
// writes. A scope header or the user's repository role alone is not app consent.
// repo must be installed for reads; a project-only source passes "" on use,
// leaving unreadable repositories redacted, and each write's repository after.
func (s *Source) appPermissions(ctx context.Context, c remote.Credential, repo string) error {
	get := func(ctx context.Context, path string) ([]byte, error) {
		response, err := s.request(ctx, c, http.MethodGet, s.base()+path, nil)
		return response.Body, err
	}
	installations, err := oauth.GitHubInstallations(ctx, get)
	if err != nil {
		return err
	}
	expected := oauth.GitHubReadApp
	if c.HasScope("write") {
		expected = oauth.GitHubWriteApp
	}
	s.issueWrite, s.pullWrite = false, false
	s.projectWritable = false
	s.projectGrants = map[string]bool{}
	readable := false
	for _, i := range installations {
		if i.AppSlug != expected {
			return oauth.Failure(oauth.CodeInsufficientScope, time.Time{})
		}
		if i.SuspendedAt != nil {
			continue
		}
		if i.Account.Type == "Organization" && i.Permissions["organization_projects"] == "write" {
			s.projectGrants[strings.ToLower(i.Account.Login)] = true
		}
		if owner, _, _ := strings.Cut(repo, "/"); !strings.EqualFold(i.Account.Login, owner) {
			continue
		}
		if i.Permissions["issues"] != "read" && i.Permissions["issues"] != "write" {
			continue
		}
		seen := map[int64]bool{}
		total := -1
		for page := 1; page <= 100; page++ {
			raw, err := get(ctx, "/user/installations/"+strconv.FormatInt(i.ID, 10)+"/repositories?per_page=100&page="+strconv.Itoa(page))
			if err != nil {
				return err
			}
			var r struct {
				Total        *int `json:"total_count"`
				Repositories []struct {
					ID       int64  `json:"id"`
					FullName string `json:"full_name"`
				} `json:"repositories"`
			}
			if json.Unmarshal(raw, &r) != nil || r.Total == nil || *r.Total < 0 || *r.Total > 10000 || total >= 0 && total != *r.Total || len(r.Repositories) > 100 {
				return oauth.Failure(oauth.CodeProviderDown, time.Time{})
			}
			total = *r.Total
			for _, entry := range r.Repositories {
				if entry.ID <= 0 || seen[entry.ID] || entry.FullName == "" {
					return oauth.Failure(oauth.CodeProviderDown, time.Time{})
				}
				seen[entry.ID] = true
				if strings.EqualFold(entry.FullName, repo) {
					readable = true
					s.issueWrite = i.Permissions["issues"] == "write"
					s.pullWrite = i.Permissions["pull_requests"] == "write"
				}
			}
			if len(seen) == total {
				break
			}
			if len(seen) > total || len(r.Repositories) != 100 || page == 100 {
				return oauth.Failure(oauth.CodeProviderDown, time.Time{})
			}
		}
	}
	if !readable && repo != "" {
		return oauth.Failure(oauth.CodeInsufficientScope, time.Time{})
	}
	return nil
}
