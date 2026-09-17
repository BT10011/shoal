package ui

import (
	"path"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/BT10011/shoal/internal/model"
)

// filter narrows the table to devices with a matching value. The pattern
// applies as it is typed; Enter keeps it, Esc clears it.
type filter struct {
	input   textinput.Model
	editing bool
	pattern string
}

func newFilter() filter {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "ip, mac, name, vendor, service, flag…"
	ti.CharLimit = 64
	return filter{input: ti}
}

func (f *filter) edit() tea.Cmd {
	f.editing = true
	f.input.SetValue(f.pattern)
	f.input.CursorEnd()
	return f.input.Focus()
}

func (f *filter) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	f.pattern = f.input.Value()
	return cmd
}

func (f *filter) keep() {
	f.editing = false
	f.input.Blur()
}

func (f *filter) clear() {
	f.editing = false
	f.pattern = ""
	f.input.SetValue("")
	f.input.Blur()
}

func (f filter) active() bool { return f.pattern != "" }

// glob reports whether the pattern should be read as a shell-style glob
// rather than a substring.
func glob(pattern string) bool { return strings.ContainsAny(pattern, "*?[") }

// matches reports whether any live value of any field, or the device key,
// matches the pattern. Matching ignores case. A pattern containing * ? or [
// is a glob that must match a whole value; anything else is a substring.
func (f filter) matches(d model.DeviceSnapshot, now time.Time) bool {
	if !f.active() {
		return true
	}
	p := strings.ToLower(f.pattern)
	match := func(v string) bool {
		v = strings.ToLower(v)
		if glob(p) {
			ok, err := path.Match(p, v)
			return err == nil && ok
		}
		return strings.Contains(v, p)
	}
	if match(d.Key) {
		return true
	}
	for _, field := range d.Fields() {
		for _, o := range d.Live(field, now) {
			if match(o.Value) {
				return true
			}
		}
	}
	return false
}
