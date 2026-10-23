// Package textutil makes untrusted provider text safe for terminal display.
package textutil

import (
	"strings"
	"unicode"
	"unicode/utf8"

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

// SanitizeStyled keeps only SGR color and attribute sequences, as produced by
// a trusted renderer over already sanitized text. Every other escape sequence
// (cursor movement, OSC clipboard or hyperlinks, DCS) and control is removed.
func SanitizeStyled(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); {
		c := value[i]
		if c != 0x1b {
			r, size := utf8.DecodeRuneInString(value[i:])
			if r == utf8.RuneError && size == 1 || unicode.IsControl(r) && r != '\n' && r != '\t' {
				i += size
				continue
			}
			b.WriteString(value[i : i+size])
			i += size
			continue
		}
		if i+1 >= len(value) {
			break
		}
		switch value[i+1] {
		case '[':
			j := i + 2
			params := true
			for j < len(value) && value[j] >= 0x30 && value[j] <= 0x3f {
				params = params && (value[j] >= '0' && value[j] <= '9' || value[j] == ';' || value[j] == ':')
				j++
			}
			intermediate := j
			for j < len(value) && value[j] >= 0x20 && value[j] <= 0x2f {
				j++
			}
			if j < len(value) && value[j] >= 0x40 && value[j] <= 0x7e {
				if value[j] == 'm' && params && intermediate == j {
					b.WriteString(value[i : j+1])
				}
				j++
			}
			i = j
		case ']', 'P', '_', '^', 'X':
			// String sequences end with BEL (OSC only) or ST.
			j := i + 2
			for j < len(value) {
				if value[j] == 0x07 && value[i+1] == ']' {
					j++
					break
				}
				if value[j] == 0x1b && j+1 < len(value) && value[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j
		default:
			j := i + 1
			for j < len(value) && value[j] >= 0x20 && value[j] <= 0x2f {
				j++
			}
			if j < len(value) && value[j] >= 0x30 && value[j] <= 0x7e {
				j++
			}
			i = j
		}
	}
	return b.String()
}

// Line also removes layout controls for table cells.
func Line(value string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(Sanitize(value))
}
