package source

import (
	"slices"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
)

// PrepareFields prepares common provider field edits and comment appends without
// round-tripping a remote UUID, URL, raw fields or dependency graph through the
// native format. Comments have no inverse; archive state comes from the
// provider's "archived" field. It never changes the input and leaves
// version enforcement to Apply.
func PrepareFields(before model.Item, m model.Mutation) (Prepared, error) {
	result := Prepared{Item: before, Inverse: model.Mutation{Ref: m.Ref, Op: m.Op, Field: m.Field}}
	if m.Op == model.Batch {
		undoable := true
		for _, change := range m.Changes {
			prepared, err := PrepareFields(result.Item, change)
			if err != nil {
				return Prepared{}, err
			}
			result.Item = prepared.Item
			undoable = undoable && prepared.Inverse.Op != ""
			result.Inverse.Changes = append([]model.Mutation{prepared.Inverse}, result.Inverse.Changes...)
		}
		if !undoable {
			result.Inverse = model.Mutation{}
		}
		return result, nil
	}
	if m.Op == model.AppendComment {
		comment := m.Comment
		if comment.Body == "" {
			comment.Body = m.Text
		}
		if strings.TrimSpace(comment.Body) == "" {
			return Prepared{}, Refuse("rejected", "comment body required")
		}
		result.Item.Comments = append(slices.Clone(before.Comments), comment)
		result.Inverse = model.Mutation{}
		return result, nil
	}
	if m.Op == model.Archive || m.Op == model.Unarchive {
		// Like native files, the inverse restores the observed archive state.
		result.Inverse.Op = model.Unarchive
		if before.Fields["archived"] == true {
			result.Inverse.Op = model.Archive
		}
		return result, nil
	}
	if m.Op == model.ReplaceSection {
		if m.Section != "Description" {
			return Prepared{}, Refuse("unavailable", "provider preparation required for this section")
		}
		result.Inverse.Section, result.Inverse.Text = m.Section, before.Description
		result.Item.Description = m.Text
		return result, nil
	}
	if (m.Op == model.Add || m.Op == model.Remove) && (m.Field == "assignees" || m.Field == "labels") {
		value, ok := m.Value.(string)
		if !ok {
			return Prepared{}, Refuse("rejected", "list entry requires a string")
		}
		list := &result.Item.Labels
		if m.Field == "assignees" {
			list = &result.Item.Assignees
		}
		// The inverse undoes only an actual change: a no-op stays a no-op.
		// Providers match names and logins case-insensitively.
		same := func(v string) bool { return strings.EqualFold(v, value) }
		present := slices.ContainsFunc(*list, same)
		result.Inverse.Value = value
		if m.Op == model.Add && !present {
			result.Inverse.Op = model.Remove
			*list = append(slices.Clone(*list), value)
		} else if m.Op == model.Remove && present {
			result.Inverse.Op = model.Add
			*list = slices.DeleteFunc(slices.Clone(*list), same)
		}
		return result, nil
	}
	if m.Op != model.Set {
		return Prepared{}, Refuse("unavailable", "provider preparation required for this operation")
	}
	if m.Field == "assignees" || m.Field == "labels" {
		values, ok := m.Value.([]string)
		if !ok {
			return Prepared{}, Refuse("rejected", "list field requires strings")
		}
		if m.Field == "assignees" {
			result.Inverse.Value = slices.Clone(before.Assignees)
			result.Item.Assignees = slices.Clone(values)
		} else {
			result.Inverse.Value = slices.Clone(before.Labels)
			result.Item.Labels = slices.Clone(values)
		}
		return result, nil
	}
	value, ok := m.Value.(string)
	if !ok {
		return Prepared{}, Refuse("rejected", "field requires a string")
	}
	var field *string
	switch m.Field {
	case "title":
		field = &result.Item.Title
	case "status":
		field = &result.Item.Status
	case "type":
		field = &result.Item.Type
	case "priority":
		result.Inverse.Value = string(before.Priority)
		result.Item.Priority = model.Priority(value)
		return result, nil
	case "sprint":
		result.Inverse.Value, result.Inverse.ExpectedSprint = before.Sprint, new(value)
		result.Item.Sprint = value
		return result, nil
	case "milestone":
		field = &result.Item.Milestone
	case "rank":
		field = &result.Item.Rank
	case "due":
		field = &result.Item.Due
	default:
		return Prepared{}, Refuse("unavailable", "provider preparation required for this field")
	}
	result.Inverse.Value = *field
	*field = value
	return result, nil
}
