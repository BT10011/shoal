package ui

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

// displaySafe makes text safe to lay out on a fixed-width line. Everything
// Shoal shows is either its own writing or a value a probe learned from a
// device, and a device can put anything in its name. Combining marks,
// variation selectors and other format characters are the dangerous ones:
// width libraries and terminals disagree about whether they take a cell, so
// a name carrying one makes the line one cell wider than the code that
// padded it believed, and the terminal wraps it, shifting the whole screen
// up by a row. NFC turns decomposed accents back into single precomposed
// runes ("e" + accent becomes "é"), and the rest of the format machinery is
// dropped rather than allowed to desynchronise the layout. Control
// characters become spaces so a name cannot smuggle in a newline or an
// escape sequence.
func displaySafe(s string) string {
	s = norm.NFC.String(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteRune(' ')
		case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Cf, r),
			unicode.Is(unicode.Co, r), unicode.Is(unicode.Cs, r),
			unicode.Is(unicode.Cn, r), unicode.Is(unicode.Mn, r),
			unicode.Is(unicode.Me, r):
			// Dropped: a format rune, a combining mark NFC could not
			// compose, a private-use or unassigned rune, or a control.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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
