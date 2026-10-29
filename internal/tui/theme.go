package tui

import (
	"fmt"
	"image/color"
	"strings"

	"github.com/brettinternet/backlog/internal/model"
	"github.com/charmbracelet/x/ansi"
)

// paint wraps text in SGR sequences. Each style resets only the attributes it
// set, so a row background survives the styled cells inside it.
type paint struct{ on, off string }

func (p paint) render(s string) string {
	if p.on == "" || s == "" {
		return s
	}
	return p.on + s + p.off
}

// theme holds precomputed SGR sequences, keeping View free of style objects.
// Semantic colors use the terminal's own ANSI palette; neutral shades are mixed
// from the reported foreground and background so they suit any color scheme.
type theme struct {
	name                                      string
	color, high                               bool
	bold, muted, faint, accent, title         paint
	red, green, yellow, blue, magenta, cyan   paint
	tab, selectedTitle, highlight, reverse    paint
	selection, surface                        string // background on-sequences; reset with 49
	selectionOff                              string
	urgent, high2, medium, low, none, pending paint
	failed, mark, link, code                  paint
}

const resetBg = "\x1b[49m"

type rgb struct{ r, g, b uint8 }

func toRGB(c color.Color) (rgb, bool) {
	if c == nil {
		return rgb{}, false
	}
	r, g, b, _ := c.RGBA()
	return rgb{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}, true
}
func mix(a, b rgb, t float64) rgb {
	f := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return rgb{f(a.r, b.r), f(a.g, b.g), f(a.b, b.b)}
}
func (c rgb) fg() string { return fmt.Sprintf("38;2;%d;%d;%d", c.r, c.g, c.b) }
func (c rgb) bg() string { return fmt.Sprintf("48;2;%d;%d;%d", c.r, c.g, c.b) }
func (c rgb) dark() bool {
	return 0.2126*float64(c.r)+0.7152*float64(c.g)+0.0722*float64(c.b) < 128
}

func sgr(on, off string) paint { return paint{"\x1b[" + on + "m", "\x1b[" + off + "m"} }

// newTheme builds the palette. fg/bg are the terminal's reported colors, or nil
// before (or without) a reply, which assumes a dark background.
func newTheme(noColor, highContrast bool, fg, bg color.Color) theme {
	if noColor {
		return theme{name: "no-color"}
	}
	background, knownBg := toRGB(bg)
	foreground, knownFg := toRGB(fg)
	dark := !knownBg || background.dark()
	if !knownBg {
		background = rgb{0x1e, 0x1e, 0x2e}
	}
	if !knownFg {
		foreground = rgb{0xcd, 0xd6, 0xf4}
		if !dark {
			foreground = rgb{0x4c, 0x4f, 0x69}
		}
	}
	// Bright ANSI colors read better on dark backgrounds, normal ones on light.
	ansiColor := func(n int) string {
		if dark {
			return fmt.Sprint(90 + n)
		}
		return fmt.Sprint(30 + n)
	}
	t := theme{name: "dark", color: true, high: highContrast}
	if !dark {
		t.name = "light"
	}
	t.bold = sgr("1", "22")
	t.reverse = sgr("7", "27")
	t.red = sgr(ansiColor(1), "39")
	t.green = sgr(ansiColor(2), "39")
	t.yellow = sgr(ansiColor(3), "39")
	t.blue = sgr(ansiColor(4), "39")
	t.magenta = sgr(ansiColor(5), "39")
	t.cyan = sgr(ansiColor(6), "39")
	t.accent = t.blue
	t.title = sgr("1;"+ansiColor(4), "22;39")
	t.tab = sgr("1;7", "22;27")
	t.highlight = sgr("1;4", "22;24")
	t.selectedTitle = t.bold
	t.urgent = sgr("1;"+ansiColor(1), "22;39")
	t.high2 = t.yellow
	t.pending = t.cyan
	t.failed = sgr("1;"+ansiColor(1), "22;39")
	t.mark = t.magenta
	t.link = sgr("4;"+ansiColor(4), "24;39")
	if highContrast {
		// Never faint: secondary text keeps the terminal foreground.
		t.name += "-high-contrast"
		t.muted, t.faint, t.medium, t.low, t.none = paint{}, paint{}, paint{}, paint{}, paint{}
		t.code = t.bold
		t.selection = "\x1b[7m"
		t.selectionOff = "\x1b[27m"
		t.surface = ""
		return t
	}
	t.muted = sgr(mix(background, foreground, 0.55).fg(), "39")
	t.faint = sgr(mix(background, foreground, 0.28).fg(), "39")
	t.medium, t.low, t.none = t.muted, t.muted, t.faint
	t.code = t.cyan
	surface := mix(background, foreground, 0.12)
	t.selection = "\x1b[" + surface.bg() + "m"
	t.selectionOff = resetBg
	t.surface = "\x1b[" + mix(background, foreground, 0.06).bg() + "m"
	t.tab = sgr("1;"+ansiColor(4)+";"+surface.bg(), "22;39;49")
	return t
}

// key identifies the palette for caches of rendered, colored output.
func (t theme) key() string { return t.name + t.muted.on + t.faint.on + t.selection }

// selected paints a full-width line with the selection background.
func (t theme) selected(line string) string {
	if t.selection == "" {
		return line
	}
	// Inner resets that clear the background would end the highlight early.
	line = strings.ReplaceAll(line, resetBg, resetBg+t.selection)
	return t.selection + line + t.selectionOff
}

// dim repaints already rendered text behind a modal so focus is unambiguous.
func (t theme) dim(line string) string {
	if !t.color {
		return line
	}
	plain := ansi.Strip(line)
	if t.high {
		return plain
	}
	return t.faint.render(plain)
}

// Status categories carry a glyph, so color is never the only signal.
func categoryGlyph(category model.StatusCategory) string {
	switch category {
	case model.Backlog:
		return "◌"
	case model.Todo:
		return "○"
	case model.Active:
		return "◐"
	case model.Done:
		return "✓"
	case model.Canceled:
		return "✕"
	}
	return "·"
}
func (t theme) category(category model.StatusCategory) paint {
	switch category {
	case model.Active:
		return t.yellow
	case model.Done:
		return t.green
	case model.Backlog, model.Canceled:
		return t.muted
	case model.Todo:
		return paint{}
	}
	return t.faint
}
func (t theme) categoryIcon(category model.StatusCategory) string {
	return t.category(category).render(categoryGlyph(category))
}

func priorityGlyph(p model.Priority) string {
	switch p {
	case model.Urgent:
		return "▲▲"
	case model.High:
		return "▲"
	case model.Medium:
		return "■"
	case model.Low:
		return "▼"
	}
	return "·"
}

// claim colors a claim label: your own, a lapsed one, or another holder's.
func (t theme) claim(label string) paint {
	switch label {
	case "mine":
		return t.accent
	case "expired":
		return t.yellow
	}
	return t.muted
}
func (t theme) priority(p model.Priority) paint {
	switch p {
	case model.Urgent:
		return t.urgent
	case model.High:
		return t.high2
	case model.Medium:
		return t.medium
	case model.Low:
		return t.low
	}
	return t.none
}
