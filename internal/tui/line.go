package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/brettinternet/backlog/internal/textutil"
)

// lineInput is a single-line text field with the readline keys a terminal
// prompt has. ctrl+k moves up in lists (as in fzf), so it doesn't kill.
type lineInput struct {
	text   string
	cursor int // byte offset into text
}

func newLine(text string) lineInput { return lineInput{text: text, cursor: len(text)} }

// edit applies an editing key or inserts typed text. handled reports whether
// the key belonged to the field; changed whether the text did.
func (l *lineInput) edit(key, text string) (handled, changed bool) {
	before := l.text
	switch key {
	case "left", "ctrl+b":
		l.cursor = l.prevRune()
	case "right", "ctrl+f":
		l.cursor = l.nextRune()
	case "home", "ctrl+a":
		l.cursor = 0
	case "end", "ctrl+e":
		l.cursor = len(l.text)
	case "alt+b", "alt+left", "ctrl+left":
		l.cursor = l.prevWord(isWordRune)
	case "alt+f", "alt+right", "ctrl+right":
		l.cursor = l.nextWord()
	case "backspace", "ctrl+h":
		l.cut(l.prevRune(), l.cursor)
	case "delete", "ctrl+d":
		l.cut(l.cursor, l.nextRune())
	case "ctrl+w":
		l.cut(l.prevWord(func(r rune) bool { return !unicode.IsSpace(r) }), l.cursor)
	case "alt+backspace", "ctrl+backspace":
		l.cut(l.prevWord(isWordRune), l.cursor)
	case "alt+d", "alt+delete":
		l.cut(l.cursor, l.nextWord())
	case "ctrl+u":
		l.cut(0, l.cursor)
	default:
		if key == "space" {
			text = " "
		}
		if text == "" && utf8.RuneCountInString(key) == 1 {
			text = key
		}
		if text == "" || strings.ContainsAny(text, "\x00\x1b\r\n") {
			return false, false
		}
		l.text = l.text[:l.cursor] + text + l.text[l.cursor:]
		l.cursor += len(text)
	}
	return true, l.text != before
}

// view renders the text with mark at the cursor.
func (l lineInput) view(mark string) string {
	return textutil.Line(l.text[:l.cursor]) + mark + textutil.Line(l.text[l.cursor:])
}

func (l *lineInput) cut(from, to int) {
	l.text = l.text[:from] + l.text[to:]
	l.cursor = from
}
func (l lineInput) prevRune() int {
	_, size := utf8.DecodeLastRuneInString(l.text[:l.cursor])
	return l.cursor - size
}
func (l lineInput) nextRune() int {
	_, size := utf8.DecodeRuneInString(l.text[l.cursor:])
	return l.cursor + size
}

// prevWord is the start of the word before the cursor, skipping separators.
func (l lineInput) prevWord(word func(rune) bool) int {
	i := l.cursor
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(l.text[:i])
		if word(r) {
			break
		}
		i -= size
	}
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(l.text[:i])
		if !word(r) {
			break
		}
		i -= size
	}
	return i
}

// nextWord is the end of the word after the cursor, skipping separators.
func (l lineInput) nextWord() int {
	i := l.cursor
	for i < len(l.text) {
		r, size := utf8.DecodeRuneInString(l.text[i:])
		if isWordRune(r) {
			break
		}
		i += size
	}
	for i < len(l.text) {
		r, size := utf8.DecodeRuneInString(l.text[i:])
		if !isWordRune(r) {
			break
		}
		i += size
	}
	return i
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
