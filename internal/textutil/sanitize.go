// Package textutil makes untrusted provider text safe for terminal display.
package textutil

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// Sanitize removes terminal escape sequences and controls, retaining body lines.
func Sanitize(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, ansi.Strip(value))
}

// Line also removes layout controls for table cells.
func Line(value string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(Sanitize(value))
}
