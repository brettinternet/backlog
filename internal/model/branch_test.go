package model

import (
	"strings"
	"testing"
)

func TestBranchName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, id, title, want string
	}{
		{"slug punctuation", "BL-12", "Debounce detail rendering", "bl-12-debounce-detail-rendering"},
		{"non ascii title", "BL-13", "修正 the cache 🚀", "bl-13-the-cache"},
		{"non ascii only", "BL-14", "修正 🚀", "bl-14"},
		{"word boundary limit", "BL-1", "one two three four five six seven eight ten eleven", "bl-1-one-two-three-four-five-six-seven-eight-ten"},
		{"controls removed", "BL-15", "safe\x1b]52;c;bad\a title", "bl-15-safe-52-c-bad-title"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := BranchName(Item{Ref: Ref{ID: test.id}, Title: test.title})
			if got != test.want {
				t.Fatalf("BranchName() = %q, want %q", got, test.want)
			}
			if len(got) > 48 {
				t.Fatalf("branch name exceeds 48 ASCII characters: %q", got)
			}
			for _, char := range got {
				if char > 127 || !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
					t.Fatalf("branch name contains a non-slug character: %q", got)
				}
			}
		})
	}
	if got := BranchName(Item{Ref: Ref{ID: "BL-1"}, Title: strings.Repeat("longword", 10)}); got != "bl-1" {
		t.Fatalf("long word was cut mid-word: %q", got)
	}
}
