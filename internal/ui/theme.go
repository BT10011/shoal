package ui

import (
	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
)

const themePickerWidth = 34

func newRenderer(t tideui.Theme) tideui.Renderer {
	return tideui.NewRenderer(t, tideui.StyleOptions{Density: tideui.Compact})
}

func (a *app) openTheme() { a.picker.Open(a.theme) }

// handleThemeKey forwards a key to the picker and then re-renders the
// whole TUI in whichever theme is highlighted, so moving through the list
// is a live preview against real data. Enter keeps the highlighted theme;
// Esc puts the picker's cursor back on the confirmed one, which the same
// re-render then restores.
func (a *app) handleThemeKey(msg tea.KeyMsg) {
	if a.picker.Update(msg) == tideui.ThemePickerConfirm {
		a.theme = a.picker.ConfirmedTheme().Name
	}
	a.renderer = newRenderer(a.picker.PreviewTheme())
}

func (a *app) themeModal() tideui.Overlay {
	return a.picker.SoftModal(a.renderer, min(themePickerWidth, max(12, a.width-4)), a.height-2, "shoal")
}
