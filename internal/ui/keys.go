package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/allisonhere/tideui"
)

// handleKey routes a key to whichever part of the UI owns the keyboard: an
// open modal first, then the filter while it is being typed, then the panes.
func (a *app) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return a, tea.Quit
	}
	switch {
	case a.help.open:
		a.handleHelpKey(msg)
		return a, nil
	case a.picker.Opened():
		return a, a.handleThemeKey(msg)
	case a.filter.editing:
		return a, a.handleFilterKey(msg)
	}

	switch msg.String() {
	case "q":
		return a, tea.Quit
	case "?":
		a.help.toggle()
	case "t":
		a.openTheme()
	case "/":
		return a, a.filter.edit()
	case "esc":
		switch {
		case a.filter.active():
			a.filter.clear()
			a.refresh()
		case a.focus != paneDevices:
			a.focus = paneDevices
		}
	case "enter":
		a.focus = paneDetails
	case "tab":
		a.focus = (a.focus + 1) % a.focusable()
	case "shift+tab":
		a.focus = (a.focus + a.focusable() - 1) % a.focusable()
	case "s":
		a.sort.next()
		a.refresh()
	case "S":
		a.sort.reverse = !a.sort.reverse
		a.refresh()
	case "x":
		a.showRaw = !a.showRaw
	case "r":
		if a.opts.Engine != nil {
			a.opts.Engine.Rescan()
		}
	case "c":
		if a.opts.Engine != nil {
			a.opts.Engine.Cancel()
		}
	case "down", "j":
		a.move(1)
	case "up", "k":
		a.move(-1)
	case "pgdown":
		a.move(a.visibleRows())
	case "pgup":
		a.move(-a.visibleRows())
	case "g", "home":
		a.moveTo(0)
	case "G", "end":
		a.moveTo(len(a.devices) - 1)
	}
	return a, nil
}

// handleFilterKey edits the pattern. Movement keys still drive the table
// so the user can pick a device while narrowing the list.
func (a *app) handleFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		a.filter.keep()
		return nil
	case "esc":
		a.filter.clear()
		a.refresh()
		return nil
	case "up":
		a.moveTo(a.cursor - 1)
		return nil
	case "down":
		a.moveTo(a.cursor + 1)
		return nil
	case "pgup":
		a.moveTo(a.cursor - a.visibleRows())
		return nil
	case "pgdown":
		a.moveTo(a.cursor + a.visibleRows())
		return nil
	}
	cmd := a.filter.update(msg)
	a.refresh()
	return cmd
}

// binding is one entry in the key bar. keep ranks which entries survive
// when the bar is short of room: the highest keep values stay longest.
type binding struct {
	key, plainKey, label string
	keep                 int
}

// Stop and rescan rank just below help and quit: they are the two things
// a user reaches for once the table starts moving.
var tableBindings = []binding{
	{"↑↓", "^v", "move", 5},
	{"⏎", "enter", "details", 2},
	{"/", "/", "filter", 4},
	{"s", "s", "sort", 3},
	{"c", "c", "stop", 7},
	{"r", "r", "rescan", 6},
	{"t", "t", "theme", 1},
	{"?", "?", "help", 9},
	{"q", "q", "quit", 8},
}

var detailBindings = []binding{
	{"↑↓", "^v", "scroll", 5},
	{"x", "x", "raw", 4},
	{"esc", "esc", "back", 3},
	{"/", "/", "filter", 2},
	{"c", "c", "stop", 7},
	{"r", "r", "rescan", 6},
	{"?", "?", "help", 9},
	{"q", "q", "quit", 8},
}

var filterBindings = []binding{
	{"⏎", "enter", "keep", 3},
	{"esc", "esc", "clear", 2},
	{"↑↓", "^v", "move", 1},
}

var hoodBindings = []binding{
	{"↑↓", "^v", "select", 5},
	{"G", "G", "follow", 4},
	{"esc", "esc", "back", 3},
	{"c", "c", "stop", 7},
	{"r", "r", "rescan", 6},
	{"?", "?", "help", 9},
	{"q", "q", "quit", 8},
}

// keyBar renders the bindings that fit in width. Rather than truncating
// mid-word it drops whole entries, least important first; the full list is
// in the help.
func keyBar(s tideui.Styles, bindings []binding, width int) string {
	entries := append([]binding(nil), bindings...)
	for len(entries) > 0 {
		var plain, styled []string
		for _, b := range entries {
			key := b.key
			if s.PlainUI {
				key = b.plainKey
			}
			plain = append(plain, key+" "+b.label)
			styled = append(styled, s.StatusBarJoiner.Bold(true).Render(key)+s.StatusHint.Render(" "+b.label))
		}
		if lipglossWidth(strings.Join(plain, "  ")) <= width {
			return strings.Join(styled, s.StatusHint.Render("  "))
		}
		lowest := 0
		for i, b := range entries {
			if b.keep < entries[lowest].keep {
				lowest = i
			}
		}
		entries = append(entries[:lowest], entries[lowest+1:]...)
	}
	return ""
}

// keyBarWidth is what the status bar leaves for the key bar once the
// left-hand text has taken its share, mirroring tideui's arithmetic.
func keyBarWidth(width int, left string) int {
	inner := max(0, width-2)
	return max(0, inner-lipglossWidth(left)-2)
}
