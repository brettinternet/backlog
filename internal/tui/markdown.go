package tui

import (
	"charm.land/glamour/v2"
	"github.com/brettinternet/backlog/internal/textutil"
)

// RenderMarkdown renders a selected body off the UI loop. Callers cache the
// result by item version, width and theme, never by selection index alone.
func RenderMarkdown(body string, width int) (string, error) {
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle("notty"), glamour.WithWordWrap(max(1, width)))
	if err != nil {
		return "", err
	}
	return r.Render(textutil.Sanitize(body))
}
