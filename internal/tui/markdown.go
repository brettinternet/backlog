package tui

import (
	"strconv"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"github.com/brettinternet/backlog/internal/textutil"
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
	out, err := r.Render(textutil.Sanitize(body))
	if err != nil {
		return "", err
	}
	// The renderer only styles sanitized input; keep its colors, nothing else.
	return textutil.SanitizeStyled(out), nil
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
