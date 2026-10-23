package source

import (
	"slices"

	"github.com/brettinternet/backlog/internal/model"
)

// PrepareFields prepares common provider field edits without round-tripping a
// remote UUID, URL, raw fields or dependency graph through the native format.
// It never changes the input and leaves version enforcement to Apply.
func PrepareFields(before model.Item, m model.Mutation) (Prepared, error) {
	result := Prepared{Item: before, Inverse: model.Mutation{Ref: m.Ref, Op: m.Op, Field: m.Field}}
	if m.Op == model.Batch {
		for _, change := range m.Changes {
			prepared, err := PrepareFields(result.Item, change)
			if err != nil {
				return Prepared{}, err
			}
			result.Item = prepared.Item
			result.Inverse.Changes = append([]model.Mutation{prepared.Inverse}, result.Inverse.Changes...)
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
