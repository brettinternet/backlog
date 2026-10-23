package github

import (
	"context"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
)

type projectItem struct {
	ID        string
	UpdatedAt time.Time
	Content   *struct {
		TypeName   string `json:"__typename"`
		ID         string
		Number     int
		Repository struct{ NameWithOwner string }
	}
	Value struct {
		Name, OptionID string
		Field          struct{ ID string }
	} `json:"fieldValueByName"`
}

const discoveryQuery = `query($projectID:ID!,$after:String){node(id:$projectID){... on ProjectV2{id number viewerCanUpdate owner{__typename ... on User{login} ... on Organization{login}} fields(first:100,after:$after){nodes{... on ProjectV2SingleSelectField{id name options{id name}}} pageInfo{hasNextPage endCursor}}}}}`
const projectItemsQuery = `query($projectID:ID!,$fieldName:String!,$after:String){node(id:$projectID){... on ProjectV2{id number owner{... on User{login} ... on Organization{login}} items(first:100,after:$after){nodes{id updatedAt content{__typename ... on Issue{id number repository{nameWithOwner}}} fieldValueByName(name:$fieldName){... on ProjectV2ItemFieldSingleSelectValue{name optionId field{... on ProjectV2SingleSelectField{id}}}}} pageInfo{hasNextPage endCursor}}}}}`

func (s *Source) projectIdentity(id string, number int, owner string) error {
	p := s.o.Project
	if id != p.ID || number != p.Number || !strings.EqualFold(owner, p.Owner) {
		return source.Refuse("unavailable", "Projects v2 binding changed")
	}
	return nil
}
func (s *Source) discover(ctx context.Context, c remote.Credential) (string, error) {
	vars := map[string]any{"projectID": s.o.Project.ID}
	seen := map[string]bool{}
	for {
		var data struct {
			Node *struct {
				ID              string
				Number          int
				ViewerCanUpdate bool
				Owner           struct {
					Login    string
					TypeName string `json:"__typename"`
				}
				Fields struct {
					Nodes []struct {
						ID, Name string
						Options  []struct{ ID, Name string }
					}
					PageInfo *pageInfo
				}
			}
		}
		if err := s.query(ctx, c, discoveryQuery, vars, &data); err != nil {
			return "", err
		}
		if data.Node == nil {
			return "", source.Refuse("unavailable", "project inaccessible")
		}
		if err := s.projectIdentity(data.Node.ID, data.Node.Number, data.Node.Owner.Login); err != nil {
			return "", err
		}
		if s.o.Account.OAuth {
			if data.Node.Owner.TypeName != "Organization" {
				return "", source.Refuse("rejected", "personal Projects v2 require a credential helper")
			}
			s.projectWritable = data.Node.ViewerCanUpdate && s.projectGrants[strings.ToLower(data.Node.Owner.Login)]
		}
		for _, field := range data.Node.Fields.Nodes {
			if field.ID != s.o.Project.FieldID {
				continue
			}
			if field.Name == "" || len(field.Options) == 0 {
				return "", source.Refuse("unavailable", "project field changed")
			}
			options := map[string]bool{}
			for _, o := range field.Options {
				options[o.ID] = true
			}
			for id := range s.o.Project.Options {
				if !options[id] {
					return "", source.Refuse("unavailable", "project option disappeared")
				}
			}
			return field.Name, nil
		}
		next, err := advance(data.Node.Fields.PageInfo, seen)
		if err != nil {
			return "", err
		}
		if next == "" {
			return "", source.Refuse("unavailable", "project single-select field missing")
		}
		vars["after"] = next
	}
}

// Always enumerate independently of issue updatedAt and the REST ETag. Project
// field changes and dependency removal need not change the issue timestamp.
func (s *Source) projects(ctx context.Context, c remote.Credential) (map[string]projectItem, error) {
	items := map[string]projectItem{}
	if s.o.Project == nil {
		return items, nil
	}
	name, err := s.discover(ctx, c)
	if err != nil {
		return nil, err
	}
	vars := map[string]any{"projectID": s.o.Project.ID, "fieldName": name}
	seen := map[string]bool{}
	for {
		var data struct {
			Node *struct {
				ID     string
				Number int
				Owner  struct{ Login string }
				Items  struct {
					Nodes    []projectItem
					PageInfo *pageInfo
				}
			}
		}
		if err = s.query(ctx, c, projectItemsQuery, vars, &data); err != nil {
			return nil, err
		}
		if data.Node == nil {
			return nil, source.Refuse("unavailable", "project inaccessible")
		}
		if err = s.projectIdentity(data.Node.ID, data.Node.Number, data.Node.Owner.Login); err != nil {
			return nil, err
		}
		for _, item := range data.Node.Items.Nodes {
			if item.Content == nil || item.Content.TypeName != "Issue" || !strings.EqualFold(item.Content.Repository.NameWithOwner, s.o.Repo) {
				continue
			}
			if item.ID == "" || item.Content.ID == "" || item.Content.Number < 1 || item.UpdatedAt.IsZero() || item.Value.OptionID != "" && item.Value.Field.ID != s.o.Project.FieldID {
				return nil, source.Refuse("unavailable", "invalid project item binding")
			}
			if _, exists := items[item.Content.ID]; exists {
				return nil, source.Refuse("unavailable", "ambiguous project issue")
			}
			items[item.Content.ID] = item
		}
		next, err := advance(data.Node.Items.PageInfo, seen)
		if err != nil {
			return nil, err
		}
		if next == "" {
			return items, nil
		}
		vars["after"] = next
	}
}

const projectStatusMutation = `mutation($projectID:ID!,$itemID:ID!,$fieldID:ID!,$optionID:String!){updateProjectV2ItemFieldValue(input:{projectId:$projectID,itemId:$itemID,fieldId:$fieldID,value:{singleSelectOptionId:$optionID}}){projectV2Item{id}}}`
