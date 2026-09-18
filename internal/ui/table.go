package ui

import (
	"sort"
	"strings"
	"time"

	"github.com/allisonhere/tideui"
	"github.com/charmbracelet/lipgloss"

	"github.com/BT10011/shoal/internal/model"
)

type column struct {
	title string
	field model.Field
	width int // 0 = flexible
	drop  int // lower is dropped first when the pane is narrow
}

// The MAC goes first when space is short: it is always in the details pane,
// while hostname and vendor are what make the overview readable. FLAGS
// outlives vendor because a badge saying a device is on the wrong subnet is
// the whole point of looking, for a technician on a venue floor.
var columns = []column{
	{title: "IP", field: model.FieldIP, width: 15, drop: 6},
	{title: "MAC", field: model.FieldMAC, width: 17, drop: 1},
	{title: "HOSTNAME", field: model.FieldHostname, drop: 5},
	{title: "VENDOR", field: model.FieldVendor, drop: 3},
	{title: "RTT", field: model.FieldLatency, width: 7, drop: 2},
	{title: "FLAGS", field: model.FieldFlag, width: 10, drop: 4},
}

// badges are the short forms flags take in the table, in the order they
// are shown: a wrong address first, since that is what a technician is
// looking for, then the rest. The details pane carries the full flag and
// its reasoning.
var badges = []struct{ flag, badge string }{
	{"link-local-ip", "link-local"},
	{"off-subnet-ip", "off-subnet"},
	{"duplicate-ip", "dup-ip"},
	{"this-host", "self"},
	{"locally-administered-mac", "rand-mac"},
}

// badgeText turns a device's flags into the FLAGS cell.
func badgeText(flags []string) string {
	set := make(map[string]bool, len(flags))
	for _, f := range flags {
		set[f] = true
	}
	var out []string
	for _, b := range badges {
		if set[b.flag] {
			out = append(out, b.badge)
			delete(set, b.flag)
		}
	}
	for _, f := range flags { // anything a future probe adds, as it is
		if set[f] {
			out = append(out, f)
		}
	}
	return strings.Join(out, ",")
}

// cell is the table text for one field of a device: the resolved value,
// or for the multi-valued flags every badge.
func cell(d model.DeviceSnapshot, f model.Field, now time.Time) string {
	if f == model.FieldFlag {
		return badgeText(d.Values(f, now))
	}
	if o, ok := d.ResolvedAt(f, now); ok {
		return o.Value
	}
	return ""
}

const minFlexWidth = 14

// markWidth is the freshness mark and its gap at the left of every row.
const markWidth = 2

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

// sortState is the column the table is ordered by. It always refers to
// columns, whether or not the column currently fits on screen.
type sortState struct {
	col     int
	reverse bool
}

func (s *sortState) next() { s.col = (s.col + 1) % len(columns) }

func (s sortState) field() model.Field { return columns[s.col].field }

// sortKey is what one device sorts by in one column: whether it has a
// value at all, and the value as a number where that means something.
type sortKey struct {
	has     bool
	numeric bool
	num     float64
	str     string
}

func keyFor(d model.DeviceSnapshot, f model.Field, now time.Time) sortKey {
	o, ok := d.ResolvedAt(f, now)
	if !ok {
		return sortKey{}
	}
	switch f {
	case model.FieldIP:
		if n, ok := ipKey(o.Value); ok {
			return sortKey{has: true, numeric: true, num: float64(n)}
		}
	case model.FieldLatency:
		if rtt, err := time.ParseDuration(o.Value); err == nil {
			return sortKey{has: true, numeric: true, num: float64(rtt)}
		}
	}
	return sortKey{has: true, str: strings.ToLower(o.Value)}
}

// compare orders numbers before strings, so an IP that failed to parse
// sorts after every address that did.
func compare(a, b sortKey) int {
	switch {
	case a.numeric && b.numeric:
		switch {
		case a.num < b.num:
			return -1
		case a.num > b.num:
			return 1
		}
		return 0
	case a.numeric != b.numeric:
		if a.numeric {
			return -1
		}
		return 1
	}
	return strings.Compare(a.str, b.str)
}

// sortDevices orders devices by the chosen column. Devices with no value in
// that column always go last, whichever direction is chosen; ties fall back
// to the IP and then the key so the order is stable between batches.
func sortDevices(devs []model.DeviceSnapshot, st sortState, now time.Time) []model.DeviceSnapshot {
	type keyed struct {
		dev     model.DeviceSnapshot
		key, ip sortKey
	}
	ks := make([]keyed, len(devs))
	for i, d := range devs {
		ks[i] = keyed{dev: d, key: keyFor(d, st.field(), now), ip: keyFor(d, model.FieldIP, now)}
	}
	sort.SliceStable(ks, func(i, j int) bool {
		a, b := ks[i], ks[j]
		if a.key.has != b.key.has {
			return a.key.has
		}
		if c := compare(a.key, b.key); c != 0 {
			if st.reverse {
				return c > 0
			}
			return c < 0
		}
		if a.ip.has != b.ip.has {
			return a.ip.has
		}
		if c := compare(a.ip, b.ip); c != 0 {
			return c < 0
		}
		return a.dev.Key < b.dev.Key
	})
	out := make([]model.DeviceSnapshot, len(ks))
	for i, k := range ks {
		out[i] = k.dev
	}
	return out
}

func (a *app) sortArrow() string {
	if a.renderer.Styles.PlainUI {
		if a.sort.reverse {
			return "^"
		}
		return "v"
	}
	if a.sort.reverse {
		return "▴"
	}
	return "▾"
}

func (a *app) renderTable(width, rows int) []string {
	cols, widths := layoutColumns(max(1, width-markWidth))
	s := a.renderer.Styles

	cells := make([]string, len(cols))
	for i, c := range cols {
		title := c.title
		if c.field == a.sort.field() {
			title += " " + a.sortArrow()
		}
		cells[i] = fit(title, widths[i])
	}
	header := strings.Repeat(" ", markWidth) + strings.Join(cells, " ")
	lines := []string{s.Item.Bold(true).Width(width).Render(fit(header, width))}
	if len(a.devices) == 0 {
		msg := "waiting for the first reply…"
		if a.filter.active() && len(a.all) > 0 {
			msg = "nothing matches " + a.filter.pattern
		}
		lines = append(lines, s.ItemMuted.Width(width).Render(fit(msg, width)))
		return lines
	}

	end := min(len(a.devices), a.top+max(0, rows-1))
	for i := a.top; i < end; i++ {
		d := a.devices[i]
		fr := classify(d, a.status)
		for j, c := range cols {
			v := cell(d, c.field, a.now)
			if c.field == model.FieldHostname && d.Conflicting(c.field, a.now) {
				v += " !"
			}
			cells[j] = fit(v, widths[j])
		}
		text := fr.mark(s.PlainUI) + " " + strings.Join(cells, " ")
		lines = append(lines, a.renderRow(text, width, i == a.cursor, fr))
	}
	return lines
}

// renderRow colours a row by freshness. Selection wins so the cursor is
// always visible; a device that stopped answering is drawn in the error
// colour, one not yet confirmed by the running scan is muted.
func (a *app) renderRow(text string, width int, selected bool, fr freshness) string {
	switch {
	case selected:
		return a.renderer.RenderRow(tideui.Row{Text: text, Selected: true}, width)
	case fr == notAnswering:
		return a.alert().Width(width).Render(fit(text, width))
	case fr == stale || fr == unheard:
		return a.renderer.RenderRow(tideui.Row{Text: text, Muted: true}, width)
	}
	return a.renderer.RenderRow(tideui.Row{Text: text}, width)
}

// alert is pane text in the theme's error colour: for a value that
// disagrees with another source, or a device that has gone quiet.
func (a *app) alert() lipgloss.Style {
	s := a.renderer.Styles
	return s.Item.Foreground(s.StatusError.GetForeground())
}
