package query

import (
	"slices"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
)

// QuickAdd parses an item template; allocation, defaults and source validation
// belong to the caller. me is the resolved @handle for @me (empty refuses @me).
// List fields accumulate without duplicates; the last scalar token wins.
func QuickAdd(input, me string) (model.Item, error) {
	p := scanner{s: input}
	item := model.Item{}
	var title []string
	for p.space(); p.i < len(input); p.space() {
		at := p.i
		first := input[at]
		text, err := p.value(false)
		if err != nil {
			return item, err
		}
		if first == '"' || first == '\\' || !strings.ContainsRune("!@#^<~:", rune(first)) {
			title = append(title, text)
			continue
		}
		value := text[1:]
		if value == "" {
			return item, fail(at, "expected quick-add token value")
		}
		ref := model.Ref{ID: value}
		if source, id, ok := strings.Cut(value, ":"); ok && (first == '^' || first == '<') {
			ref = model.Ref{Source: source, ID: id}
			if source == "" || id == "" {
				return item, fail(at, "invalid reference")
			}
		}
		switch first {
		case '!':
			var matches []string
			for _, v := range []string{"urgent", "high", "medium", "low"} {
				if strings.HasPrefix(v, strings.ToLower(value)) {
					matches = append(matches, v)
				}
			}
			if len(matches) != 1 {
				return item, fail(at, "unknown or ambiguous priority "+value)
			}
			item.Priority = model.Priority(matches[0])
		case '@':
			if strings.EqualFold(value, "me") {
				if me == "" {
					return item, fail(at, "@me requires a configured identity")
				}
				text = me
			}
			if !slices.Contains(item.Assignees, text) {
				item.Assignees = append(item.Assignees, text)
			}
		case '#':
			if !slices.Contains(item.Labels, value) {
				item.Labels = append(item.Labels, value)
			}
		case '^':
			item.Parent = &ref
		case '<':
			if !slices.Contains(item.BlockedBy, ref) {
				item.BlockedBy = append(item.BlockedBy, ref)
			}
		case '~':
			item.Milestone = value
		case ':':
			item.Type = value
		}
	}
	item.Title = strings.Join(title, " ")
	if strings.TrimSpace(item.Title) == "" {
		return item, fail(0, "expected title")
	}
	return item, nil
}
