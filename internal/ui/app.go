// Package ui is the Bubble Tea front end. It only ever reads snapshots
// delivered in Batch messages and talks to the engine through Options; it
// never touches engine internals.
package ui

import (
	"context"
	"fmt"
	"time"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/store"
)

// Options wire the UI to a store and engine.
type Options struct {
	Store  *store.Memory
	Engine *engine.Engine
	Iface  netif.Interface
	Demo   bool
	Theme  string
}

const (
	batchEvery = 100 * time.Millisecond
	clockEvery = time.Second
	logKeep    = 500
)

// Run starts the engine, bridges its events into the program, and blocks
// until the user quits.
func Run(ctx context.Context, o Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p := tea.NewProgram(newApp(o), tea.WithAltScreen(), tea.WithContext(ctx))
	b := newBridge(o.Store, o.Engine, p.Send, batchEvery)
	go b.run(ctx)

	if err := o.Engine.Start(ctx); err != nil {
		return err
	}
	_, err := p.Run()
	cancel()
	o.Engine.Stop()
	return err
}

type pane int

const (
	paneDevices pane = iota
	paneDetails
	paneHood
)

type tickMsg time.Time

type app struct {
	opts     Options
	renderer tideui.Renderer
	width    int
	height   int

	devices  []model.DeviceSnapshot
	cursor   int
	top      int
	selected string
	focus    pane
	details  tideui.PaneScroller
	events   []engine.ProbeEvent
	status   engine.Status
	now      time.Time
}

func newApp(o Options) *app {
	theme, _ := tideui.ThemeByName(o.Theme)
	return &app{
		opts:     o,
		renderer: tideui.NewRenderer(theme, tideui.StyleOptions{Density: tideui.Compact}),
		now:      time.Now(),
	}
}

func (a *app) Init() tea.Cmd {
	return tick()
}

func tick() tea.Cmd {
	return tea.Tick(clockEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
	case tickMsg:
		a.now = time.Time(msg)
		return a, tick()
	case Batch:
		a.applyBatch(msg)
	case tea.KeyMsg:
		return a.handleKey(msg)
	}
	return a, nil
}

func (a *app) applyBatch(b Batch) {
	a.now = b.At
	a.status = b.Status
	for _, ev := range b.Events {
		if ev.Kind != engine.KindProgress { // progress is drawn as a bar, not log lines
			a.events = append(a.events, ev)
		}
	}
	if len(a.events) > logKeep {
		a.events = a.events[len(a.events)-logKeep:]
	}
	if b.Devices == nil {
		return
	}
	a.devices = sortByIP(b.Devices)
	a.cursor = 0
	for i, d := range a.devices {
		if d.Key == a.selected {
			a.cursor = i
			break
		}
	}
	a.syncSelection()
}

func (a *app) syncSelection() {
	if len(a.devices) == 0 {
		a.cursor, a.selected = 0, ""
		return
	}
	if a.cursor >= len(a.devices) {
		a.cursor = len(a.devices) - 1
	}
	if a.cursor < 0 {
		a.cursor = 0
	}
	if key := a.devices[a.cursor].Key; key != a.selected {
		a.selected = key
		a.details.ScrollToTop()
	}
}

func (a *app) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return a, tea.Quit
	case "tab":
		a.focus = (a.focus + 1) % 2
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

func (a *app) move(n int) {
	if a.focus == paneDetails {
		if n > 0 {
			a.details.ScrollDown(n)
		} else {
			a.details.ScrollUp(-n)
		}
		return
	}
	a.moveTo(a.cursor + n)
}

func (a *app) moveTo(i int) {
	if a.focus == paneDetails {
		if i <= 0 {
			a.details.ScrollToTop()
		} else {
			a.details.ScrollDown(1 << 20)
		}
		return
	}
	a.cursor = i
	a.syncSelection()
}

func (a *app) current() (model.DeviceSnapshot, bool) {
	if len(a.devices) == 0 {
		return model.DeviceSnapshot{}, false
	}
	return a.devices[a.cursor], true
}

func (a *app) View() string {
	if a.width == 0 || a.height == 0 {
		return "starting…"
	}
	g := geometry(a.width, a.height)
	a.clampTop(g.devices.rows - 1)

	detailLines := a.renderDetails(g.details.cols)
	a.details.ClampTo(len(detailLines), g.details.rows)

	hint := "live"
	if a.opts.Demo {
		hint = "demo"
	}
	dev, hasDev := a.current()
	detailHint := ""
	if hasDev {
		if ip, ok := dev.ResolvedAt(model.FieldIP, a.now); ok {
			detailHint = ip.Value
		}
	}

	layout := tideui.Layout{
		Width: a.width, Height: a.height, Mode: tideui.StackedRight,
		SidebarRatio: sidebarRatio, UpperRightRatio: upperRatio,
		Panes: [3]tideui.Pane{
			{Title: "Devices", Hint: fmt.Sprintf("%d", len(a.devices)), Focused: a.focus == paneDevices,
				Content: joinLines(a.renderTable(g.devices.cols, g.devices.rows))},
			{Title: "Details", Hint: detailHint, Focused: a.focus == paneDetails,
				Content: joinLines(detailLines), ScrollOffset: a.details.Offset()},
			{Title: "Under the hood", Hint: hint,
				Content: joinLines(a.renderHood(g.hood.cols, g.hood.rows))},
		},
		Status: &tideui.StatusBar{Left: a.statusLeft(), Right: a.statusRight()},
	}
	return a.renderer.Render(layout)
}

func (a *app) visibleRows() int {
	if a.width == 0 || a.height == 0 {
		return 1
	}
	return max(1, geometry(a.width, a.height).devices.rows-1)
}

// clampTop keeps the cursor inside the visible window of the table.
func (a *app) clampTop(visible int) {
	if visible < 1 {
		visible = 1
	}
	if a.cursor < a.top {
		a.top = a.cursor
	}
	if a.cursor >= a.top+visible {
		a.top = a.cursor - visible + 1
	}
	if a.top < 0 {
		a.top = 0
	}
}

func (a *app) statusLeft() string {
	s := a.renderer.Styles
	var parts []string
	if a.opts.Demo {
		parts = append(parts, s.StatusNotice.Render(" DEMO "), "192.168.1.0/24 simulated, no packets sent")
	} else if a.opts.Iface.Subnet != nil {
		parts = append(parts, a.opts.Iface.Name+" "+a.opts.Iface.Subnet.String())
	}
	parts = append(parts, fmt.Sprintf("%d devices", len(a.devices)))
	for _, d := range a.status.Discoverers {
		parts = append(parts, fmt.Sprintf("%s %s %d/%d", d.Name, d.State, d.Done, d.Total))
	}
	return joinStatus(s, parts)
}

func (a *app) statusRight() string {
	return a.renderer.Styles.StatusHint.Render("↑↓ move  tab pane  q quit")
}

func joinStatus(s tideui.Styles, parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += s.StatusBarJoiner.Render(s.StatusBarSeparator())
		}
		out += p
	}
	return out
}
