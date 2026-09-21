package ui

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"golang.org/x/text/unicode/norm"

	"github.com/BT10011/shoal/internal/model"
)

const (
	sidebarRatio = 0.6
	upperRatio   = 0.5

	// Below either of these the three panes cannot share the screen, so
	// the layout switches to one pane at a time behind a tab bar.
	tabbedBelowWidth  = 80
	tabbedBelowHeight = 16
)

type box struct{ cols, rows int }

type panes struct{ devices, details, hood box }

// geometry mirrors tideui's arithmetic so the app knows how many content
// lines and columns each pane really has. In tabbed mode every pane gets
// the whole width and everything below the tab bar.
func geometry(width, height int, tabbed bool) panes {
	mainHeight := max(1, height-1)
	if tabbed {
		b := box{cols: max(1, width), rows: max(1, mainHeight-1)}
		return panes{devices: b, details: b, hood: b}
	}
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

const (
	flagFirst, flagLast = 0x1F1E6, 0x1F1FF // regional indicator symbols, A to Z
	toneFirst, toneLast = 0x1F3FB, 0x1F3FF // emoji skin-tone modifiers
)

// displaySafe makes text safe to lay out on a fixed-width line. Everything
// Shoal shows is either its own writing or a value a probe learned from a
// device, and a device can put anything in its name. The danger is a name
// whose width the code and the terminal count differently: the row comes out
// wider than it was padded to, the terminal wraps it, and the whole screen
// scrolls up a line. Nothing can ask the terminal what it will do, so the
// text is cut down to what two independent width models agree on: x/ansi,
// which the layout is built on, and go-runewidth, standing in for the
// terminal.
//
//   - NFC turns decomposed accents back into single runes.
//   - Controls, format characters, private-use and unassigned runes and
//     combining marks NFC could not compose are dropped, and so are emoji
//     skin-tone modifiers: they attach to the rune before them, and a
//     terminal that cannot join an emoji sequence draws one as a glyph of
//     its own. Tabs and line breaks become spaces, so a name cannot break a
//     line or carry an escape sequence.
//   - A flag, two regional indicators that the models count differently, is
//     spelled as its country code.
//   - Any other rune the models disagree about becomes "?", a run of them
//     one.
//   - A last check on the whole string catches what only shows in a
//     sequence, such as a spacing mark after a letter of another script, and
//     repairWidths rebuilds the string.
//
// The tests check that every prefix of the result measures the same either
// way, which is what lets ansi.Truncate cut it anywhere; the code checks
// only the whole.
func displaySafe(s string) string {
	if plainASCII(s) {
		return s // nearly every cell on screen: nothing to change or allocate
	}
	s = norm.NFC.String(s)
	var b strings.Builder
	b.Grow(len(s))
	placeholder := false
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteByte(' ')
		case r >= flagFirst && r <= flagLast:
			b.WriteRune('A' + r - flagFirst)
		case r >= toneFirst && r <= toneLast, dropped(r):
			continue
		case widthDisputed(r):
			if !placeholder {
				b.WriteByte('?')
			}
			placeholder = true
			continue
		default:
			b.WriteRune(r)
		}
		placeholder = false
	}
	out := b.String()
	if widthsAgree(out) {
		return out
	}
	return repairWidths(out)
}

// plainASCII reports whether s is only printable ASCII, which every width
// model counts the same way.
func plainASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// dropped reports whether displaySafe removes r outright: a control, format,
// private-use, surrogate or unassigned rune, or a combining mark.
func dropped(r rune) bool {
	return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs,
		unicode.Cn, unicode.Mn, unicode.Me)
}

// widthsAgree reports whether both width models measure s the same.
func widthsAgree(s string) bool {
	return ansi.StringWidth(s) == runewidth.StringWidth(s)
}

// widthDisputed reports whether the width models disagree about r on its
// own. It asks the libraries rather than keeping a table, so it follows
// whichever versions are built in.
func widthDisputed(r rune) bool {
	return r >= utf8.RuneSelf && !widthsAgree(string(r))
}

// repairWidths is displaySafe's slow path, for the rare string whose two
// measures still differ once every rune has been dealt with. It rebuilds the
// string one rune at a time, keeping a rune only if what has been built
// still measures the same and standing in "?" for one that would not. The
// work grows with the square of the length, which is why only that rare
// case pays it.
func repairWidths(s string) string {
	kept, placeholder := "", false
	for _, r := range s {
		if next := kept + string(r); widthsAgree(next) {
			kept, placeholder = next, false
		} else if !placeholder && widthsAgree(kept+"?") {
			kept, placeholder = kept+"?", true
		}
	}
	return kept
}

// fit truncates s to w cells with an ellipsis and pads to exactly w. Text
// from a probe is put through displaySafe first, so the width this promises
// is the width the terminal will actually draw.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(displaySafe(s), w, "…")
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
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// stamp is a time for the details pane: the clock alone today, the date
// as well otherwise, since history reaches back across visits.
func stamp(now, t time.Time) string {
	now, t = now.Local(), t.Local()
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	if y1 == y2 && m1 == m2 && d1 == d2 {
		return t.Format("15:04:05")
	}
	return t.Format("2006-01-02 15:04")
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

// wrap word-wraps s to width. Text narrower than eight cells is not worth
// wrapping; it is returned whole and truncated by the caller.
func wrap(s string, width int) []string {
	if width < 8 {
		return []string{s}
	}
	var out []string
	for _, line := range strings.Split(ansi.Wordwrap(s, width, ""), "\n") {
		// Word wrapping can leave a line too long, for instance around a
		// word starting "--"; cut such a line rather than overflow.
		if lipgloss.Width(line) > width {
			out = append(out, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
			continue
		}
		out = append(out, line)
	}
	return out
}
