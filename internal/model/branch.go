package model

import "strings"

// BranchName is the shared design.md §8 branch placeholder value used by TUI
// yanks and hook/action expansion.
func BranchName(item Item) string {
	id := asciiWords(strings.ToLower(item.Ref.ID))
	title := asciiWords(strings.ToLower(item.Title))
	if len(id) > 48 {
		id = truncateWords(id, 48)
	}
	if id == "" {
		return truncateWords(title, 48)
	}
	if title == "" || len(id) == 48 {
		return id
	}
	result := id
	for _, word := range strings.Split(title, "-") {
		if word == "" {
			continue
		}
		if len(result)+1+len(word) > 48 {
			break
		}
		result += "-" + word
	}
	return result
}

func asciiWords(value string) string {
	var result strings.Builder
	separator := false
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			if separator && result.Len() > 0 {
				result.WriteByte('-')
			}
			result.WriteByte(c)
			separator = false
		} else {
			separator = true
		}
	}
	return result.String()
}

func truncateWords(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	var result strings.Builder
	for _, word := range strings.Split(value, "-") {
		if word == "" {
			continue
		}
		if result.Len() > 0 {
			if result.Len()+1+len(word) > limit {
				break
			}
			result.WriteByte('-')
		} else if len(word) > limit {
			return word[:limit]
		}
		result.WriteString(word)
	}
	return result.String()
}
