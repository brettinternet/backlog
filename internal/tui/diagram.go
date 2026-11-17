package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/AlexanderGrooff/mermaid-ascii/pkg/diagram"
	mermaid "github.com/AlexanderGrooff/mermaid-ascii/pkg/render"
	"github.com/brettinternet/backlog/internal/format"
	"github.com/brettinternet/backlog/internal/textutil"
	"github.com/charmbracelet/x/ansi"
)

// diagramMark tags a supported Mermaid fence's info string, so its caption
// can be found in rendered output. It's invisible and zero width; bodies lose
// their own copies.
const diagramMark = "⁤"

// diagramLimit caps a diagram's source; layout cost grows faster than its size.
const diagramLimit = 4 << 10

// diagramCaption is a rendered body line that captions a diagram detail can open.
type diagramCaption struct {
	line   int
	source string
}

// markDiagrams tags each closed, unindented mermaid fence whose type the
// renderer supports, and returns the sources in body order.
func markDiagrams(body string) (string, []string) {
	lines := strings.Split(strings.ReplaceAll(body, diagramMark, ""), "\n")
	var sources []string
	fence := format.Fence{}
	open, at := -1, -1 // the open fence's line, and where its mermaid info word ends
	for i, line := range lines {
		inside := fence.Open()
		if !fence.Consume(line) {
			continue
		}
		switch {
		case !inside:
			open, at = i, -1
			info := strings.TrimLeft(line, line[:1])
			if words := strings.Fields(info); line[0] != ' ' && len(words) > 0 && strings.EqualFold(words[0], "mermaid") {
				at = len(line) - len(info) + strings.Index(info, words[0]) + len(words[0])
			}
		case !fence.Open() && at >= 0:
			source := strings.Join(lines[open+1:i], "\n")
			if diagramSupported(source) {
				lines[open] = lines[open][:at] + diagramMark + lines[open][at:]
				sources = append(sources, textutil.Sanitize(source))
			}
			at = -1
		}
	}
	return strings.Join(lines, "\n"), sources
}

// diagramSupported reports the diagram types mermaid-ascii renders: its first
// statement after any frontmatter and comments.
func diagramSupported(source string) bool {
	source, _ = diagram.StripFrontmatter(source)
	for line := range strings.SplitSeq(source, "\n") {
		words := strings.Fields(line)
		if len(words) == 0 || strings.HasPrefix(words[0], "%%") {
			continue
		}
		switch words[0] {
		case "graph", "flowchart", "sequenceDiagram", "erDiagram":
			return true
		}
		return false
	}
	return false
}

// takeDiagrams finds the tagged captions in a rendered body, starting at page
// line offset, and labels each with how to open it.
func takeDiagrams(lines []string, offset int, sources []string, t theme) []diagramCaption {
	var captions []diagramCaption
	for i, line := range lines {
		if !strings.Contains(line, diagramMark) {
			continue
		}
		lines[i] = strings.ReplaceAll(line, diagramMark, "") + "  " + t.faint.render("g d open")
		if len(captions) < len(sources) {
			captions = append(captions, diagramCaption{line: offset + i, source: sources[len(captions)]})
		}
	}
	return captions
}

// renderDiagram lays a diagram out as text. It runs in a command, never on
// the update loop; a renderer panic becomes the error the view shows.
func renderDiagram(source string, width int, theme string) (lines []string, err error) {
	if len(source) > diagramLimit {
		return nil, fmt.Errorf("diagram is %d KiB, over the %d KiB limit", (len(source)+1023)>>10, diagramLimit>>10)
	}
	defer func() {
		if r := recover(); r != nil {
			lines, err = nil, fmt.Errorf("diagram renderer failed: %v", r)
		}
	}()
	config := diagram.DefaultConfig()
	config.MaxWidth = max(1, width)
	out, err := mermaid.RenderDiagram(source, config)
	if err != nil {
		return nil, err
	}
	// classDef colors are the renderer's only styling; NO_COLOR drops them.
	if theme == "no-color" {
		out = textutil.Sanitize(out)
	} else {
		out = textutil.SanitizeStyled(out)
	}
	return strings.Split(strings.TrimRight(out, "\n "), "\n"), nil
}

type diagramKey struct {
	source, theme string
	width         int
}

type diagramMsg struct {
	key   diagramKey
	lines []string
	err   string
}

// diagramView is the full-screen diagram opened from detail, scrolled x
// cells and y lines.
type diagramView struct {
	open    bool
	source  string
	key     diagramKey
	loading bool
	lines   []string
	wide    int // the widest line
	err     string
	x, y    int
}

const maxDiagrams = 16

func (m *Model) openDiagram(source string) tea.Cmd {
	m.diagram = diagramView{open: true, source: source}
	m.mouse.sel = textDrag{}
	return m.loadDiagram()
}

// loadDiagram shows the open diagram at the terminal's width and theme, from
// the cache or a command.
func (m *Model) loadDiagram() tea.Cmd {
	if !m.diagram.open {
		return nil
	}
	key := diagramKey{source: m.diagram.source, theme: m.theme.key(), width: max(1, m.width-2*detailPad)}
	if m.diagram.key == key {
		return nil // shown, or its command is running
	}
	m.diagram.key = key
	if result, ok := m.diagrams[key]; ok {
		m.showDiagram(result)
		return nil
	}
	m.diagram.loading = true
	name := m.theme.name
	return func() tea.Msg {
		lines, err := renderDiagram(key.source, key.width, name)
		result := diagramMsg{key: key, lines: lines}
		if err != nil {
			result.err = textutil.Line(err.Error())
		}
		return result
	}
}

func (m *Model) receiveDiagram(msg diagramMsg) {
	if m.diagrams == nil || len(m.diagrams) >= maxDiagrams {
		m.diagrams = make(map[diagramKey]diagramMsg)
	}
	m.diagrams[msg.key] = msg
	if m.diagram.open && m.diagram.key == msg.key {
		m.showDiagram(msg)
	}
}

func (m *Model) showDiagram(result diagramMsg) {
	m.diagram.loading = false
	m.diagram.lines, m.diagram.err, m.diagram.wide = result.lines, result.err, 0
	for _, line := range result.lines {
		m.diagram.wide = max(m.diagram.wide, ansi.StringWidth(line))
	}
	m.scrollDiagram(0, 0)
}

// scrollDiagram moves the view, keeping the diagram's edges in reach.
func (m *Model) scrollDiagram(dx, dy int) {
	m.diagram.x = max(0, min(m.diagram.x+dx, m.diagram.wide-max(1, m.width-2*detailPad)))
	m.diagram.y = max(0, min(m.diagram.y+dy, len(m.diagram.lines)-m.bodyRows()))
}

// diagramAt is the diagram whose caption is on body line, else -1.
func (m Model) diagramAt(line int) int {
	for i, d := range m.detailDiagrams {
		if d.line == line {
			return i
		}
	}
	return -1
}

// diagramInView is the first diagram at or below the top of detail's viewport.
func (m Model) diagramInView() int {
	for i, d := range m.detailDiagrams {
		if d.line >= m.detailScroll {
			return i
		}
	}
	return -1
}

func (m Model) updateDiagramKey(s string) (tea.Model, tea.Cmd) {
	page, half := max(1, m.bodyRows()), max(1, m.bodyRows()/2)
	step := 4
	switch s {
	case "esc", "q", "ctrl+g", "backspace":
		m.diagram = diagramView{}
	case "ctrl+c":
		return m, tea.Quit
	case "j", "down", "ctrl+n":
		m.scrollDiagram(0, 1)
	case "k", "up", "ctrl+p":
		m.scrollDiagram(0, -1)
	case "l", "right":
		m.scrollDiagram(step, 0)
	case "h", "left":
		m.scrollDiagram(-step, 0)
	case "ctrl+d", "pagedown":
		m.scrollDiagram(0, half)
	case "ctrl+u", "pageup":
		m.scrollDiagram(0, -half)
	case "ctrl+f", "ctrl+v", "space":
		m.scrollDiagram(0, page)
	case "ctrl+b", "alt+v":
		m.scrollDiagram(0, -page)
	case "g", "home":
		m.scrollDiagram(-m.diagram.x, -m.diagram.y)
	case "G", "end":
		m.scrollDiagram(0, len(m.diagram.lines))
	case "0":
		m.scrollDiagram(-m.diagram.x, 0)
	case "$":
		m.scrollDiagram(m.diagram.wide, 0)
	}
	return m, nil
}

func (m Model) diagramMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.MouseWheelMsg); !ok {
		return m, nil
	}
	switch msg.Mouse().Button {
	case tea.MouseWheelUp:
		m.scrollDiagram(0, -3)
	case tea.MouseWheelDown:
		m.scrollDiagram(0, 3)
	case tea.MouseWheelLeft:
		m.scrollDiagram(-3, 0)
	case tea.MouseWheelRight:
		m.scrollDiagram(3, 0)
	}
	return m, nil
}

// diagramPane replaces the body with the diagram, across the full width.
func (m Model) diagramPane(rows int) []string {
	t := m.theme
	out := make([]string, rows)
	pad := blank(detailPad)
	inner := max(1, m.width-2*detailPad)
	for i := range out {
		out[i] = blank(m.width)
	}
	switch {
	case m.diagram.loading:
		out[0] = fit(pad+t.muted.render("Loading…"), m.width)
	case m.diagram.err != "":
		out[0] = fit(pad+t.red.render("! "+m.diagram.err), m.width)
	default:
		for i := 0; i < rows && m.diagram.y+i < len(m.diagram.lines); i++ {
			out[i] = fit(pad+ansi.Cut(m.diagram.lines[m.diagram.y+i], m.diagram.x, m.diagram.x+inner), m.width)
		}
	}
	return out
}
