package query

import (
	"slices"
	"strings"
	"unicode"

	"github.com/brettinternet/backlog/internal/model"
)

// QuickAdd parses an item template; allocation, defaults and source validation
// belong to the caller. A +ID token sets item.Ref.Source. me returns the
// @handle for @me in the given +ID source ("" when none was given); an empty
// handle refuses @me. List fields accumulate without duplicates; the last
// scalar token wins.
func QuickAdd(input string, me func(source string) (string, error)) (model.Item, error) {
	p := scanner{s: input}
	item := model.Item{}
	var title, assignees []string
	meAt := -1
	for p.space(); p.i < len(input); p.space() {
		at := p.i
		first := input[at]
		text, err := p.value(false)
		if err != nil {
			return item, err
		}
		if first == '"' || first == '\\' || !strings.ContainsRune("!@#^<~:+", rune(first)) {
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
				text = ""
				if meAt < 0 {
					meAt = at
				}
			}
			assignees = append(assignees, text)
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
		case '+':
			item.Ref.Source = value
		}
	}
	// @me resolves after the loop, since a later +ID can change its source.
	for _, a := range assignees {
		if a == "" {
			handle, err := me(item.Ref.Source)
			if err != nil {
				return item, err
			}
			if handle == "" {
				return item, fail(meAt, "@me requires a configured identity")
			}
			a = handle
		}
		if !slices.Contains(item.Assignees, a) {
			item.Assignees = append(item.Assignees, a)
		}
	}
	item.Title = strings.Join(title, " ")
	if strings.TrimSpace(item.Title) == "" {
		return item, fail(0, "expected title")
	}
	return item, nil
}

// QuoteTitle escapes text so QuickAdd reads it back as the title alone.
func QuoteTitle(text string) string {
	var b strings.Builder
	start := true
	for _, r := range text {
		if r == '\\' || r == '"' || start && strings.ContainsRune("!@#^<~:+", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
		start = unicode.IsSpace(r)
	}
	return b.String()
}
