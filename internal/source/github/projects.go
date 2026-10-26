package github

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/model"
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
	Sprint struct {
		IterationID string `json:"iterationId"`
	} `json:"sprint"`
}

// ymd decodes GitHub's Date scalar (a bare YYYY-MM-DD, not a DateTime), used
// by Projects v2 iteration start dates.
type ymd time.Time

func (d *ymd) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	t, err := time.Parse("2006-01-02", text)
	if err != nil {
		return err
	}
	*d = ymd(t)
	return nil
}
func (d ymd) time() time.Time { return time.Time(d) }

// iterationNode is one entry of a ProjectV2IterationField's configuration,
// current/upcoming or completed.
type iterationNode struct {
	ID, Title string
	StartDate ymd
	Duration  int
}

// iterationField is the discovered Projects v2 iteration field: its display
// name, for fieldValueByName, and its catalog classified against the clock
// when the field was discovered. Query evaluation never reclassifies it.
type iterationField struct {
	name    string
	sprints []model.Sprint
}

const discoveryQuery = `query($projectID:ID!,$after:String){node(id:$projectID){... on ProjectV2{id number viewerCanUpdate owner{__typename ... on User{login} ... on Organization{login}} fields(first:100,after:$after){nodes{... on ProjectV2SingleSelectField{id name options{id name}}} pageInfo{hasNextPage endCursor}}}}}`
const projectItemsQuery = `query($projectID:ID!,$fieldName:String!,$after:String){node(id:$projectID){... on ProjectV2{id number owner{... on User{login} ... on Organization{login}} items(first:100,after:$after){nodes{id updatedAt content{__typename ... on Issue{id number repository{nameWithOwner}}} fieldValueByName(name:$fieldName){... on ProjectV2ItemFieldSingleSelectValue{name optionId field{... on ProjectV2SingleSelectField{id}}}}} pageInfo{hasNextPage endCursor}}}}}`

// iterationFragment extends field discovery to the iteration field's
// configuration. Requested only with sprint demand, matching Linear's
// withCycles: a sprint-free process makes no iteration-shaped requests.
const iterationFragment = `... on ProjectV2IterationField{id name configuration{iterations{id title startDate duration} completedIterations{id title startDate duration}}}`

func withIterations(q string) string {
	return strings.Replace(q, "nodes{... on ProjectV2SingleSelectField", "nodes{"+iterationFragment+" ... on ProjectV2SingleSelectField", 1)
}

// withSprintValue adds the aliased iteration value to the project items
// query. Requested only once a sprint field was discovered under demand.
func withSprintValue(q string) string {
	q = strings.Replace(q, "$projectID:ID!,$fieldName:String!,$after:String", "$projectID:ID!,$fieldName:String!,$sprintFieldName:String!,$after:String", 1)
	return strings.Replace(q, "fieldValueByName(name:$fieldName){... on ProjectV2ItemFieldSingleSelectValue{name optionId field{... on ProjectV2SingleSelectField{id}}}}", "fieldValueByName(name:$fieldName){... on ProjectV2ItemFieldSingleSelectValue{name optionId field{... on ProjectV2SingleSelectField{id}}}} sprint:fieldValueByName(name:$sprintFieldName){... on ProjectV2ItemFieldIterationValue{iterationId}}", 1)
}

func (s *Source) projectIdentity(id string, number int, owner string) error {
	p := s.o.Project
	if id != p.ID || number != p.Number || !strings.EqualFold(owner, p.Owner) {
		return source.Refuse("unavailable", "Projects v2 binding changed")
	}
	return nil
}

// discover resolves the status field's display name for fieldValueByName and,
// when sprints is true, the iteration field: chosen by IterationFieldID if
// the source config names one, else the single iteration field present. A
// project with none is not sprint-capable (iteration is nil, no error);
// several with no configured choice is a refusal, not a guess.
func (s *Source) discover(ctx context.Context, c remote.Credential, sprints bool) (string, *iterationField, error) {
	q := discoveryQuery
	if sprints {
		q = withIterations(discoveryQuery)
	}
	vars := map[string]any{"projectID": s.o.Project.ID}
	seen := map[string]bool{}
	statusName := ""
	type candidate struct {
		name                  string
		iterations, completed []iterationNode
	}
	var candidates []candidate
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
						ID, Name      string
						Options       []struct{ ID, Name string }
						Configuration *struct {
							Iterations          []iterationNode
							CompletedIterations []iterationNode
						}
					}
					PageInfo *pageInfo
				}
			}
		}
		if err := s.query(ctx, c, q, vars, &data); err != nil {
			return "", nil, err
		}
		if data.Node == nil {
			return "", nil, source.Refuse("unavailable", "project inaccessible")
		}
		if err := s.projectIdentity(data.Node.ID, data.Node.Number, data.Node.Owner.Login); err != nil {
			return "", nil, err
		}
		if s.o.Account.OAuth {
			if data.Node.Owner.TypeName != "Organization" {
				return "", nil, source.Refuse("rejected", "personal Projects v2 require a credential helper")
			}
			s.projectWritable = data.Node.ViewerCanUpdate && s.projectGrants[strings.ToLower(data.Node.Owner.Login)]
		}
		for _, field := range data.Node.Fields.Nodes {
			if field.ID == s.o.Project.FieldID {
				if field.Name == "" || len(field.Options) == 0 {
					return "", nil, source.Refuse("unavailable", "project field changed")
				}
				options := map[string]bool{}
				for _, o := range field.Options {
					options[o.ID] = true
				}
				for id := range s.o.Project.Options {
					if !options[id] {
						return "", nil, source.Refuse("unavailable", "project option disappeared")
					}
				}
				statusName = field.Name
			}
			if sprints && field.Configuration != nil {
				if field.ID == "" || field.Name == "" {
					return "", nil, source.Refuse("unavailable", "invalid Projects v2 iteration field")
				}
				if s.o.Project.IterationFieldID == "" || field.ID == s.o.Project.IterationFieldID {
					candidates = append(candidates, candidate{field.Name, field.Configuration.Iterations, field.Configuration.CompletedIterations})
				}
			}
		}
		next, err := advance(data.Node.Fields.PageInfo, seen)
		if err != nil {
			return "", nil, err
		}
		if next == "" {
			break
		}
		vars["after"] = next
	}
	if statusName == "" {
		return "", nil, source.Refuse("unavailable", "project single-select field missing")
	}
	if !sprints {
		return statusName, nil, nil
	}
	if s.o.Project.IterationFieldID != "" && len(candidates) == 0 {
		return "", nil, source.Refuse("unavailable", "configured iteration field missing")
	}
	if len(candidates) > 1 {
		return "", nil, source.Refuse("rejected", "multiple Projects v2 iteration fields; set iterationFieldId")
	}
	if len(candidates) == 0 {
		return statusName, nil, nil
	}
	catalog, err := s.classify(candidates[0].iterations, candidates[0].completed)
	if err != nil {
		return "", nil, err
	}
	return statusName, &iterationField{name: candidates[0].name, sprints: catalog}, nil
}

// classify assigns iteration states per design.md D31: an entry in
// completedIterations is always closed; within iterations (current and
// upcoming), the one with startDate <= today < startDate+duration is active
// and the rest are future. A break with none matching leaves them all future,
// never active. This runs once, when the catalog is fetched; query evaluation
// only ever reads the resulting states.
func (s *Source) classify(iterations, completed []iterationNode) ([]model.Sprint, error) {
	now := s.o.Now().UTC()
	catalog := make([]model.Sprint, 0, len(iterations)+len(completed))
	for _, it := range completed {
		if it.ID == "" || it.Title == "" || it.Duration < 1 {
			return nil, source.Refuse("unavailable", "invalid Projects v2 iteration")
		}
		start := it.StartDate.time()
		catalog = append(catalog, model.Sprint{Key: it.ID, Name: it.Title, State: model.SprintClosed, Start: start, End: start.AddDate(0, 0, it.Duration), Source: s.o.ID})
	}
	activeIdx := -1
	for i, it := range iterations {
		if it.ID == "" || it.Title == "" || it.Duration < 1 {
			return nil, source.Refuse("unavailable", "invalid Projects v2 iteration")
		}
		start := it.StartDate.time()
		end := start.AddDate(0, 0, it.Duration)
		if activeIdx == -1 && !start.After(now) && now.Before(end) {
			activeIdx = i
		}
	}
	for i, it := range iterations {
		start := it.StartDate.time()
		state := model.SprintFuture
		if i == activeIdx {
			state = model.SprintActive
		}
		catalog = append(catalog, model.Sprint{Key: it.ID, Name: it.Title, State: state, Start: start, End: start.AddDate(0, 0, it.Duration), Source: s.o.ID})
	}
	return catalog, nil
}

// Always enumerate independently of issue updatedAt and the REST ETag. Project
// field changes and dependency removal need not change the issue timestamp.
// sprints requests the iteration field and value only on demand.
func (s *Source) projects(ctx context.Context, c remote.Credential, sprints bool) (map[string]projectItem, []model.Sprint, error) {
	items := map[string]projectItem{}
	if s.o.Project == nil {
		return items, nil, nil
	}
	statusName, iteration, err := s.discover(ctx, c, sprints)
	if err != nil {
		return nil, nil, err
	}
	q := projectItemsQuery
	vars := map[string]any{"projectID": s.o.Project.ID, "fieldName": statusName}
	if iteration != nil {
		q = withSprintValue(projectItemsQuery)
		vars["sprintFieldName"] = iteration.name
	}
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
		if err = s.query(ctx, c, q, vars, &data); err != nil {
			return nil, nil, err
		}
		if data.Node == nil {
			return nil, nil, source.Refuse("unavailable", "project inaccessible")
		}
		if err = s.projectIdentity(data.Node.ID, data.Node.Number, data.Node.Owner.Login); err != nil {
			return nil, nil, err
		}
		for _, item := range data.Node.Items.Nodes {
			if item.Content == nil || item.Content.TypeName != "Issue" || !strings.EqualFold(item.Content.Repository.NameWithOwner, s.o.Repo) {
				continue
			}
			if item.ID == "" || item.Content.ID == "" || item.Content.Number < 1 || item.UpdatedAt.IsZero() || item.Value.OptionID != "" && item.Value.Field.ID != s.o.Project.FieldID {
				return nil, nil, source.Refuse("unavailable", "invalid project item binding")
			}
			if _, exists := items[item.Content.ID]; exists {
				return nil, nil, source.Refuse("unavailable", "ambiguous project issue")
			}
			items[item.Content.ID] = item
		}
		next, err := advance(data.Node.Items.PageInfo, seen)
		if err != nil {
			return nil, nil, err
		}
		if next == "" {
			if iteration == nil {
				return items, nil, nil
			}
			return items, iteration.sprints, nil
		}
		vars["after"] = next
	}
}

const projectStatusMutation = `mutation($projectID:ID!,$itemID:ID!,$fieldID:ID!,$optionID:String!){updateProjectV2ItemFieldValue(input:{projectId:$projectID,itemId:$itemID,fieldId:$fieldID,value:{singleSelectOptionId:$optionID}}){projectV2Item{id}}}`
