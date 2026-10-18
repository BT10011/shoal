package ui

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/store"
)

// Session is one scan of one network: the probes for an interface and the
// store they fill. Switching interface ends one session and starts another,
// since devices on another network are not the same devices.
type Session struct {
	Store  *store.Memory
	Engine *engine.Engine
	Iface  netif.Interface
	Mode   string
}

// SweepChoice is what the user decided about a subnet too large to sweep
// without asking.
type SweepChoice int

const (
	SweepAsk   SweepChoice = iota // ask first if the subnet is too large
	SweepAll                      // every address in the subnet
	SweepLocal                    // only the /24 around this machine
	SweepNone                     // sweep nothing; the listeners still run
)

// LargeSweep describes a subnet too large to sweep without asking, so the
// UI can say what sweeping it would cost before anything is sent.
type LargeSweep struct {
	Iface   netif.Interface
	Hosts   int        // addresses in the subnet
	Limit   int        // the most swept without asking (--max-hosts)
	Largest int        // the most swept at all; above it SweepAll is refused
	Rate    int        // requests per second a full sweep sends
	Local   *net.IPNet // the /24 around this machine
}

// openedMsg is the answer to Options.Open, delivered once the previous
// session has stopped.
type openedMsg struct {
	iface   netif.Interface
	session Session
	large   *LargeSweep
	err     error
	capture *captureMsg // a recording of the old network, ended by the switch
}

// ifacesMsg carries the list of interfaces for the picker.
type ifacesMsg struct {
	list []netif.Interface
	err  error
}

// start runs a session: its engine, and a bridge feeding its batches to
// the program. Batches from earlier sessions are told apart by generation
// and dropped, so a late one cannot put the old network back on screen.
func (a *app) start(s Session) error {
	a.gen++
	ctx, cancel := context.WithCancel(a.ctx)
	b := newBridge(s.Store, s.Engine, a.send, batchEvery)
	b.gen = a.gen
	go b.run(ctx)
	if err := s.Engine.Start(a.ctx); err != nil {
		cancel()
		return err
	}
	a.stopBridge = cancel
	a.opts.Store, a.opts.Engine, a.opts.Iface, a.opts.Mode = s.Store, s.Engine, s.Iface, s.Mode
	return nil
}

// switchTo ends the current session, if any, and opens one on iface. The
// old engine is stopped and the new one built off the UI goroutine, since
// stopping waits for every probe to let go of its socket.
func (a *app) switchTo(iface netif.Interface, choice SweepChoice) tea.Cmd {
	old := a.opts.Engine
	if a.stopBridge != nil {
		a.stopBridge()
		a.stopBridge = nil
	}
	a.gen++ // anything the old bridge still has in flight is now stale
	a.opts.Engine, a.opts.Store = nil, nil
	a.opts.Iface = iface
	a.opts.Mode = "starting on " + iface.Name
	a.all, a.devices, a.selected, a.cursor, a.top = nil, nil, "", 0, 0
	a.status = engine.Status{}
	a.heard = nil
	a.switching = true
	a.pending.Add(1)
	open, capture := a.opts.Open, a.opts.Capture
	return func() tea.Msg {
		defer a.pending.Done()
		if old != nil {
			old.Stop()
		}
		// One file is one network: the old engine has stopped, so its last
		// packets are in, and the recording ends with it.
		rec := stopCaptureFor(capture, "switched to "+iface.Name)
		s, large, err := open(iface, choice)
		return openedMsg{iface: iface, session: s, large: large, err: err, capture: rec}
	}
}

// opened takes the answer to a switch: a session to run, a large subnet
// to ask about, or an error to log.
func (a *app) opened(msg openedMsg) {
	a.switching = false
	if msg.capture != nil {
		a.captured(*msg.capture)
	}
	switch {
	case msg.err != nil:
		a.opts.Mode = "not scanning"
		a.note(engine.KindError, fmt.Sprintf("could not start on %s: %v; press i to choose another interface", msg.iface.Name, msg.err))
	case msg.large != nil:
		a.large = &largeModal{info: *msg.large, cursor: largeLocal}
		a.opts.Mode = "waiting for a choice"
	default:
		if err := a.start(msg.session); err != nil {
			a.opts.Mode = "not scanning"
			a.note(engine.KindError, fmt.Sprintf("could not start on %s: %v", msg.iface.Name, err))
		}
	}
}

// note adds a line of the UI's own to the log.
func (a *app) note(kind engine.EventKind, message string) {
	a.events = append(a.events, engine.ProbeEvent{Probe: "iface", Kind: kind, Message: message, At: time.Now()})
}

// listInterfaces asks for the interfaces off the UI goroutine.
func (a *app) listInterfaces() tea.Cmd {
	list := a.opts.Interfaces
	return func() tea.Msg {
		l, err := list()
		return ifacesMsg{list: l, err: err}
	}
}

// ifacePicker is the modal opened with i: every network this machine is
// on, one row per IPv4 address.
type ifacePicker struct {
	open   bool
	list   []netif.Interface
	cursor int
}

func (a *app) openIfaces(msg ifacesMsg) {
	if msg.err != nil {
		a.note(engine.KindError, "could not list interfaces: "+msg.err.Error())
		return
	}
	if len(msg.list) == 0 {
		a.note(engine.KindInfo, "no other interface is up with an IPv4 address")
		return
	}
	a.ifaces = ifacePicker{open: true, list: msg.list}
	for i, c := range msg.list {
		if c.Same(a.opts.Iface) {
			a.ifaces.cursor = i
		}
	}
}

func (a *app) handleIfaceKey(msg tea.KeyMsg) tea.Cmd {
	p := &a.ifaces
	switch msg.String() {
	case "esc", "i", "q":
		p.open = false
	case "down", "j":
		p.cursor = min(len(p.list)-1, p.cursor+1)
	case "up", "k":
		p.cursor = max(0, p.cursor-1)
	case "enter":
		p.open = false
		chosen := p.list[p.cursor]
		if chosen.Same(a.opts.Iface) && a.opts.Engine != nil {
			a.note(engine.KindInfo, fmt.Sprintf("already scanning %s %s; r rescans it", chosen.Name, chosen.Subnet))
			return nil
		}
		a.note(engine.KindInfo, fmt.Sprintf("switching to %s %s: stopping the current scan and starting afresh; devices from the other network are not carried over", chosen.Name, chosen.Subnet))
		return a.switchTo(chosen, SweepAsk)
	}
	return nil
}

// ifaceRow is one line of the picker, its name padded to nameW.
func (a *app) ifaceRow(c netif.Interface, nameW int) string {
	hosts := thousands(c.Hosts()) + " hosts"
	if c.Hosts() == 1 {
		hosts = "1 host"
	}
	parts := []string{fmt.Sprintf("%-*s %-18s", nameW, c.Name, c.IP.String()+"/"+prefixLen(c.Subnet)), hosts}
	if c.Gateway != nil {
		parts = append(parts, "gw "+c.Gateway.String())
	}
	if len(c.MAC) != 6 {
		parts = append(parts, "no MAC, no ARP")
	}
	if c.Same(a.opts.Iface) {
		parts = append(parts, "· scanning")
	}
	return strings.Join(parts, "  ")
}

func (a *app) ifaceModal() tideui.Overlay {
	width := min(76, max(20, a.width-4))
	inner := width - 4
	p := a.ifaces
	rows := max(1, min(len(p.list), a.height-12))
	first := max(0, min(p.cursor-rows+1, len(p.list)-rows))
	first = min(first, p.cursor)
	nameW := 0
	for _, c := range p.list {
		nameW = min(16, max(nameW, len(c.Name)))
	}
	var lines []string
	for i := first; i < len(p.list) && i < first+rows; i++ {
		lines = append(lines, a.renderer.RenderSoftRow(tideui.SoftRow{Text: fit(a.ifaceRow(p.list[i], nameW), inner), Selected: i == p.cursor}, inner))
	}
	// What the highlighted entry would scan, and how its gateway was
	// judged, so the choice is never a guess.
	c := p.list[p.cursor]
	lines = append(lines, "")
	for _, l := range wrap(fmt.Sprintf("Scans %s on %s. Gateway: %s.", c.Subnet, c.Name, gatewayWords(c)), inner) {
		lines = append(lines, a.renderer.Styles.ItemMuted.Render(fit(l, inner)))
	}
	lines = append(lines, "", a.renderer.RenderSoftHints(inner,
		tideui.SoftHint{Key: a.plainKey("↑↓", "^v"), Label: "move"},
		tideui.SoftHint{Key: "enter", Label: "scan it"},
		tideui.SoftHint{Key: "esc", Label: "close"},
	))
	return a.renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "shoal", Title: "interfaces", Content: a.renderer.RenderSoftBody(width, strings.Join(lines, "\n")), Width: width})
}

func gatewayWords(c netif.Interface) string {
	if c.Gateway != nil {
		return fmt.Sprintf("%s (%s)", c.Gateway, c.GatewaySource)
	}
	if c.GatewaySource == "" {
		return "none"
	}
	return "none, " + c.GatewaySource
}

func (a *app) plainKey(fancy, plain string) string {
	if a.renderer.Styles.PlainUI {
		return plain
	}
	return fancy
}

// The choices offered for a large subnet, in the order shown.
const (
	largeAll = iota
	largeLocal
	largeNone
)

// largeModal asks what to do with a subnet larger than shoal sweeps
// without asking, after saying plainly what sweeping it costs.
type largeModal struct {
	info   LargeSweep
	cursor int
	scroll tideui.PaneScroller
}

// allowed reports whether a choice can be taken: sweeping everything is
// refused above the largest sweep.
func (m *largeModal) allowed(choice int) bool {
	return choice != largeAll || m.info.Hosts <= m.info.Largest
}

func (a *app) handleLargeKey(msg tea.KeyMsg) tea.Cmd {
	m := a.large
	switch msg.String() {
	case "q":
		return tea.Quit
	case "down", "j":
		if m.cursor < largeNone {
			m.cursor++
		}
	case "up", "k":
		if m.cursor > largeAll && m.allowed(m.cursor-1) {
			m.cursor--
		}
	case "pgdown", " ":
		m.scroll.ScrollDown(5)
	case "pgup", "b":
		m.scroll.ScrollUp(5)
	case "esc":
		return a.decideLarge(largeNone)
	case "enter":
		return a.decideLarge(m.cursor)
	}
	return nil
}

func (a *app) decideLarge(choice int) tea.Cmd {
	info := a.large.info
	a.large = nil
	switch choice {
	case largeAll:
		a.note(engine.KindInfo, fmt.Sprintf("sweeping all %s addresses of %s, as chosen after the warning", thousands(info.Hosts), info.Iface.Subnet))
		return a.switchTo(info.Iface, SweepAll)
	case largeLocal:
		a.note(engine.KindInfo, fmt.Sprintf("sweeping only %s, the part of %s around this machine", info.Local, info.Iface.Subnet))
		return a.switchTo(info.Iface, SweepLocal)
	default:
		a.note(engine.KindInfo, fmt.Sprintf("not sweeping %s: listening only, so devices appear as they speak", info.Iface.Subnet))
		return a.switchTo(info.Iface, SweepNone)
	}
}

// largeWarning is what the user reads before choosing.
func largeWarning(l LargeSweep) []string {
	subnet := l.Iface.Subnet.String()
	if l.Hosts > l.Largest {
		return []string{
			fmt.Sprintf("%s is on %s: %s addresses, more than a /16 (%s), the most shoal will sweep. Sweep the /24 around this machine, or only listen.", l.Iface.Name, subnet, thousands(l.Hosts), thousands(l.Largest)),
		}
	}
	return []string{
		fmt.Sprintf("%s is on %s: %s addresses. Shoal sweeps up to %s without asking. Sweeping all of this one means:", l.Iface.Name, subnet, thousands(l.Hosts), thousands(l.Limit)),
		fmt.Sprintf("Time. At %d requests a second one pass takes %s, and there is no retry pass, so a device that misses its one request is found only if it speaks. --rate sets the speed.", l.Rate, duration(l.Hosts, l.Rate)),
		fmt.Sprintf("Broadcast load. Every request is an Ethernet broadcast, so every device on this segment receives and handles all %s of them: switches, Dante and NDI gear, cameras, control processors, phones. Small embedded devices cope badly with a broadcast flood, and audio and clock traffic on a busy AV network can suffer while it runs.", thousands(l.Hosts)),
		"Neighbour tables. Every device that answers is then pinged and asked its name, which puts it in this machine's ARP table. On Linux that table holds 1,024 entries by default before the kernel starts throwing them away, so on a network with more devices than that, lookups stall or fail.",
		"Being noticed. A sweep this size looks like a network scan. Switch storm control or port security can throttle or shut the port this machine is on, and an intrusion detection system may raise an alert.",
		"The log keeps only the newest 2,000 events, so most of the sweep's requests scroll out of it.",
		"Only sweep a network you own or are authorised to test. If you go ahead, it is your call.",
	}
}

func (m *largeModal) choiceText(choice int) string {
	l := m.info
	switch choice {
	case largeAll:
		if !m.allowed(largeAll) {
			return fmt.Sprintf("sweep all %s addresses: refused, more than a /16", thousands(l.Hosts))
		}
		return fmt.Sprintf("sweep all %s addresses · %s at %d/s", thousands(l.Hosts), duration(l.Hosts, l.Rate), l.Rate)
	case largeLocal:
		n := netif.Interface{Subnet: l.Local}.Hosts()
		return fmt.Sprintf("sweep only %s around this machine · %s addresses", l.Local, thousands(n))
	default:
		return "don't sweep · listen only (ARP, mDNS, history)"
	}
}

func (a *app) largeModalOverlay() tideui.Overlay {
	m := a.large
	s := a.renderer.Styles
	width := min(76, max(20, a.width-4))
	inner := width - 4

	var choices []string
	for c := largeAll; c <= largeNone; c++ {
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
		tideui.SoftHint{Key: "esc", Label: "listen only"},
		tideui.SoftHint{Key: "pgdn", Label: "read on"},
	)

	var body []string
	for i, para := range largeWarning(m.info) {
		if i > 0 {
			body = append(body, "")
		}
		body = append(body, wrap(para, inner)...)
	}
	room := max(3, a.height-6-len(choices)-3)
	m.scroll.ClampTo(len(body), room)
	off := m.scroll.Offset()
	visible := append([]string(nil), body[off:min(len(body), off+room)]...)
	if off+room < len(body) {
		visible[len(visible)-1] = s.ItemMuted.Render(fit("… pgdn reads on", inner))
	}

	lines := append(visible, "")
	lines = append(lines, choices...)
	lines = append(lines, "", hints)
	return a.renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "shoal", Title: "large network", Content: a.renderer.RenderSoftBody(width, strings.Join(lines, "\n")), Width: width})
}

// duration is how long one pass over n addresses takes at rate a second,
// plus the second the sweep waits for late replies.
func duration(n, rate int) string {
	if rate <= 0 {
		rate = 1
	}
	d := time.Duration(n)*time.Second/time.Duration(rate) + time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("about %d s", int(d.Seconds()))
	case d%time.Minute < 5*time.Second:
		return fmt.Sprintf("about %d min", int(d.Minutes()))
	default:
		return fmt.Sprintf("about %d min %d s", int(d.Minutes()), int((d % time.Minute).Seconds()))
	}
}

// thousands writes n with commas, as a person would read it aloud.
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func prefixLen(n *net.IPNet) string {
	if n == nil {
		return "?"
	}
	ones, _ := n.Mask.Size()
	return fmt.Sprint(ones)
}
