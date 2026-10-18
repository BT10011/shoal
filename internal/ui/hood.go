package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

// renderHood draws the progress of every probe, then tails the event log
// into whatever room is left.
func (a *app) renderHood(width, rows int) []string {
	s := a.renderer.Styles
	var lines []string

	for i, d := range a.status.Discoverers {
		state := string(d.State)
		switch {
		case d.Err != "":
			state = "failed: " + d.Err
		case d.State == engine.StateCancelled:
			state = "stopped"
		}
		// A probe that listens has no end to count towards, so its row
		// says what it has heard and sends each message in as a comet
		// rather than filling a bar.
		listener := isListener(d)
		// A listener straight after a sweep of the same name is that probe's
		// second stage (the ARP listener takes over when the sweep ends), so
		// it is drawn as a continuation of the sweep's row, not a second
		// probe with the same name.
		var sweep *engine.DiscovererStatus
		if listener && i > 0 && a.status.Discoverers[i-1].Name == d.Name && a.status.Discoverers[i-1].Total > 0 {
			sweep = &a.status.Discoverers[i-1]
		}
		waiting := sweep != nil && sweep.State == engine.StateRunning && d.State == engine.StateRunning
		name := d.Name
		if sweep != nil {
			name = "  └"
			if s.PlainUI {
				name = "  \\"
			}
		}
		tail := fmt.Sprintf(" %d/%d %s", d.Done, d.Total, state)
		switch {
		case waiting:
			tail = " listens once the sweep ends"
		case sweep != nil && d.State == engine.StateRunning:
			tail = fmt.Sprintf(" still listening · %d heard", d.Done)
		case listener && d.State == engine.StateRunning:
			tail = fmt.Sprintf(" listening · %d heard", d.Done)
		case listener:
			tail = fmt.Sprintf(" %s · %d heard", state, d.Done)
		case d.Total == 0 && d.Message != "":
			// A probe with nothing to count says what it is doing instead,
			// the way the history probe reports the visit it is on.
			tail = " " + d.Message
			if d.State != engine.StateRunning {
				tail = fmt.Sprintf(" %s · %s", state, d.Message)
			}
		case d.Total == 0:
			tail = fmt.Sprintf(" %d %s", d.Done, state)
		}
		barW := width - 7 - lipgloss.Width(tail)
		if barW < 4 {
			lines = append(lines, s.Item.Render(fit(fmt.Sprintf("%-6s%s", name, tail), width)))
			continue
		}
		graphic := progressBar(d.Done, d.Total, barW, a.frame(), a.barStyles())
		if !listener && d.Total == 0 && d.Message != "" {
			// No bar for something that does not progress: the words fill the row.
			lines = append(lines, s.Item.Render(fit(name, 7))+s.ItemMuted.Render(fit(tail, width-7)))
			continue
		}
		if listener {
			graphic = listenComets(barW, a.heardPerTick(d.Name), d.State == engine.StateRunning && !waiting, a.cometStyles())
		}
		label := s.Item.Render(fit(name, 7))
		if sweep != nil {
			label = s.ItemMuted.Render(fit(name, 7))
		}
		lines = append(lines, label+graphic+s.Item.Render(fit(tail, width-7-barW)))
	}
	for _, e := range a.status.Enrichers {
		lines = append(lines, a.renderEnricher(e, width))
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
	return append(lines, a.renderLog(width, room)...)
}

// renderLog shows the newest events while following. Pinned, it keeps the
// selected event in view and expands it to its full text.
func (a *app) renderLog(width, room int) []string {
	n := len(a.events)
	if n == 0 {
		return nil
	}
	var out []string
	if a.log.follow {
		for _, ev := range a.events[max(0, n-room):] {
			out = append(out, a.renderEvent(ev, width))
		}
		return out
	}
	cursor := max(0, min(n-1, a.log.cursor))
	selected := a.renderSelectedEvent(a.events[cursor], width)
	// Slide the window so the whole selected event fits.
	a.log.top = min(a.log.top, cursor)
	a.log.top = max(a.log.top, cursor+len(selected)-room)
	a.log.top = max(0, min(n-1, a.log.top))
	for i := a.log.top; i < n && len(out) < room; i++ {
		if i != cursor {
			out = append(out, a.renderEvent(a.events[i], width))
			continue
		}
		for _, l := range selected {
			if len(out) < room {
				out = append(out, l)
			}
		}
	}
	return out
}

// answerWord says what an answer from an enricher is, by the field it
// exists to fill: a name or vendor is "named", a round trip "answered", a
// flag "flagged".
func answerWord(f model.Field) string {
	switch f {
	case model.FieldHostname, model.FieldVendor:
		return "named"
	case model.FieldFlag:
		return "flagged"
	}
	return "answered"
}

// renderEnricher counts what an enricher asked this scan against what it
// got back, so a probe that is asking and learning nothing is plain to
// see. Work still in hand follows; the row is muted until the probe has
// produced something.
func (a *app) renderEnricher(e engine.EnricherStatus, width int) string {
	s := a.renderer.Styles
	line := fmt.Sprintf("%-6s %d asked · %d %s", e.Name, e.Asked, e.Answered, answerWord(e.Produces))
	if e.Running > 0 {
		line += fmt.Sprintf(" · %d running", e.Running)
	}
	if e.Queued > 0 {
		line += fmt.Sprintf(" · %d queued", e.Queued)
	}
	if e.Failed > 0 {
		line += fmt.Sprintf(" · %d failed", e.Failed)
	}
	if e.Renewed > 0 {
		line += fmt.Sprintf(" · %d renewed", e.Renewed)
	}
	style := s.Item
	if e.Answered == 0 {
		style = s.ItemMuted
	}
	return style.Render(fit(line, width))
}

// eventTag is the one-cell glyph for an event kind.
func (a *app) eventTag(kind engine.EventKind) string {
	plain := a.renderer.Styles.PlainUI
	switch kind {
	case engine.KindSent:
		if plain {
			return ">"
		}
		return "→"
	case engine.KindReceived:
		if plain {
			return "<"
		}
		return "←"
	case engine.KindError:
		if plain {
			return "!"
		}
		return "✗"
	case engine.KindProgress:
		if plain {
			return "-"
		}
		return "…"
	}
	if plain {
		return "-"
	}
	return "·"
}

// eventHead is the fixed prefix of a log line: time, probe and tag.
func eventHead(ev engine.ProbeEvent, tag string) string {
	return fmt.Sprintf("%s %-7s %s ", ev.At.Format("15:04:05"), ev.Probe, tag)
}

// describe puts an event's kind and target into words for the expanded
// view, since the tag glyph alone is easy to misread.
func describe(ev engine.ProbeEvent) string {
	switch {
	case ev.Target == "":
		return string(ev.Kind)
	case ev.Kind == engine.KindSent:
		return "sent to " + ev.Target
	case ev.Kind == engine.KindReceived:
		return "received from " + ev.Target
	}
	return fmt.Sprintf("%s · %s", ev.Kind, ev.Target)
}

// renderSelectedEvent shows one event in full: the message wrapped rather
// than truncated, then a line saying what kind of event it was and which
// address it concerned.
func (a *app) renderSelectedEvent(ev engine.ProbeEvent, width int) []string {
	s := a.renderer.Styles
	head := eventHead(ev, a.eventTag(ev.Kind))
	indent := strings.Repeat(" ", lipgloss.Width(head))
	msg := ev.Message
	if msg == "" && ev.Kind == engine.KindProgress {
		msg = fmt.Sprintf("%d/%d", ev.Done, ev.Total)
	}
	var lines []string
	for i, l := range wrap(msg, max(1, width-lipgloss.Width(head))) {
		prefix := indent
		if i == 0 {
			prefix = head
		}
		lines = append(lines, s.ItemSelected.Width(width).Render(fit(prefix+l, width)))
	}
	lines = append(lines, s.DetailFocusLine.Width(width).Render(fit(indent+describe(ev), width)))
	return lines
}

func (a *app) renderEvent(ev engine.ProbeEvent, width int) string {
	s := a.renderer.Styles
	style := s.ItemMuted
	switch ev.Kind {
	case engine.KindSent:
		style = s.Item
	case engine.KindReceived:
		style = s.Badge.Background(s.Theme.Bg)
	case engine.KindError:
		style = a.alert()
	}
	msg := ev.Message
	if msg == "" && ev.Kind == engine.KindProgress {
		msg = fmt.Sprintf("%d/%d", ev.Done, ev.Total)
	}
	return style.Render(fit(eventHead(ev, a.eventTag(ev.Kind))+msg, width))
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
// finished bar is drawn solid in the theme's Unread colour, the same one
// the log uses for packets received, so a sweep that has run its course
// reads as done at a glance.
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
		solid:  s.Item.Foreground(s.Theme.Unread),
	}
}

// frameEvery is the animation clock for the bar, independent of how fast
// probes tick, so flares are visible whatever the sweep rate.
const frameEvery = 100 * time.Millisecond

func (a *app) frame() int {
	return int(a.now.UnixNano() / int64(frameEvery))
}

// isListener spots a discoverer that has no total to count towards and
// says so in its own words: the passive mDNS probe reports "listening".
// A sweep that has not yet announced its total is not a listener.
func isListener(d engine.DiscovererStatus) bool {
	return d.Total == 0 && strings.HasPrefix(d.Message, "listening")
}

// The listener's comets. A probe that listens never finishes, so a bar
// that fills would be a lie; instead its row is a wire, and every message
// the listener hears flies in along it towards the probe's name: a bright
// head trailing light that fades from the theme's accent to its focus
// colour. Several heard in one tick follow each other in a short train. A
// quiet network leaves the wire empty, and a stopped listener's goes dim.
const (
	// cometEvery is the comets' clock: each moves a cell per tick.
	cometEvery = 100 * time.Millisecond
	// cometTail is how many cells of light trail a comet's head.
	cometTail = 4
	// cometTrain is how many comets one tick's messages send at most.
	cometTrain = 3
	// cometKeep is how many ticks of what was heard are kept per
	// listener: more than any row is wide.
	cometKeep = 512
)

// heardLog is what one listener has heard, tick by tick while it runs.
type heardLog struct {
	last    int   // its heard count at the previous tick
	perTick []int // messages heard in each tick, newest last
}

// sampleHeard records how many messages each running listener heard
// since the last tick.
func (a *app) sampleHeard() {
	for _, d := range a.status.Discoverers {
		if !isListener(d) || d.State != engine.StateRunning {
			continue
		}
		if a.heard == nil {
			a.heard = map[string]*heardLog{}
		}
		h := a.heard[d.Name]
		if h == nil {
			h = &heardLog{}
			a.heard[d.Name] = h
		}
		n := d.Done - h.last
		if n < 0 { // a rescan started the listener's count again
			n = d.Done
		}
		h.last = d.Done
		h.perTick = append(h.perTick, n)
		if over := len(h.perTick) - cometKeep; over > 0 {
			h.perTick = h.perTick[over:]
		}
	}
}

func (a *app) heardPerTick(name string) []int {
	if h := a.heard[name]; h != nil {
		return h.perTick
	}
	return nil
}

// cometStyles colour a listener's wire: the bar's brightness levels and,
// when the theme's colours are #rrggbb, the three a comet's tail blends
// between. Without them the tail steps through the levels instead.
type cometStyles struct {
	barStyles
	base                 lipgloss.Style
	blend                bool
	muted, focus, accent [3]float64
}

func (a *app) cometStyles() cometStyles {
	s := a.renderer.Styles
	c := cometStyles{barStyles: a.barStyles(), base: s.Item, blend: true}
	for i, col := range []lipgloss.TerminalColor{s.ItemMuted.GetForeground(), s.Theme.BorderFocus, s.Theme.Unread} {
		rgb, ok := hexRGB(col)
		c.blend = c.blend && ok
		switch i {
		case 0:
			c.muted = rgb
		case 1:
			c.focus = rgb
		case 2:
			c.accent = rgb
		}
	}
	return c
}

// hexRGB reads a #rrggbb colour.
func hexRGB(c lipgloss.TerminalColor) ([3]float64, bool) {
	var rgb [3]float64
	hex, ok := c.(lipgloss.Color)
	if !ok || len(hex) != 7 || hex[0] != '#' {
		return rgb, false
	}
	for i := range rgb {
		v, err := strconv.ParseUint(string(hex[1+2*i:3+2*i]), 16, 8)
		if err != nil {
			return rgb, false
		}
		rgb[i] = float64(v)
	}
	return rgb, true
}

// light is the style of a comet's tail at brightness x, from 1 beside the
// head to nearly 0 at its end: from accent to focus colour, fading
// towards the muted one.
func (c cometStyles) light(x float64) lipgloss.Style {
	if !c.blend {
		switch {
		case x > 0.65:
			return c.solid
		case x > 0.35:
			return c.mid
		}
		return c.dim
	}
	fade := 0.3 + 0.7*x
	hex := "#"
	for i := range c.accent {
		v := c.focus[i] + (c.accent[i]-c.focus[i])*x
		v = c.muted[i] + (v-c.muted[i])*fade
		hex += fmt.Sprintf("%02x", int(math.Round(min(255, max(0, v)))))
	}
	return c.base.Foreground(lipgloss.Color(hex))
}

func listenComets(width int, heard []int, running bool, c cometStyles) string {
	if width < 1 {
		return ""
	}
	wire, tail, head := "─", "━", "●"
	if c.plain {
		wire, tail, head = "-", "=", "o"
	}
	if !running {
		return c.dim.Render(strings.Repeat(wire, width))
	}
	// A message heard age ticks ago entered at the right edge and has
	// moved age cells towards the name since.
	light := make([]float64, width)
	isHead := make([]bool, width)
	for age := 0; age < width+cometTail; age++ {
		j := len(heard) - 1 - age
		if j < 0 {
			break
		}
		for k := 0; k < min(heard[j], cometTrain); k++ {
			x := width - 1 - age - 2*k
			if x >= 0 {
				isHead[x] = true
			}
			for t := 1; t <= cometTail; t++ {
				if xt := x + t; xt >= 0 && xt < width {
					light[xt] = max(light[xt], 1-float64(t)/(cometTail+1))
				}
			}
		}
	}
	var b strings.Builder
	for i := 0; i < width; i++ {
		switch {
		case isHead[i]:
			b.WriteString(c.bright.Render(head))
		case light[i] > 0:
			b.WriteString(c.light(light[i]).Render(tail))
		default:
			b.WriteString(c.dim.Render(wire))
		}
	}
	return b.String()
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
