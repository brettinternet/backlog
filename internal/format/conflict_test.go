package format_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/brettinternet/backlog/internal/format"
)

func TestConflictRepairPreservesCommentsAndRejectsBaseChanges(t *testing.T) {
	t.Parallel()
	header := "---\nid: BL-1\ntitle: Comments\nstatus: todo\ncreated: 2026-10-22T00:00:00Z\nupdated: 2026-10-22T00:00:00Z\n---\n\n## Comments\n\n"
	base := "### 2026-10-22T01:00:00Z @a\nimmediate body\n"
	ours := "### 2026-10-22T02:00:00Z @b\nours body\n"
	theirs := "### 2026-10-22T03:00:00Z @c\ntheirs body\n"
	for _, style := range []string{"merge", "diff3"} {
		t.Run(style, func(t *testing.T) {
			middle := ""
			left, right := ours, theirs
			if style == "diff3" {
				middle = "||||||| base\n" + base
				left, right = base+ours, base+theirs
			}
			input := []byte(header + "<<<<<<< ours\n" + left + middle + "=======\n" + right + ">>>>>>> theirs\n")
			repaired, changed := format.ResolveConflicts(input)
			if !changed {
				t.Fatal("supported repair refused")
			}
			doc, err := format.Parse(repaired)
			if err != nil {
				t.Fatal(err)
			}
			comments := doc.Item().Comments
			want := 2
			if style == "diff3" {
				want = 3
			}
			if len(comments) != want {
				t.Fatalf("comments: %#v", comments)
			}
			for _, comment := range comments {
				if strings.Contains(comment.Author, "body") || !strings.Contains(comment.Body, "body") {
					t.Fatalf("comment body joined header: %#v", comment)
				}
			}
		})
	}
	for _, side := range []string{ours, strings.Replace(base, "immediate body", "edited body", 1) + ours} {
		input := []byte(header + "<<<<<<< ours\n" + side + "||||||| base\n" + base + "=======\n" + base + theirs + ">>>>>>> theirs\n")
		repaired, changed := format.ResolveConflicts(input)
		if changed || !bytes.Equal(input, repaired) {
			t.Fatal("comment deletion/edit silently repaired")
		}
		if _, err := format.Parse(repaired); err == nil || format.ErrorCode(err) != "merge-conflict" {
			t.Fatalf("unsupported conflict not retained: %v", err)
		}
	}
}
