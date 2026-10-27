package github

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/brettinternet/backlog/internal/source"
	"github.com/brettinternet/backlog/internal/source/remote"
)

type projectItem struct {
	ID        string
	UpdatedAt time.Time
	// Project is read only through an issue's projectItems in a change window.
	Project *struct{ ID string }
	Content *struct {
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

const projectWindowQuery = `query($projectID:ID!,$filter:String!,$after:String){node(id:$projectID){... on ProjectV2{id number owner{... on User{login} ... on Organization{login}} items(first:100,after:$after,query:$filter,archivedStates:[ARCHIVED,NOT_ARCHIVED]){nodes{id updatedAt content{__typename ... on Issue{id number repository{nameWithOwner}}}} pageInfo{hasNextPage endCursor}}}}}`

// changedProjectIssues returns the numbers of this repository's issues whose
// project item changed at or after since. A field edit bumps only the item's
// updatedAt, not the issue's. The items filter accepts only dates, so it
// reads a day early and compares the timestamp here.
func (s *Source) changedProjectIssues(ctx context.Context, c remote.Credential, since time.Time) ([]int, error) {
	vars := map[string]any{"projectID": s.o.Project.ID, "filter": "updated:>=" + since.AddDate(0, 0, -1).Format(time.DateOnly)}
	seen := map[string]bool{}
	var numbers []int
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
		if err := s.query(ctx, c, projectWindowQuery, vars, &data); err != nil {
			return nil, err
		}
		if data.Node == nil {
			return nil, source.Refuse("unavailable", "project inaccessible")
		}
		if err := s.projectIdentity(data.Node.ID, data.Node.Number, data.Node.Owner.Login); err != nil {
			return nil, err
		}
		for _, item := range data.Node.Items.Nodes {
			if item.Content == nil || item.Content.TypeName != "Issue" || !strings.EqualFold(item.Content.Repository.NameWithOwner, s.o.Repo) {
				continue
			}
			if item.ID == "" || item.Content.Number < 1 || item.UpdatedAt.IsZero() {
				return nil, source.Refuse("unavailable", "invalid project item binding")
			}
			if !item.UpdatedAt.Before(since) {
				numbers = append(numbers, item.Content.Number)
			}
		}
		next, err := advance(data.Node.Items.PageInfo, seen)
		if err != nil {
			return nil, err
		}
		if next == "" {
			return numbers, nil
		}
		vars["after"] = next
	}
}

// boundItem picks the issue's item in the bound project from its projectItems.
func (s *Source) boundItem(row issue) (projectItem, error) {
	var found projectItem
	if s.o.Project == nil {
		return found, nil
	}
	if row.ProjectItems == nil || row.ProjectItems.PageInfo == nil || row.ProjectItems.PageInfo.HasNextPage {
		return found, source.Refuse("unavailable", "issue project items exceed complete read limit")
	}
	for _, item := range row.ProjectItems.Nodes {
		if item.Project == nil || item.Project.ID != s.o.Project.ID {
			continue
		}
		if found.ID != "" {
			return found, source.Refuse("unavailable", "ambiguous project issue")
		}
		if item.ID == "" || item.UpdatedAt.IsZero() || item.Value.OptionID != "" && item.Value.Field.ID != s.o.Project.FieldID {
			return found, source.Refuse("unavailable", "invalid project item binding")
		}
		found = item
	}
	return found, nil
}

const projectStatusMutation = `mutation($projectID:ID!,$itemID:ID!,$fieldID:ID!,$optionID:String!){updateProjectV2ItemFieldValue(input:{projectId:$projectID,itemId:$itemID,fieldId:$fieldID,value:{singleSelectOptionId:$optionID}}){projectV2Item{id}}}`

// projectIssuesQuery pages a project-only source's items with each issue's
// full fields. %s takes the optional items filter and the sprint value.
const projectIssuesQuery = `query($projectID:ID!,$fieldName:String!%s,$after:String){node(id:$projectID){... on ProjectV2{id number owner{... on User{login} ... on Organization{login}} items(first:100,after:$after%s){nodes{id type updatedAt content{__typename ... on Issue{` + issueFields + `}} fieldValueByName(name:$fieldName){... on ProjectV2ItemFieldSingleSelectValue{name optionId field{... on ProjectV2SingleSelectField{id}}}}%s} pageInfo{hasNextPage endCursor}}}}}`

// projectLoad reads a project-only source from its project items: all of them
// on reconciliation, else those whose item changed at or after since. Every
// issue edit but a comment bumps the item's updatedAt
// (docs/github-verification.md), so a window also reads issues updated since
// in each repository the last scan saw, as a repository source would. Items
// the credential can't read come back redacted and are skipped with one
// warning; pull requests and draft issues aren't issues and are skipped
// silently. It returns the repositories seen, for the next window.
func (s *Source) projectLoad(ctx context.Context, c remote.Credential, reconcile bool, since time.Time, sprints bool, repos []string) (source.Delta, []string, error) {
	statusName, iteration, err := s.discover(ctx, c, sprints)
	if err != nil {
		return source.Delta{}, nil, err
	}
	vars := map[string]any{"projectID": s.o.Project.ID, "fieldName": statusName}
	header, filter, sprint := "", "", ""
	delta := source.Delta{Coverage: source.Complete}
	if !reconcile {
		// The filter takes only dates, so read a day early and compare here.
		header, filter = ",$filter:String!", ",query:$filter"
		vars["filter"] = "updated:>=" + since.AddDate(0, 0, -1).Format(time.DateOnly)
		delta.Incremental, delta.RetainUnseen = true, true
	}
	values := `fieldValueByName(name:$fieldName){... on ProjectV2ItemFieldSingleSelectValue{name optionId field{... on ProjectV2SingleSelectField{id}}}}`
	sprintNames := map[string]string{}
	if iteration != nil {
		sprint = ` sprint:fieldValueByName(name:$sprintFieldName){... on ProjectV2ItemFieldIterationValue{iterationId}}`
		values += sprint
		vars["sprintFieldName"] = iteration.name
		delta.Sprints = iteration.sprints
		for _, s := range iteration.sprints {
			sprintNames[s.Key] = s.Name
		}
	}
	type entry struct {
		row issue
		p   projectItem
	}
	rows := map[string]entry{}
	var order []string
	add := func(row issue, p projectItem) {
		if _, ok := rows[row.ID]; !ok {
			order = append(order, row.ID)
		}
		rows[row.ID] = entry{row, p}
	}
	seenRepos := map[string]string{}
	if !reconcile {
		for _, repo := range repos {
			seenRepos[strings.ToLower(repo)] = repo
		}
	}
	q := fmt.Sprintf(projectIssuesQuery, header+sprintHeader(iteration), filter, sprint)
	seen, ids := map[string]bool{}, map[string]bool{}
	redacted := 0
	for {
		var data struct {
			Node *struct {
				ID     string
				Number int
				Owner  struct{ Login string }
				Items  struct {
					Nodes []struct {
						projectItem
						Type    string
						Content *struct {
							TypeName string `json:"__typename"`
							issue
						}
					}
					PageInfo *pageInfo
				}
			}
		}
		if err = s.query(ctx, c, q, vars, &data); err != nil {
			return source.Delta{}, nil, err
		}
		if data.Node == nil {
			return source.Delta{}, nil, source.Refuse("unavailable", "project inaccessible")
		}
		if err = s.projectIdentity(data.Node.ID, data.Node.Number, data.Node.Owner.Login); err != nil {
			return source.Delta{}, nil, err
		}
		for _, node := range data.Node.Items.Nodes {
			if node.Type == "REDACTED" || node.Type == "ISSUE" && node.Content == nil {
				redacted++
				continue
			}
			if node.Type != "ISSUE" || node.Content == nil || node.Content.TypeName != "Issue" {
				continue
			}
			p := node.projectItem
			if p.ID == "" || p.UpdatedAt.IsZero() || p.Value.OptionID != "" && p.Value.Field.ID != s.o.Project.FieldID {
				return source.Delta{}, nil, source.Refuse("unavailable", "invalid project item binding")
			}
			row := node.Content.issue
			if ids[row.ID] {
				return source.Delta{}, nil, source.Refuse("unavailable", "ambiguous project issue")
			}
			ids[row.ID] = true
			if repo := row.Repository.NameWithOwner; validRepo(repo) {
				seenRepos[strings.ToLower(repo)] = repo
			}
			if reconcile || !p.UpdatedAt.Before(since) {
				add(row, p)
			}
		}
		next, err := advance(data.Node.Items.PageInfo, seen)
		if err != nil {
			return source.Delta{}, nil, err
		}
		if next == "" {
			break
		}
		vars["after"] = next
	}
	if !reconcile {
		for _, key := range slices.Sorted(maps.Keys(seenRepos)) {
			changed, ok, err := s.repoWindow(ctx, c, seenRepos[key], since, statusName, iteration, values)
			if err != nil {
				return source.Delta{}, nil, err
			}
			if !ok {
				// Gone or unreadable: reconciliation reports it redacted.
				delete(seenRepos, key)
				continue
			}
			for _, row := range changed {
				p, err := s.boundItem(row)
				if err != nil {
					return source.Delta{}, nil, err
				}
				if p.ID != "" {
					add(row, p)
				}
			}
		}
	}
	for _, id := range order {
		item, err := s.item(ctx, c, rows[id].row, rows[id].p, sprintNames)
		if err != nil {
			return source.Delta{}, nil, err
		}
		delta.Items = append(delta.Items, item)
	}
	if redacted > 0 {
		delta.Findings = append(delta.Findings, source.Finding{Code: "github-project-redacted", Severity: "warning", Path: s.o.ID, Message: fmt.Sprintf("%d project items are in repositories this account can't read and were skipped", redacted)})
	}
	return delta, slices.Sorted(maps.Values(seenRepos)), nil
}

func sprintHeader(iteration *iterationField) string {
	if iteration == nil {
		return ""
	}
	return ",$sprintFieldName:String!"
}

// repoWindow reads one repository's issues updated since, with their project
// items. ok is false when the repository is gone or unreadable.
func (s *Source) repoWindow(ctx context.Context, c remote.Credential, repo string, since time.Time, statusName string, iteration *iterationField, values string) ([]issue, bool, error) {
	vars := repoVars(repo)
	vars["fieldName"], vars["since"] = statusName, since.Format(time.RFC3339)
	if iteration != nil {
		vars["sprintFieldName"] = iteration.name
	}
	q := `query($owner:String!,$repo:String!,$fieldName:String!` + sprintHeader(iteration) + `,$since:DateTime!,$after:String){repository(owner:$owner,name:$repo){id nameWithOwner issues(first:100,after:$after,states:[OPEN,CLOSED],filterBy:{since:$since},orderBy:{field:UPDATED_AT,direction:ASC}){nodes{` + issueFields + ` projectItems(first:100,includeArchived:false){nodes{id updatedAt project{id} ` + values + `} pageInfo{hasNextPage endCursor}}} pageInfo{hasNextPage endCursor}}}}`
	var rows []issue
	seen := map[string]bool{}
	for {
		var result struct {
			Repository *struct {
				ID, NameWithOwner string
				Issues            struct {
					Nodes    []issue
					PageInfo *pageInfo
				}
			}
		}
		if err := s.query(ctx, c, q, vars, &result, "repository"); err != nil {
			return nil, false, err
		}
		if result.Repository == nil {
			return nil, false, nil
		}
		if !strings.EqualFold(result.Repository.NameWithOwner, repo) {
			return nil, false, source.Refuse("unavailable", "repository identity changed")
		}
		rows = append(rows, result.Repository.Issues.Nodes...)
		after, err := advance(result.Repository.Issues.PageInfo, seen)
		if err != nil {
			return nil, false, err
		}
		if after == "" {
			return rows, true, nil
		}
		vars["after"] = after
	}
}

// projectRead reads one issue of a project-only source and its item in the
// project, and records the viewer's permission on its repository for a write
// preflight. An issue outside the project isn't in the source.
func (s *Source) projectRead(ctx context.Context, c remote.Credential, repo string, n int) (model.Item, error) {
	statusName, iteration, err := s.discover(ctx, c, s.readSprints)
	if err != nil {
		return model.Item{}, err
	}
	vars := repoVars(repo)
	vars["number"], vars["fieldName"] = n, statusName
	header := ",$fieldName:String!"
	values := `fieldValueByName(name:$fieldName){... on ProjectV2ItemFieldSingleSelectValue{name optionId field{... on ProjectV2SingleSelectField{id}}}}`
	sprintNames := map[string]string{}
	if iteration != nil {
		header += ",$sprintFieldName:String!"
		values += ` sprint:fieldValueByName(name:$sprintFieldName){... on ProjectV2ItemFieldIterationValue{iterationId}}`
		vars["sprintFieldName"] = iteration.name
		for _, s := range iteration.sprints {
			sprintNames[s.Key] = s.Name
		}
	}
	q := `query($owner:String!,$repo:String!,$number:Int!` + header + `){repository(owner:$owner,name:$repo){id nameWithOwner viewerPermission issue(number:$number){` + issueFields + ` projectItems(first:100,includeArchived:false){nodes{id updatedAt project{id} ` + values + `} pageInfo{hasNextPage endCursor}}}}}`
	var result struct {
		Repository *struct {
			ID, NameWithOwner, ViewerPermission string
			Issue                               *issue
		}
	}
	if err = s.query(ctx, c, q, vars, &result, "repository", "issue"); err != nil {
		return model.Item{}, err
	}
	r := result.Repository
	if r == nil {
		return model.Item{}, source.Refuse("not-found", "repository unavailable")
	}
	if r.ID == "" || !strings.EqualFold(r.NameWithOwner, repo) {
		return model.Item{}, source.Refuse("unavailable", "repository identity changed")
	}
	if r.Issue == nil {
		return model.Item{}, source.Refuse("not-found", "issue unavailable")
	}
	if r.Issue.Number != n || r.Issue.Repository.ID != r.ID {
		return model.Item{}, source.Refuse("unavailable", "issue identity changed")
	}
	p, err := s.boundItem(*r.Issue)
	if err != nil {
		return model.Item{}, err
	}
	if p.ID == "" {
		return model.Item{}, source.Refuse("not-found", "issue not in the configured project")
	}
	s.writable = writePermission(r.ViewerPermission)
	return s.item(ctx, c, *r.Issue, p, sprintNames)
}
