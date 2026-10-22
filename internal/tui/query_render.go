package tui

// Styling is applied after cell sanitizes and clips provider text. Never pass
// styled text back through cell, which deliberately strips terminal escapes.
func (m Model) highlightListCell(index int, line string, width int) string {
	line = cell(line, width)
	if m.showHelp || m.loading || m.failure != "" {
		return line
	}
	if _, ok := m.queryOverlay(index - m.top); ok {
		return line
	}
	if index >= len(m.data.rows) || m.data.rows[index].header != "" {
		return line
	}
	return m.highlightLine(line)
}
