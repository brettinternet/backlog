package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/brettinternet/backlog/internal/changes"
	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
)

// Pull request statuses, fixed like the Issues adapter's (design.md D44).
const (
	PullDraft = "pr-draft"
	PullOpen  = "pr-open"
)

// Pulls is a read-only source of a repository's open pull requests, read
// through the forge that linked changes use (design.md D44). The engine hides
// each one an item from another source links to.
type Pulls struct {
	id    string
	forge *Forge
}

// NewPulls reads forge's open pull requests as source id's items.
func NewPulls(id string, forge *Forge) *Pulls { return &Pulls{id: id, forge: forge} }

func (p *Pulls) Info() source.Info {
	return source.Info{ID: p.id, Kind: "github-pulls", Display: p.forge.s.o.Repo + " pull requests", Capabilities: model.Capabilities{}}
}

// Load returns every open pull request not opened by an app. A failed
// refresh keeps the cached list, marked stale.
func (p *Pulls) Load(ctx context.Context, _ source.Cursor) (source.Delta, error) {
	prs, err := p.forge.Recent(ctx)
	if err != nil && len(prs) == 0 {
		return source.Delta{}, err
	}
	delta := source.Delta{Coverage: source.Complete, Items: []model.Item{}}
	for _, pr := range prs {
		if pr.State == changes.StateOpen && !pr.Bot {
			delta.Items = append(delta.Items, p.item(pr))
		}
	}
	if err != nil {
		delta.Stale = map[model.Ref]bool{}
		for _, item := range delta.Items {
			delta.Stale[item.Ref] = true
		}
		delta.Findings = []source.Finding{{Code: "stale", Severity: "warning", Message: p.forge.Name() + " refresh failed: " + err.Error()}}
	}
	return delta, nil
}

func (p *Pulls) Read(ctx context.Context, id string) (model.Item, error) {
	number, err := strconv.Atoi(strings.TrimPrefix(id, "#"))
	if err != nil || !strings.HasPrefix(id, "#") || number < 1 {
		return model.Item{}, source.Refuse("invalid-id", "a pull request ID is #N")
	}
	prs, err := p.forge.Recent(ctx)
	for _, pr := range prs {
		if pr.Number == number && pr.State == changes.StateOpen && !pr.Bot {
			return p.item(pr), nil
		}
	}
	if err != nil {
		return model.Item{}, err
	}
	return model.Item{}, source.Refuse("not-found", id)
}

func (p *Pulls) Apply(context.Context, model.Mutation) (model.Item, error) {
	return model.Item{}, source.Refuse("rejected", "GitHub pull requests are read-only")
}

// item is pr as an item. Fields keep what linking reads beyond the item's
// own text: the head branch, closing issue node IDs, and fork origin.
func (p *Pulls) item(pr changes.PullRequest) model.Item {
	item := model.Item{
		Ref: model.Ref{Source: p.id, ID: "#" + strconv.Itoa(pr.Number)}, Title: pr.Title, Description: pr.Body,
		Status: PullOpen, Category: model.Active, Refs: []string{pr.URL}, URL: pr.URL,
		Created: pr.Created, Updated: pr.Updated,
		Fields: map[string]any{"head": pr.Head},
	}
	if pr.Draft {
		item.Status, item.Category = PullDraft, model.Backlog
	}
	if pr.Author != "" {
		item.Assignees = []string{pr.Author}
	}
	if pr.CrossRepository {
		item.Fields["crossRepository"] = true
	}
	if len(pr.Closes) > 0 {
		closes := make([]any, len(pr.Closes))
		for n, id := range pr.Closes {
			closes[n] = id
		}
		item.Fields["closes"] = closes
	}
	raw, _ := json.Marshal(item)
	hash := sha256.Sum256(raw)
	item.Version = hex.EncodeToString(hash[:])
	return item
}

// PullRequest is what linking needs of a github-pulls item.
func PullRequest(item model.Item) changes.PullRequest {
	number, _ := strconv.Atoi(strings.TrimPrefix(item.Ref.ID, "#"))
	pr := changes.PullRequest{Number: number, Title: item.Title, Body: item.Description, URL: item.URL, State: changes.StateOpen, Draft: item.Status == PullDraft, Created: item.Created, Updated: item.Updated}
	pr.Head, _ = item.Fields["head"].(string)
	pr.CrossRepository, _ = item.Fields["crossRepository"].(bool)
	switch closes := item.Fields["closes"].(type) {
	case []any:
		for _, id := range closes {
			if s, ok := id.(string); ok {
				pr.Closes = append(pr.Closes, s)
			}
		}
	case []string:
		pr.Closes = closes
	}
	return pr
}
