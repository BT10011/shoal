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
	Mode   string // how discovery is running, shown in the "under the hood" header
	Theme  string
}

const (
	batchEvery = 100 * time.Millisecond
	clockEvery = time.Second
	// A listener's wave needs a steady clock, since batches only arrive
	// when something is heard; while one runs the UI ticks at this rate.
	animateEvery = waveEvery
	logKeep      = 2000 // events kept for browsing; a /24 sweep produces about 600

	// subnetBelowWidth is the terminal width under which the status bar
	// stops showing the subnet so the key bar keeps move, rescan and stop.
	subnetBelowWidth = 100
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
	theme    string // the confirmed theme name
	width    int
	height   int

	all      []model.DeviceSnapshot // every device in the last batch
	devices  []model.DeviceSnapshot // all, filtered and sorted: what the table shows
	cursor   int
	top      int
	selected string
	focus    pane
	details  tideui.PaneScroller
	events   []engine.ProbeEvent
	status   engine.Status
	now      time.Time

	sort    sortState
	filter  filter
	showRaw bool
	picker  tideui.ThemePicker
	help    help
	log     logView
}

// logView is the reader's place in the event log. While follow is set the
// pane tails the newest events; scrolling up pins it, selects one event
// and shows that event in full, so it can be read while the scan goes on.
type logView struct {
	follow bool
	cursor int // the selected event while pinned
	top    int // the first event shown while pinned
}

func newApp(o Options) *app {
	theme, _ := tideui.ThemeByName(o.Theme)
	return &app{
		opts:     o,
		renderer: newRenderer(theme),
		theme:    theme.Name,
		now:      time.Now(),
		filter:   newFilter(),
		picker:   tideui.NewThemePicker(tideui.ThemePickerOptions{InitialTheme: theme.Name}),
		log:      logView{follow: true},
	}
}

func (a *app) Init() tea.Cmd {
	return a.tick()
}

// tick schedules the next clock message: once a second normally, faster
// while a listener's wave has to keep rolling.
func (a *app) tick() tea.Cmd {
	every := clockEvery
	for _, d := range a.status.Discoverers {
		if isListener(d) && d.State == engine.StateRunning {
			every = animateEvery
			break
		}
	}
	return tea.Tick(every, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
	case tickMsg:
		a.now = time.Time(msg)
		return a, a.tick()
	case Batch:
		a.applyBatch(msg)
	case tea.KeyMsg:
		return a.handleKey(msg)
	default:
		if a.filter.editing { // the input's own messages, such as cursor blinks
			return a, a.filter.update(msg)
		}
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
	if drop := len(a.events) - logKeep; drop > 0 {
		a.events = a.events[drop:]
		a.log.cursor = max(0, a.log.cursor-drop)
		a.log.top = max(0, a.log.top-drop)
	}
	if b.Devices == nil {
		return
	}
	a.all = b.Devices
	a.refresh()
}

// refresh rebuilds the table from the last batch under the current filter
// and sort, keeping the cursor on the same device where it is still shown.
func (a *app) refresh() {
	var shown []model.DeviceSnapshot
	for _, d := range a.all {
		if a.filter.matches(d, a.now) {
			shown = append(shown, d)
		}
	}
	a.devices = sortDevices(shown, a.sort, a.now)
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

func (a *app) move(n int) {
	switch a.focus {
	case paneDetails:
		if n > 0 {
			a.details.ScrollDown(n)
		} else {
			a.details.ScrollUp(-n)
		}
	case paneHood:
		a.scrollLog(n)
	default:
		a.moveTo(a.cursor + n)
	}
}

// moveTo jumps to the first row (i <= 0) or the last (anything else) of
// the focused pane; for the table it selects row i.
func (a *app) moveTo(i int) {
	switch a.focus {
	case paneDetails:
		if i <= 0 {
			a.details.ScrollToTop()
		} else {
			a.details.ScrollDown(1 << 20)
		}
	case paneHood:
		if i <= 0 {
			a.log.follow = false
			a.log.cursor = 0
		} else {
			a.log.follow = true
		}
	default:
		a.cursor = i
		a.syncSelection()
	}
}

// scrollLog moves the log selection. The first step up from following
// pins the log on the newest event; stepping down onto the newest event
// lets it follow again.
func (a *app) scrollLog(n int) {
	last := len(a.events) - 1
	if last < 0 {
		return
	}
	if a.log.follow {
		if n >= 0 {
			return
		}
		a.log.follow = false
		a.log.cursor = last
		n++
	}
	a.log.cursor = max(0, min(last, a.log.cursor+n))
	if n > 0 && a.log.cursor == last {
		a.log.follow = true
	}
}

func (a *app) current() (model.DeviceSnapshot, bool) {
	if len(a.devices) == 0 {
		return model.DeviceSnapshot{}, false
	}
	return a.devices[a.cursor], true
}

// tabbed reports whether the terminal is too small for three panes.
func (a *app) tabbed() bool {
	return a.width < tabbedBelowWidth || a.height < tabbedBelowHeight
}

// focusable is how many panes Tab cycles through.
func (a *app) focusable() pane { return 3 }

func (a *app) View() string {
	if a.width == 0 || a.height == 0 {
		return "starting…"
	}
	tabbed := a.tabbed()
	g := geometry(a.width, a.height, tabbed)
	a.clampTop(g.devices.rows - 1)

	detailLines := a.renderDetails(g.details.cols)
	a.details.ClampTo(len(detailLines), g.details.rows)

	hoodHint := a.opts.Mode
	if a.opts.Demo {
		hoodHint = "simulated, no packets sent"
	}
	if !a.log.follow && len(a.events) > 0 {
		hoodHint = fmt.Sprintf("pinned at %s · G follows again", a.events[min(a.log.cursor, len(a.events)-1)].At.Format("15:04:05"))
	}
	dev, hasDev := a.current()
	detailHint := ""
	if hasDev {
		if ip, ok := dev.ResolvedAt(model.FieldIP, a.now); ok {
			detailHint = ip.Value
		}
	}
	devicesHint := fmt.Sprintf("%d", len(a.devices))
	if a.filter.active() {
		devicesHint = fmt.Sprintf("%d/%d", len(a.devices), len(a.all))
	}
	// A hint that does not fit beside its title would truncate the title,
	// which is worse than losing the hint. Tabs are a third of the width, so
	// they never carry one. The mode is worth keeping: when it cannot sit
	// in the header it becomes the first line of the hood pane.
	hoodLines := a.renderHood(g.hood.cols, g.hood.rows)
	if tabbed || !hintFits("Under the hood", hoodHint, g.hood.cols) {
		hoodLines = append([]string{a.renderer.Styles.ItemMuted.Render(fit(hoodHint, g.hood.cols))}, a.renderHood(g.hood.cols, g.hood.rows-1)...)
		hoodHint = ""
	}
	if tabbed || !hintFits("Devices", devicesHint, g.devices.cols) {
		devicesHint = ""
	}
	if tabbed || !hintFits("Details", detailHint, g.details.cols) {
		detailHint = ""
	}

	mode := tideui.StackedRight
	if tabbed {
		mode = tideui.Tabbed
	}
	left := a.statusLeft()
	layout := tideui.Layout{
		Width: a.width, Height: a.height, Mode: mode,
		SidebarRatio: sidebarRatio, UpperRightRatio: upperRatio,
		Panes: [3]tideui.Pane{
			{Title: "Devices", Hint: devicesHint, Focused: a.focus == paneDevices,
				Content: joinLines(a.renderTable(g.devices.cols, g.devices.rows))},
			{Title: "Details", Hint: detailHint, Focused: a.focus == paneDetails,
				Content: joinLines(detailLines), ScrollOffset: a.details.Offset()},
			{Title: "Under the hood", Hint: hoodHint, Focused: a.focus == paneHood,
				Content: joinLines(hoodLines)},
		},
		Status: &tideui.StatusBar{Left: left, Right: a.statusRight(left)},
	}
	switch {
	case a.help.open:
		m := a.helpModal()
		layout.Modal = &m
	case a.picker.Opened():
		m := a.themeModal()
		layout.Modal = &m
	}
	return a.renderer.Render(layout)
}

// hintFits mirrors tideui's header layout: a two-cell prefix, the title,
// a gap and the hint.
func hintFits(title, hint string, cols int) bool {
	return hint == "" || 2+lipglossWidth(title)+1+lipglossWidth(hint) <= cols
}

func (a *app) visibleRows() int {
	if a.width == 0 || a.height == 0 {
		return 1
	}
	return max(1, geometry(a.width, a.height, a.tabbed()).devices.rows-1)
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

// statusLeft is the state that changes: where we are scanning, how many
// devices, which scan is running. Probe progress lives in the hood pane.
func (a *app) statusLeft() string {
	s := a.renderer.Styles
	text := s.StatusBarJoiner // plain text that keeps the bar's background
	var parts []string
	if a.filter.editing {
		a.styleFilterInput()
		return joinStatus(s, []string{
			a.filter.input.View(),
			text.Render(fmt.Sprintf("%d of %d match", len(a.devices), len(a.all))),
		})
	}
	// Below 100 columns the subnet gives way to the key bar, which is what
	// a new user needs more; the interface name stays.
	narrow := a.width < subnetBelowWidth
	switch {
	case a.opts.Demo && narrow:
		parts = append(parts, s.StatusNotice.Render("DEMO"))
	case a.opts.Demo:
		parts = append(parts, s.StatusNotice.Render("DEMO")+text.Render(" 192.168.1.0/24"))
	case a.opts.Iface.Subnet != nil && narrow:
		parts = append(parts, text.Render(a.opts.Iface.Name))
	case a.opts.Iface.Subnet != nil:
		parts = append(parts, text.Render(a.opts.Iface.Name+" "+a.opts.Iface.Subnet.String()))
	}
	if a.filter.active() {
		parts = append(parts, text.Render(fmt.Sprintf("/%s  %d of %d", a.filter.pattern, len(a.devices), len(a.all))))
	} else {
		parts = append(parts, text.Render(fmt.Sprintf("%d devices", len(a.all))))
	}
	parts = append(parts, text.Render(a.scanLabel()))
	return joinStatus(s, parts)
}

// scanLabel sums up the current scan in a couple of words.
func (a *app) scanLabel() string {
	st := a.status
	if st.Scan == 0 {
		return "starting"
	}
	label := fmt.Sprintf("scan %d", st.Scan)
	switch {
	case st.Sweeping():
		return label + " running"
	case st.Settled():
		return label + " done"
	}
	swept, running := false, false
	for _, d := range st.Discoverers {
		switch d.State {
		case engine.StateCancelled:
			return label + " stopped"
		case engine.StateFailed:
			return label + " failed"
		case engine.StateDone:
			swept = true
		case engine.StateRunning:
			running = true
		}
	}
	switch {
	case swept:
		return label + " finishing" // the sweep is over, lookups are still out
	case running:
		return label + " listening"
	}
	return label
}

// styleFilterInput dresses the text input in the status bar's colours so
// it reads as part of the bar rather than a box dropped on top of it.
func (a *app) styleFilterInput() {
	s := a.renderer.Styles
	in := &a.filter.input
	in.PromptStyle = s.StatusBarJoiner.Bold(true)
	in.TextStyle = s.StatusBarJoiner
	in.PlaceholderStyle = s.StatusHint
	in.CompletionStyle = s.StatusHint
	in.Cursor.Style = s.StatusBarJoiner.Reverse(true)
	in.Cursor.TextStyle = s.StatusBarJoiner
}

// statusRight is the key bar, trimmed to the room the left side leaves.
func (a *app) statusRight(left string) string {
	s := a.renderer.Styles
	bindings := tableBindings
	switch {
	case a.help.open, a.picker.Opened():
		return "" // the modal carries its own hints
	case a.filter.editing:
		bindings = filterBindings
	case a.focus == paneDetails:
		bindings = detailBindings
	case a.focus == paneHood:
		bindings = hoodBindings
	}
	return keyBar(s, bindings, keyBarWidth(a.width, left))
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
