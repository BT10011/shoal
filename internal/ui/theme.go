package ui

import (
	"fmt"
	"time"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/BT10011/shoal/internal/engine"
)

const themePickerWidth = 34

func newRenderer(t tideui.Theme) tideui.Renderer {
	return tideui.NewRenderer(t, tideui.StyleOptions{Density: tideui.Compact})
}

func (a *app) openTheme() { a.picker.Open(a.theme) }

// handleThemeKey forwards a key to the picker and then re-renders the
// whole TUI in whichever theme is highlighted, so moving through the list
// is a live preview against real data. Enter keeps the highlighted theme
// and remembers it for next time; Esc puts the picker's cursor back on the
// confirmed one, which the same re-render then restores.
func (a *app) handleThemeKey(msg tea.KeyMsg) tea.Cmd {
	var cmd tea.Cmd
	if a.picker.Update(msg) == tideui.ThemePickerConfirm {
		a.theme = a.picker.ConfirmedTheme().Name
		cmd = a.saveTheme(a.theme)
	}
	a.renderer = newRenderer(a.picker.PreviewTheme())
	return cmd
}

// themeSavedMsg reports how remembering a theme went.
type themeSavedMsg struct {
	name string
	err  error
}

// saveTheme writes the choice away from the UI goroutine.
func (a *app) saveTheme(name string) tea.Cmd {
	save := a.opts.SaveTheme
	if save == nil {
		return nil
	}
	return func() tea.Msg { return themeSavedMsg{name: name, err: save(name)} }
}

// themeSaved notes the outcome in the log: quietly when it worked, as an
// error when the choice could not be kept for next time.
func (a *app) themeSaved(msg themeSavedMsg) {
	ev := engine.ProbeEvent{Probe: "config", Kind: engine.KindInfo, At: time.Now(),
		Message: fmt.Sprintf("theme %s kept for next time", msg.name)}
	if msg.err != nil {
		ev.Kind = engine.KindError
		ev.Message = fmt.Sprintf("theme %s is in use but could not be kept for next time: %v", msg.name, msg.err)
	}
	a.events = append(a.events, ev)
}

func (a *app) themeModal() tideui.Overlay {
	return a.picker.SoftModal(a.renderer, min(themePickerWidth, max(12, a.width-4)), a.height-2, "shoal")
}
