// Package query implements the shared, pure query and quick-add languages.
package query

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Positions are zero-based UTF-8 byte offsets into the original input.
type Error struct {
	Pos                 int
	Message, Suggestion string
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s at %d", e.Message, e.Pos)
	if e.Suggestion != "" {
		s += fmt.Sprintf("; did you mean %q?", e.Suggestion)
	}
	return s
}
func fail(pos int, message string) error { return &Error{Pos: pos, Message: message} }

type Value struct {
	Text, Op string
	Pos      int
}
type Term struct {
	Field   string
	Negated bool
	Values  []Value
	Pos     int
}
type SortKey struct {
	Field string
	Desc  bool
}
type Query struct {
	Terms []Term
	Sort  []SortKey
	Group string
}
type Options struct{ Hook bool }

var fields = strings.Fields("id status category priority assignee label type milestone sprint source parent blocked-by blocks created updated due text claimed is has changed hook action outcome sort group")
var sortKeys = strings.Fields("next priority rank updated created due id title status")
var groupKeys = strings.Fields("status category priority assignee label milestone sprint type parent source none")
var isValues = strings.Fields("open closed ready blocked waiting unknown mine claimed archived pending")
var hasValues = strings.Fields("parent children blockers assignee due criteria claim")
var categories = strings.Fields("backlog todo active done canceled")
var priorities = strings.Fields("none low medium high urgent")
var outcomes = strings.Fields("ok failed timeout refused")

func hookField(s string) bool {
	return slices.Contains([]string{"changed", "hook", "action", "outcome"}, s)
}
func ordered(s string) bool { return s == "priority" || s == "created" || s == "updated" || s == "due" }

// Parse accepts syntax without applying caller-specific defaults or consulting a
// snapshot. Validate adds snapshot-dependent diagnostics (also usable as warnings).
func Parse(input string, options Options) (Query, error) {
	p := scanner{s: input}
	q := Query{}
	for p.space(); p.i < len(input); p.space() {
		t := Term{Pos: p.i}
		if input[p.i] == '-' {
			t.Negated = true
			p.i++
		}
		start := p.i
		// A field prefix must be unquoted and unescaped.
		for p.i < len(input) && !spaceAt(input, p.i) && !strings.ContainsRune(":\"\\,", rune(input[p.i])) {
			p.i++
		}
		if p.i < len(input) && input[p.i] == ':' {
			t.Field = strings.ToLower(input[start:p.i])
			p.i++
			if !slices.Contains(fields, t.Field) && !(strings.HasPrefix(t.Field, "field.") && len(t.Field) > 6) {
				return q, &Error{Pos: start, Message: fmt.Sprintf("unknown field %q", t.Field), Suggestion: closest(t.Field, fields)}
			}
			if hookField(t.Field) && !options.Hook {
				return q, fail(start, t.Field+": is only allowed in hook if:")
			}
		} else {
			p.i = start
		}
		for {
			v := Value{Pos: p.i}
			if t.Field != "" && p.i < len(input) && strings.ContainsRune("<>", rune(input[p.i])) {
				v.Op = string(input[p.i])
				p.i++
				if p.i < len(input) && input[p.i] == '=' {
					v.Op += "="
					p.i++
				}
				if !ordered(t.Field) {
					return q, fail(v.Pos, "operator is not allowed for "+t.Field)
				}
			}
			var err error
			v.Text, err = p.value(t.Field != "")
			if err != nil {
				return q, err
			}
			if v.Text == "" {
				return q, fail(v.Pos, "expected value")
			}
			if t.Field == "" && input[v.Pos:p.i] == v.Text && slices.Contains([]string{"|", "||", "&", "&&"}, v.Text) {
				return q, fail(v.Pos, fmt.Sprintf("%q is not an operator; terms are ANDed, a comma ORs values in one field (status:todo,done), and %q searches for it", v.Text, `"`+v.Text+`"`))
			}
			if err = validateValue(t.Field, v); err != nil {
				return q, err
			}
			t.Values = append(t.Values, v)
			if t.Field == "" || p.i == len(input) || input[p.i] != ',' {
				break
			}
			p.i++
		}
		if p.i < len(input) && !spaceAt(input, p.i) {
			return q, fail(p.i, "expected whitespace")
		}
		switch t.Field {
		case "sort":
			if t.Negated || len(q.Sort) > 0 {
				return q, fail(t.Pos, "sort must appear once without negation")
			}
			for _, v := range t.Values {
				q.Sort = append(q.Sort, SortKey{strings.TrimPrefix(strings.ToLower(v.Text), "-"), strings.HasPrefix(v.Text, "-")})
			}
		case "group":
			if t.Negated || q.Group != "" || len(t.Values) != 1 {
				return q, fail(t.Pos, "group requires one key and must appear once without negation")
			}
			q.Group = strings.ToLower(t.Values[0].Text)
		default:
			q.Terms = append(q.Terms, t)
		}
	}
	return q, nil
}
func validateValue(field string, v Value) error {
	var allowed []string
	switch field {
	case "is":
		allowed = isValues
	case "has":
		allowed = hasValues
	case "category":
		allowed = categories
	case "priority":
		allowed = priorities
	case "claimed":
		allowed = []string{"me", "others", "none"}
	case "outcome":
		allowed = outcomes
	case "sort":
		allowed = sortKeys
	case "group":
		allowed = groupKeys
	case "created", "updated", "due":
		if _, _, err := dateValue(v.Text, time.Time{}); err != nil {
			return fail(v.Pos, err.Error())
		}
	}
	value := strings.ToLower(v.Text)
	if field == "sort" {
		value = strings.TrimPrefix(value, "-")
	}
	if allowed != nil && !slices.Contains(allowed, value) {
		return &Error{Pos: v.Pos, Message: fmt.Sprintf("unknown %s value %q", field, v.Text), Suggestion: closest(value, allowed)}
	}
	return nil
}

type scanner struct {
	s string
	i int
}

func spaceAt(s string, i int) bool { r, _ := utf8.DecodeRuneInString(s[i:]); return unicode.IsSpace(r) }
func (p *scanner) space() {
	for p.i < len(p.s) && spaceAt(p.s, p.i) {
		_, n := utf8.DecodeRuneInString(p.s[p.i:])
		p.i += n
	}
}

// value removes quotes and escapes. Commas delimit only field values.
func (p *scanner) value(comma bool) (string, error) {
	var b strings.Builder
	quoted := false
	quotePos := 0
	for p.i < len(p.s) {
		c := p.s[p.i]
		if !quoted && (spaceAt(p.s, p.i) || comma && c == ',') {
			break
		}
		if c == '\\' {
			at := p.i
			p.i++
			if p.i == len(p.s) {
				return "", fail(at, "unfinished escape")
			}
			r, n := utf8.DecodeRuneInString(p.s[p.i:])
			b.WriteRune(r)
			p.i += n
			continue
		}
		if c == '"' {
			if !quoted {
				quotePos = p.i
			}
			quoted = !quoted
			p.i++
			continue
		}
		b.WriteByte(c)
		p.i++
	}
	if quoted {
		return "", fail(quotePos, "unterminated quote")
	}
	return b.String(), nil
}

func closest(s string, candidates []string) string {
	// Bounded edit distance: diagnostics must stay cheap even on fuzzed input.
	if len(s) > 64 {
		return ""
	}
	best := ""
	distance := 3
	for _, v := range candidates {
		row := make([]int, len(v)+1)
		for j := range row {
			row[j] = j
		}
		for i := range len(s) {
			prev := row[0]
			row[0] = i + 1
			for j := range len(v) {
				old := row[j+1]
				cost := 0
				if s[i] != v[j] {
					cost = 1
				}
				row[j+1] = min(row[j+1]+1, row[j]+1, prev+cost)
				prev = old
			}
		}
		if row[len(v)] < distance {
			distance = row[len(v)]
			best = v
		}
	}
	return best
}

// Relative ages compare age, not timestamps: updated:<7d means newer than 7d.
// Absolute dates compare calendar dates in the injected clock's location.
func dateValue(s string, now time.Time) (time.Time, bool, error) {
	s = strings.ToLower(s)
	if s == "today" {
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), false, nil
	}
	if len(s) > 1 {
		unit := s[len(s)-1]
		scale := time.Duration(0)
		switch unit {
		case 'h':
			scale = time.Hour
		case 'd':
			scale = 24 * time.Hour
		case 'w':
			scale = 7 * 24 * time.Hour
		}
		if scale != 0 {
			n, err := strconv.ParseUint(s[:len(s)-1], 10, 64)
			if err == nil && n <= uint64((1<<63-1)/scale) {
				return now.Add(-time.Duration(n) * scale), true, nil
			}
			return time.Time{}, false, fmt.Errorf("invalid relative age %q", s)
		}
	}
	d, err := time.ParseInLocation(time.DateOnly, s, now.Location())
	if err != nil {
		return d, false, fmt.Errorf("invalid date %q", s)
	}
	return d, false, nil
}
