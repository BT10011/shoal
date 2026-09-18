package ui

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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

// wrap word-wraps s to width. Text narrower than eight cells is not worth
// wrapping; it is returned whole and truncated by the caller.
func wrap(s string, width int) []string {
	if width < 8 {
		return []string{s}
	}
	return strings.Split(ansi.Wordwrap(s, width, ""), "\n")
}
