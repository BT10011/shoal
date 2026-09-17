package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/BT10011/shoal/internal/engine"
)

// renderHood draws the progress of every probe, then tails the event log
// into whatever room is left.
func (a *app) renderHood(width, rows int) []string {
	s := a.renderer.Styles
	var lines []string

	for _, d := range a.status.Discoverers {
		state := string(d.State)
		if d.Err != "" {
			state = "failed: " + d.Err
		}
		// A probe that listens has nothing to count towards, so show what it
		// has heard rather than a denominator that will never arrive.
		tail := fmt.Sprintf(" %d/%d %s", d.Done, d.Total, state)
		if d.Total == 0 {
			tail = fmt.Sprintf(" %d %s", d.Done, state)
		}
		barW := width - 7 - lipgloss.Width(tail)
		if barW < 4 {
			lines = append(lines, s.Item.Render(fit(fmt.Sprintf("%-6s%s", d.Name, tail), width)))
			continue
		}
		lines = append(lines, s.Item.Render(fit(d.Name, 7))+progressBar(d.Done, d.Total, barW, a.frame(), a.barStyles())+s.Item.Render(fit(tail, width-7-barW)))
	}
	for _, e := range a.status.Enrichers {
		line := fmt.Sprintf("%-6s %d running · %d queued · %d done", e.Name, e.Running, e.Queued, e.Completed)
		if e.Failed > 0 {
			line += fmt.Sprintf(" · %d failed", e.Failed)
		}
		lines = append(lines, s.ItemMuted.Render(fit(line, width)))
	}
	if len(lines) == 0 {
		lines = append(lines, s.ItemMuted.Render(fit("no probes running", width)))
	}
	rule := "─"
	if s.PlainUI {
		rule = "-"
	}
	lines = append(lines, s.ItemMuted.Render(strings.Repeat(rule, max(0, width))))

	room := rows - len(lines)
	if room <= 0 {
		return lines
	}
	start := max(0, len(a.events)-room)
	for _, ev := range a.events[start:] {
		lines = append(lines, a.renderEvent(ev, width))
	}
	return lines
}

func (a *app) renderEvent(ev engine.ProbeEvent, width int) string {
	s := a.renderer.Styles
	tag, style := "·", s.ItemMuted
	switch ev.Kind {
	case engine.KindSent:
		tag, style = "→", s.Item
	case engine.KindReceived:
		tag, style = "←", s.Badge.Background(s.Theme.Bg)
	case engine.KindError:
		tag, style = "✗", a.alert()
	case engine.KindProgress:
		tag = "…"
	}
	if s.PlainUI {
		switch tag {
		case "→":
			tag = ">"
		case "←":
			tag = "<"
		case "✗":
			tag = "!"
		default:
			tag = "-"
		}
	}
	msg := ev.Message
	if msg == "" && ev.Kind == engine.KindProgress {
		msg = fmt.Sprintf("%d/%d", ev.Done, ev.Total)
	}
	return style.Render(fit(fmt.Sprintf("%s %-5s %s %s", ev.At.Format("15:04:05"), ev.Probe, tag, msg), width))
}

// Dotted, retro-looking progress bar. While a probe runs, filled cells
// pick a dense braille glyph by hashing position and tick so the texture
// shimmers, a few cells flare bright and fade, and a head glyph cycles at
// the leading edge. Once complete every cell is the full 8-dot glyph so
// the finished bar is a solid grid with no holes.
var (
	dotFilled   = []string{"⣿", "⣷", "⣯", "⣟", "⡿", "⢿", "⣻", "⣽"}
	dotHead     = []string{"⠁", "⠃", "⠇", "⡇", "⣇", "⣧"}
	asciiFilled = []string{":", ":", ":", ";"}
	asciiHead   = []string{"-", "\\", "|", "/"}
)

// barStyles are the three brightness levels of a running bar: dim at rest,
// full foreground while a flare fades, accent and bold at its peak. A
// finished bar is drawn at full foreground.
type barStyles struct {
	plain                   bool
	dim, mid, bright, solid lipgloss.Style
}

func (a *app) barStyles() barStyles {
	s := a.renderer.Styles
	return barStyles{
		plain:  s.PlainUI,
		dim:    s.ItemMuted,
		mid:    s.Item,
		bright: s.Item.Foreground(s.Theme.Unread).Bold(true),
		solid:  s.Item,
	}
}

// frameEvery is the animation clock for the bar, independent of how fast
// probes tick, so flares are visible whatever the sweep rate.
const frameEvery = 100 * time.Millisecond

func (a *app) frame() int {
	return int(a.now.UnixNano() / int64(frameEvery))
}

func progressBar(done, total, width, frame int, st barStyles) string {
	if width < 1 {
		return ""
	}
	filled, head := dotFilled, dotHead
	if st.plain {
		filled, head = asciiFilled, asciiHead
	}
	cells := 0
	if total > 0 {
		cells = min(width, done*width/total)
	}
	complete := total > 0 && done >= total

	var b strings.Builder
	for i := 0; i < cells; i++ {
		glyph, style := filled[0], st.solid
		if !complete {
			glyph = filled[cellHash(i+frame*width)%len(filled)]
			switch sparkAge(i, frame) {
			case 0:
				style = st.bright
			case 1:
				style = st.mid
			default:
				style = st.dim
			}
		}
		b.WriteString(style.Render(glyph))
	}
	rest := width - cells
	if rest > 0 && done > 0 && !complete {
		b.WriteString(st.mid.Render(head[frame%len(head)]))
		rest--
	}
	if rest > 0 {
		b.WriteString(st.dim.Render(strings.Repeat(" ", rest)))
	}
	return b.String()
}

// sparkWindow is how many frames a flare cycle lasts: bright for the
// first third, fading for the second, dim for the rest.
const sparkWindow = 6

// sparkAge returns 0 while a cell is freshly lit, 1 while it fades and -1
// when it is dim. Roughly one cell in two flares per window, each at a
// different phase so the bar twinkles rather than pulsing in unison.
func sparkAge(cell, frame int) int {
	frame += cellHash(cell*104729) % sparkWindow
	window := frame / sparkWindow
	if cellHash(cell*7919+window)%2 != 0 {
		return -1
	}
	switch age := frame % sparkWindow; {
	case age < 2:
		return 0
	case age < 4:
		return 1
	}
	return -1
}

func cellHash(i int) int {
	x := uint32(i)*2654435761 + 0x9e3779b9
	x ^= x >> 15
	x *= 0x85ebca6b
	x ^= x >> 13
	return int(x & 0x7fffffff)
}
