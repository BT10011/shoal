package ui

import (
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/allisonhere/tideui"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

const (
	sidebarRatio = 0.56
	upperRatio   = 0.5
)

type box struct{ cols, rows int }

type panes struct{ devices, details, hood box }

// geometry mirrors tideui's StackedRight arithmetic so the app knows how
// many content lines and columns each pane really has.
func geometry(width, height int) panes {
	mainHeight := max(1, height-1)
	sidebarW := ratioSize(width, sidebarRatio)
	rightW := width - sidebarW
	upperH := ratioSize(mainHeight, upperRatio)
	lowerH := mainHeight - upperH
	return panes{
		devices: inner(sidebarW, mainHeight),
		details: inner(rightW, upperH),
		hood:    inner(rightW, lowerH),
	}
}

func ratioSize(total int, ratio float64) int {
	if total <= 1 {
		return total
	}
	return max(1, min(total-1, int(float64(total)*ratio)))
}

func inner(w, h int) box {
	cols, rows := w, h
	if w > 2 {
		cols = w - 2
	}
	if h > 2 {
		rows = h - 2
	}
	return box{cols: max(1, cols), rows: max(0, max(1, rows)-1)}
}

func joinLines(lines []string) string { return strings.Join(lines, "\n") }

// fit truncates s to w cells with an ellipsis and pads to exactly w.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}

func ttl(o model.Observation, now time.Time) string {
	if o.TTL == 0 {
		return "no expiry"
	}
	left := o.At.Add(o.TTL).Sub(now)
	if left <= 0 {
		return "expired"
	}
	return "ttl " + left.Round(time.Second).String()
}

func ipKey(ip string) (uint32, bool) {
	p := net.ParseIP(ip).To4()
	if p == nil {
		return 0, false
	}
	return binary.BigEndian.Uint32(p), true
}

// sortByIP orders devices numerically by resolved IP, then by key.
func sortByIP(devs []model.DeviceSnapshot) []model.DeviceSnapshot {
	now := time.Now()
	type keyed struct {
		dev   model.DeviceSnapshot
		ip    uint32
		hasIP bool
	}
	ks := make([]keyed, len(devs))
	for i, d := range devs {
		ks[i] = keyed{dev: d}
		if o, ok := d.ResolvedAt(model.FieldIP, now); ok {
			ks[i].ip, ks[i].hasIP = ipKey(o.Value)
		}
	}
	sort.SliceStable(ks, func(i, j int) bool {
		a, b := ks[i], ks[j]
		if a.hasIP != b.hasIP {
			return a.hasIP
		}
		if a.ip != b.ip {
			return a.ip < b.ip
		}
		return a.dev.Key < b.dev.Key
	})
	out := make([]model.DeviceSnapshot, len(ks))
	for i, k := range ks {
		out[i] = k.dev
	}
	return out
}

type column struct {
	title string
	field model.Field
	width int // 0 = flexible
	drop  int // lower is dropped first when the pane is narrow
}

// The MAC goes first when space is short: it is always in the details pane,
// while hostname and vendor are what make the overview readable.
var columns = []column{
	{title: "IP", field: model.FieldIP, width: 15, drop: 5},
	{title: "MAC", field: model.FieldMAC, width: 17, drop: 1},
	{title: "HOSTNAME", field: model.FieldHostname, drop: 4},
	{title: "VENDOR", field: model.FieldVendor, drop: 3},
	{title: "RTT", field: model.FieldLatency, width: 7, drop: 2},
}

const minFlexWidth = 14

// layoutColumns picks the columns that fit in width and sizes the flexible ones.
func layoutColumns(width int) ([]column, []int) {
	cols := append([]column(nil), columns...)
	for {
		fixed, flex := 0, 0
		for _, c := range cols {
			if c.width > 0 {
				fixed += c.width
			} else {
				flex++
			}
		}
		gaps := max(0, len(cols)-1)
		need := fixed + gaps + flex*minFlexWidth
		if need <= width || len(cols) <= 1 {
			widths := make([]int, len(cols))
			spare := width - fixed - gaps
			for i, c := range cols {
				if c.width > 0 {
					widths[i] = c.width
				} else {
					widths[i] = spare / max(1, flex)
				}
			}
			if flex == 0 && len(widths) > 0 {
				widths[len(widths)-1] += max(0, width-need)
			}
			for i := len(widths) - 1; i >= 0; i-- {
				total := gaps
				for _, w := range widths {
					total += w
				}
				if total <= width {
					break
				}
				widths[i] = max(1, widths[i]-(total-width))
			}
			return cols, widths
		}
		lowest := 0
		for i, c := range cols {
			if c.drop < cols[lowest].drop {
				lowest = i
			}
		}
		cols = append(cols[:lowest], cols[lowest+1:]...)
	}
}

func (a *app) renderTable(width, rows int) []string {
	cols, widths := layoutColumns(width)
	s := a.renderer.Styles

	cells := make([]string, len(cols))
	for i, c := range cols {
		cells[i] = fit(c.title, widths[i])
	}
	lines := []string{s.Item.Bold(true).Width(width).Render(fit(strings.Join(cells, " "), width))}
	if len(a.devices) == 0 {
		lines = append(lines, s.ItemMuted.Width(width).Render(fit("waiting for the first reply…", width)))
		return lines
	}

	end := min(len(a.devices), a.top+max(0, rows-1))
	for i := a.top; i < end; i++ {
		d := a.devices[i]
		for j, c := range cols {
			v := ""
			if o, ok := d.ResolvedAt(c.field, a.now); ok {
				v = o.Value
			}
			if c.field == model.FieldHostname && d.Conflicting(c.field, a.now) {
				v += " !"
			}
			cells[j] = fit(v, widths[j])
		}
		lines = append(lines, a.renderer.RenderRow(tideui.Row{Text: strings.Join(cells, " "), Selected: i == a.cursor}, width))
	}
	return lines
}

var detailOrder = []model.Field{
	model.FieldIP, model.FieldMAC, model.FieldHostname, model.FieldVendor, model.FieldLatency,
	model.FieldType, model.FieldService, model.FieldPort, model.FieldFlag,
}

func (a *app) renderDetails(width int) []string {
	s := a.renderer.Styles
	d, ok := a.current()
	if !ok {
		return []string{s.ItemMuted.Render(fit("select a device to see how each value was learned", width))}
	}

	title := d.Key
	if h, ok := d.ResolvedAt(model.FieldHostname, a.now); ok {
		title = h.Value + "  " + d.Key
	}
	lines := []string{
		s.DetailTitle.Render(ansi.Truncate(title, max(1, width-2), "…")),
		s.DetailMeta.Render(fit(fmt.Sprintf("first seen %s · last seen %s (%s)",
			d.FirstSeen.Format("15:04:05"), d.LastSeen.Format("15:04:05"), ago(a.now, d.LastSeen)), width)),
	}

	label := s.Item.Bold(true)
	muted := s.ItemMuted
	conflict := s.StatusError.Background(s.Theme.Bg)
	for _, f := range detailOrder {
		live := d.Live(f, a.now)
		if len(live) == 0 {
			continue
		}
		lines = append(lines, "")
		seen := map[string]bool{}
		for i, o := range live {
			name := ""
			if i == 0 {
				name = string(f)
			}
			value := o.Value
			style := s.Item
			switch {
			case !f.MultiValued() && i > 0 && !seen[o.Value]:
				value += "  (disagrees)"
				style = conflict
			case !f.MultiValued() && i > 0:
				value += "  (agrees)"
				style = muted
			}
			seen[o.Value] = true
			lines = append(lines, label.Render(fit(name, 10))+" "+style.Render(fit(value, max(1, width-11))))
			for i, l := range wrap(o.Method, width-4) {
				prefix := "    "
				if i == 0 {
					prefix = "  ← "
				}
				lines = append(lines, muted.Render(fit(prefix+l, width)))
			}
			lines = append(lines, muted.Render(fit(fmt.Sprintf("    %s · conf %.1f · %s · %s", o.Source, o.Confidence, ago(a.now, o.At), ttl(o, a.now)), width)))
		}
	}
	return lines
}

func wrap(s string, width int) []string {
	if width < 8 {
		return []string{s}
	}
	return strings.Split(ansi.Wordwrap(s, width, ""), "\n")
}

func (a *app) renderHood(width, rows int) []string {
	s := a.renderer.Styles
	var lines []string

	for _, d := range a.status.Discoverers {
		state := string(d.State)
		if d.Err != "" {
			state = "failed: " + d.Err
		}
		counts := fmt.Sprintf("%d/%d", d.Done, d.Total)
		barW := width - 7 - len(counts) - 1 - lipgloss.Width(state) - 2
		line := fmt.Sprintf("%-6s %s %s %s", d.Name, progressBar(d.Done, d.Total, barW, s.PlainUI), counts, state)
		lines = append(lines, s.Item.Render(fit(line, width)))
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
		tag, style = "✗", s.StatusError.Background(s.Theme.Bg)
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

// Dotted, retro-looking progress bar. Filled cells pick a dense braille
// glyph by hashing their position, so the texture looks random yet stays
// put as the bar grows; the head cell cycles with each tick so motion is
// visible even between cell boundaries.
var (
	dotFilled   = []string{"⣿", "⣷", "⣯", "⣟", "⡿", "⢿", "⣻", "⣽"}
	dotHead     = []string{"⠁", "⠃", "⠇", "⡇", "⣇", "⣧"}
	dotEmpty    = "·"
	asciiFilled = []string{":", ":", ":", ";"}
	asciiHead   = []string{"-", "\\", "|", "/"}
	asciiEmpty  = "."
)

func progressBar(done, total, width int, plain bool) string {
	if width < 1 {
		return ""
	}
	filled, head, empty := dotFilled, dotHead, dotEmpty
	if plain {
		filled, head, empty = asciiFilled, asciiHead, asciiEmpty
	}
	cells := 0
	if total > 0 {
		cells = min(width, done*width/total)
	}
	var b strings.Builder
	for i := 0; i < cells; i++ {
		b.WriteString(filled[cellHash(i)%len(filled)])
	}
	rest := width - cells
	if rest > 0 && done > 0 && done < total {
		b.WriteString(head[done%len(head)])
		rest--
	}
	b.WriteString(strings.Repeat(empty, rest))
	return b.String()
}

func cellHash(i int) int {
	x := uint32(i)*2654435761 + 0x9e3779b9
	x ^= x >> 15
	x *= 0x85ebca6b
	x ^= x >> 13
	return int(x & 0x7fffffff)
}
