package tui

import (
	"strconv"
	"strings"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// RenderMarkdown renders a selected body off the UI loop. Callers cache the
// result by item version, width and theme, never by selection index alone.
func RenderMarkdown(body string, width int) (string, error) {
	return renderMarkdown(body, width, "no-color")
}

func renderMarkdown(body string, width int, theme string) (string, error) {
	r, err := glamour.NewTermRenderer(glamour.WithStyles(markdownStyle(theme)), glamour.WithWordWrap(max(1, width)))
	if err != nil {
		return "", err
	}
	out, err := r.Render(rewriteGitHub(textutil.Sanitize(body), theme))
	if err != nil {
		return "", err
	}
	// The renderer only styles sanitized input; keep its colors, nothing else.
	return joinAlertTitles(textutil.SanitizeStyled(out)), nil
}

// alertMark tags a rewritten alert title so joinAlertTitles can find it in
// rendered output. It's invisible and zero width; bodies lose their own copies.
const alertMark = "\u2063"

var alertTitles = map[string]string{"NOTE": "Note", "TIP": "Tip", "IMPORTANT": "Important", "WARNING": "Warning", "CAUTION": "Caution"}

// rewriteGitHub turns GitHub-only Markdown that Glamour can't express into
// Markdown it can: an alert's marker line becomes its bold type name, and a
// fence's info string becomes a caption line above the block. Lines inside
// fenced blocks are left as written.
func rewriteGitHub(body, theme string) string {
	lines := strings.Split(strings.ReplaceAll(body, alertMark, ""), "\n")
	out := make([]string, 0, len(lines))
	fence := format.Fence{}
	quoted := false
	for _, line := range lines {
		inside := fence.Open()
		if fence.Consume(line) {
			// Glamour runs a list item's paragraphs together, so only an
			// unindented fence, which can't belong to one, gets a caption.
			if !inside && line[0] != ' ' {
				out = appendCaption(out, line, theme)
			}
			out = append(out, line)
			quoted = false
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		quote := len(line)-len(trimmed) <= 3 && strings.HasPrefix(trimmed, ">")
		if quote && !quoted {
			marker := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			if name, ok := strings.CutPrefix(marker, "[!"); ok {
				if name, ok = strings.CutSuffix(name, "]"); ok {
					if title := alertTitles[strings.ToUpper(name)]; title != "" {
						line = "> **" + title + "**" + alertMark + "\n>"
					}
				}
			}
		}
		quoted = quote
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// appendCaption adds a fence's language, the first word of its info string,
// as its own paragraph above the opening line.
func appendCaption(out []string, line, theme string) []string {
	words := strings.Fields(strings.TrimLeft(line, line[:1]))
	if len(words) == 0 {
		return out
	}
	var b strings.Builder
	for _, r := range words[0] {
		if r < 0x80 && strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	caption := b.String()
	if theme != "no-color" && !strings.HasSuffix(theme, "-high-contrast") {
		caption = "\x1b[90m" + caption + "\x1b[39m"
	}
	return append(out, "", caption, "")
}

// joinAlertTitles removes the blank quote line Glamour puts between an alert's
// title paragraph and its text, so the text starts on the next line.
func joinAlertTitles(out string) string {
	if !strings.Contains(out, alertMark) {
		return out
	}
	lines := strings.Split(out, "\n")
	kept := lines[:0]
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.Contains(line, alertMark) {
			line = strings.ReplaceAll(line, alertMark, "")
			if i+1 < len(lines) {
				if rest := strings.TrimSpace(ansi.Strip(lines[i+1])); rest == "|" || rest == "│" {
					i++
				}
			}
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// markdownStyle matches the TUI palette: terminal ANSI colors for meaning,
// no document margins (the pane supplies padding), and no color at all under
// NO_COLOR, where ASCII markers keep emphasis visible.
func markdownStyle(theme string) gansi.StyleConfig {
	s := styles.ASCIIStyleConfig
	zero := uint(0)
	s.Document = gansi.StyleBlock{Margin: &zero}
	s.CodeBlock.Margin = &zero
	s.CodeBlock.Indent = new(uint)
	*s.CodeBlock.Indent = 2
	s.H1.Prefix, s.H2.Prefix, s.H3.Prefix = "", "", ""
	s.H4.Prefix, s.H5.Prefix, s.H6.Prefix = "", "", ""
	if theme == "no-color" {
		s.H1.Prefix, s.H2.Prefix, s.H3.Prefix = "# ", "## ", "### "
		return s
	}
	dark := theme == "dark" || theme == "dark-high-contrast"
	// Bright ANSI colors on dark backgrounds, normal ones on light (see theme).
	color := func(n int) *string {
		if dark {
			n += 8
		}
		v := strconv.Itoa(n)
		return &v
	}
	yes := true
	high := theme == "dark-high-contrast" || theme == "light-high-contrast"
	s.Heading.Bold = &yes
	s.Heading.Color = color(4)
	s.H1.Underline = &yes
	s.Strong = gansi.StylePrimitive{Bold: &yes}
	s.Emph = gansi.StylePrimitive{Italic: &yes}
	s.Strikethrough = gansi.StylePrimitive{CrossedOut: &yes}
	s.Code = gansi.StyleBlock{StylePrimitive: gansi.StylePrimitive{Color: color(6)}}
	s.CodeBlock.Color = color(6)
	s.Link = gansi.StylePrimitive{Color: color(4), Underline: &yes}
	s.LinkText = gansi.StylePrimitive{Bold: &yes}
	quote := "│ "
	s.BlockQuote.IndentToken = &quote
	s.Task.Ticked, s.Task.Unticked = "✓ ", "☐ "
	rule := "\n────────\n"
	s.HorizontalRule = gansi.StylePrimitive{Format: rule}
	sep, row, center := "│", "─", "┼"
	s.Table.ColumnSeparator, s.Table.RowSeparator, s.Table.CenterSeparator = &sep, &row, &center
	if !high {
		muted := "8"
		s.BlockQuote.Color = &muted
		s.HorizontalRule.Color = &muted
	}
	return s
}
