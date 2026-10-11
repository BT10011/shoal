package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/allisonhere/tideui"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

// Removing devices by hand (d, D). Only a device that has stopped
// answering can go, only when the user asks, and only after a modal
// says what will happen; the engine logs every removal. Nothing is ever
// removed automatically, and a device that answers again comes back.

// The choices in the remove modal, in the order shown.
const (
	removeList = iota
	removeForget
	removeCancel
)

// removeModal confirms a removal of one device (d) or of every device not
// answering (D).
type removeModal struct {
	keys      []string
	devices   []model.DeviceSnapshot
	canForget bool // some probe remembers this network, and a device has a MAC
	cursor    int
}

// removedMsg reports a removal done off the UI goroutine.
type removedMsg struct {
	removed, forgotten int
	err                error
}

// notAnswering lists the devices on screen that the scan has finished
// without hearing: the red rows.
func (a *app) notAnswering() []model.DeviceSnapshot {
	var out []model.DeviceSnapshot
	for _, d := range a.all {
		if classify(d, a.status) == notAnswering {
			out = append(out, d)
		}
	}
	return out
}

// removeOne opens the modal for the selected device, or says why it
// cannot be removed.
func (a *app) removeOne() {
	d, ok := a.current()
	if !ok || a.opts.Engine == nil {
		return
	}
	switch classify(d, a.status) {
	case notAnswering:
		a.openRemove([]model.DeviceSnapshot{d})
	case stale:
		a.removeNote(fmt.Sprintf("%s has not been asked yet this scan; wait for the scan to finish, and remove it then if it does not answer", deviceLabel(d, a.now)))
	case unheard:
		a.removeNote(fmt.Sprintf("%s is known only from what others said about it, never from a reply, so there is no silence to judge; it stays", deviceLabel(d, a.now)))
	default:
		a.removeNote(fmt.Sprintf("%s is still answering, so it stays: only a device that has stopped answering can be removed", deviceLabel(d, a.now)))
	}
}

// removeAll opens the modal for every device not answering.
func (a *app) removeAll() {
	if a.opts.Engine == nil {
		return
	}
	red := a.notAnswering()
	if len(red) == 0 {
		a.removeNote("every device is answering, so there is nothing to remove")
		return
	}
	a.openRemove(red)
}

func (a *app) openRemove(devices []model.DeviceSnapshot) {
	m := &removeModal{devices: devices, cursor: removeCancel}
	for _, d := range devices {
		m.keys = append(m.keys, d.Key)
	}
	if a.opts.Engine.CanForget() {
		for _, d := range devices {
			if mac, ok := d.ResolvedAt(model.FieldMAC, a.now); ok && mac.Value == d.Key {
				m.canForget = true
				break
			}
		}
	}
	a.remove = m
}

func (a *app) removeNote(message string) {
	a.events = append(a.events, engine.ProbeEvent{Probe: "remove", Kind: engine.KindInfo, Message: message, At: time.Now()})
}

func (m *removeModal) allowed(choice int) bool {
	return choice != removeForget || m.canForget
}

func (a *app) handleRemoveKey(msg tea.KeyMsg) tea.Cmd {
	m := a.remove
	switch msg.String() {
	case "down", "j":
		for c := m.cursor + 1; c <= removeCancel; c++ {
			if m.allowed(c) {
				m.cursor = c
				break
			}
		}
	case "up", "k":
		for c := m.cursor - 1; c >= removeList; c-- {
			if m.allowed(c) {
				m.cursor = c
				break
			}
		}
	case "esc", "q":
		a.remove = nil
	case "enter":
		a.remove = nil
		if m.cursor == removeCancel {
			return nil
		}
		eng, keys, forget := a.opts.Engine, m.keys, m.cursor == removeForget
		// Forgetting writes to the history file, so off the UI goroutine.
		return func() tea.Msg {
			removed, forgotten, err := eng.Remove(keys, forget)
			return removedMsg{removed: removed, forgotten: forgotten, err: err}
		}
	}
	return nil
}

// removed notes anything that went wrong; the engine has already logged
// each device it removed.
func (a *app) removed(msg removedMsg) {
	if msg.err != nil {
		a.events = append(a.events, engine.ProbeEvent{Probe: "remove", Kind: engine.KindError, At: time.Now(),
			Message: fmt.Sprintf("removed %d, but could not forget every one: %v", msg.removed, msg.err)})
	}
}

// removeText is what the modal says before the user chooses.
func (a *app) removeText(m *removeModal) []string {
	var paras []string
	if len(m.devices) == 1 {
		d := m.devices[0]
		paras = append(paras, fmt.Sprintf("Remove %s from the list?", deviceLabel(d, a.now)))
		if at, source, ok := d.LastContact(); ok {
			when := ago(a.now, at)
			if model.Historical(source) {
				when = "on an earlier visit, " + stamp(a.now, at)
			}
			paras = append(paras, fmt.Sprintf("It did not answer scan %d. It was last heard %s, via %s.", a.status.Scan, when, source))
		}
	} else {
		var names []string
		for i, d := range m.devices {
			if i == 5 {
				names = append(names, fmt.Sprintf("and %d more", len(m.devices)-5))
				break
			}
			names = append(names, deviceLabel(d, a.now))
		}
		paras = append(paras, fmt.Sprintf("Remove the %d devices that did not answer scan %d?", len(m.devices), a.status.Scan),
			strings.Join(names, "; ")+".")
	}
	paras = append(paras, "Nothing is ever removed automatically: this is your call, and the log records it. A removed device comes back if it answers again.")
	if m.canForget {
		paras = append(paras, "This network is remembered. Removing only from this list keeps the memory, so the next visit lists it as missing again. Forgetting it on this network deletes it from the history too, so a later visit that hears it calls it new.")
	}
	return paras
}

func (m *removeModal) choiceText(choice int) string {
	what := "it"
	if len(m.keys) > 1 {
		what = fmt.Sprintf("all %d", len(m.keys))
	}
	switch choice {
	case removeList:
		return fmt.Sprintf("remove %s from this list", what)
	case removeForget:
		if !m.canForget {
			return "remove and forget: this network is not remembered"
		}
		return fmt.Sprintf("remove %s and forget on this network", what)
	default:
		return "cancel"
	}
}

func (a *app) removeModalOverlay() tideui.Overlay {
	m := a.remove
	s := a.renderer.Styles
	width := min(72, max(20, a.width-4))
	inner := width - 4

	var body []string
	for i, para := range a.removeText(m) {
		if i > 0 {
			body = append(body, "")
		}
		for _, line := range wrap(para, inner) {
			body = append(body, fit(line, inner)) // names came from devices
		}
	}
	var choices []string
	for c := removeList; c <= removeCancel; c++ {
		text := fit(m.choiceText(c), inner)
		if !m.allowed(c) {
			choices = append(choices, s.ItemMuted.Render(text))
			continue
		}
		choices = append(choices, a.renderer.RenderSoftRow(tideui.SoftRow{Text: text, Selected: c == m.cursor}, inner))
	}
	hints := a.renderer.RenderSoftHints(inner,
		tideui.SoftHint{Key: a.plainKey("↑↓", "^v"), Label: "choose"},
		tideui.SoftHint{Key: "enter", Label: "go"},
		tideui.SoftHint{Key: "esc", Label: "cancel"},
	)
	lines := append(body, "")
	lines = append(lines, choices...)
	lines = append(lines, "", hints)
	return a.renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "shoal", Title: "remove", Content: a.renderer.RenderSoftBody(width, strings.Join(lines, "\n")), Width: width})
}

// deviceLabel names a device for a sentence: its name and address when
// known, then its key.
func deviceLabel(d model.DeviceSnapshot, now time.Time) string {
	var parts []string
	if o, ok := d.ResolvedAt(model.FieldHostname, now); ok {
		parts = append(parts, o.Value)
	}
	if o, ok := d.ResolvedAt(model.FieldIP, now); ok && o.Value != d.Key {
		parts = append(parts, o.Value)
	}
	parts = append(parts, d.Key)
	return strings.Join(parts, " ")
}
