package ui

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/store"
)

func candidate(t *testing.T, name, ip, cidr string) netif.Interface {
	t.Helper()
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return netif.Interface{Name: name, IP: net.ParseIP(ip).To4(), Subnet: subnet, MAC: net.HardwareAddr{0, 0, 0x5e, 0, 0x53, 1}}
}

// opener stands in for main's open: it records every call, and asks about
// any subnet larger than a /22 until told what to do.
type opener struct {
	mu      sync.Mutex
	calls   []SweepChoice
	engines []*engine.Engine
	note    string // the AccessNote each session carries
}

func (o *opener) open(iface netif.Interface, choice SweepChoice) (Session, *LargeSweep, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, choice)
	if choice == SweepAsk && iface.Hosts() > 1022 {
		return Session{}, &LargeSweep{Iface: iface, Hosts: iface.Hosts(), Limit: 1022, Largest: 65534, Rate: 500,
			Local: &net.IPNet{IP: iface.IP.Mask(net.CIDRMask(24, 32)), Mask: net.CIDRMask(24, 32)}}, nil
	}
	st := store.NewMemory()
	eng := engine.New(st, iface)
	o.engines = append(o.engines, eng)
	return Session{Store: st, Engine: eng, Iface: iface, Mode: "test on " + iface.Name, AccessNote: o.note}, nil, nil
}

func (o *opener) stopAll() {
	for _, e := range o.engines {
		e.Stop()
	}
}

func switchingApp(t *testing.T, list ...netif.Interface) (*app, *opener) {
	t.Helper()
	o := &opener{}
	a := newApp(Options{Iface: list[0], Theme: "nord", Open: o.open,
		Interfaces: func() ([]netif.Interface, error) { return list, nil }})
	a.ctx, a.send = context.Background(), func(tea.Msg) {}
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	t.Cleanup(o.stopAll)
	return a, o
}

// run executes a command and feeds its message back, as the program would.
func run(a *app, cmd tea.Cmd) {
	if cmd != nil {
		a.Update(cmd())
	}
}

func TestSwitchingInterfaceStartsAFreshSessionAndDropsTheOld(t *testing.T) {
	eth0 := candidate(t, "eth0", "172.16.10.5", "172.16.10.0/24")
	eth1 := candidate(t, "eth1", "198.51.100.7", "198.51.100.0/24")
	a, o := switchingApp(t, eth0, eth1)

	run(a, a.switchTo(eth0, SweepAsk))
	if a.opts.Engine == nil || !a.opts.Iface.Same(eth0) {
		t.Fatalf("first session not running: %+v", a.opts.Iface)
	}
	first := a.gen

	// i lists the interfaces; the picker opens on the one being scanned.
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	run(a, cmd)
	if !a.ifaces.open || a.ifaces.cursor != 0 {
		t.Fatalf("picker not open on the current interface: %+v", a.ifaces)
	}
	view := ansi.Strip(a.View())
	for _, want := range []string{"interfaces", "eth0", "eth1", "198.51.100.7/24", "· scanning"} {
		if !strings.Contains(view, want) {
			t.Errorf("picker is missing %q", want)
		}
	}

	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, cmd = a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(a, cmd)
	if !a.opts.Iface.Same(eth1) || a.opts.Engine == nil || a.gen <= first {
		t.Fatalf("did not switch to eth1: %+v gen %d", a.opts.Iface, a.gen)
	}
	if strings.Contains(ansi.Strip(a.View()), "172.16.10.0/24") {
		t.Error("the status bar still names the old subnet")
	}

	// A batch the old bridge had in flight must not bring the old network back.
	a.Update(Batch{Gen: first, Devices: seededStore(t).Devices()})
	if len(a.all) != 0 {
		t.Errorf("a stale batch put %d devices on screen", len(a.all))
	}
	a.Update(Batch{Gen: a.gen, Devices: seededStore(t).Devices()})
	if len(a.all) == 0 {
		t.Error("the current session's batch was dropped")
	}
	if got := len(o.calls); got != 2 {
		t.Errorf("open called %d times, want 2", got)
	}
}

func TestPickingTheCurrentInterfaceChangesNothing(t *testing.T) {
	eth0 := candidate(t, "eth0", "172.16.10.5", "172.16.10.0/24")
	a, o := switchingApp(t, eth0)
	run(a, a.switchTo(eth0, SweepAsk))
	gen := a.gen
	a.openIfaces(ifacesMsg{list: []netif.Interface{eth0}})
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || a.gen != gen || len(o.calls) != 1 {
		t.Errorf("choosing the interface already scanned restarted it")
	}
}

func TestLargeSubnetWarnsBeforeSweeping(t *testing.T) {
	big := candidate(t, "eth0", "172.16.10.5", "172.16.0.0/16")
	a, o := switchingApp(t, big)
	run(a, a.switchTo(big, SweepAsk))
	if a.large == nil || a.opts.Engine != nil {
		t.Fatal("a /16 should be asked about before anything runs")
	}
	view := ansi.Strip(a.View())
	for _, want := range []string{"large network", "65,534 addresses", "Broadcast load", "sweep all 65,534 addresses", "about 2 min 12 s at 500/s", "sweep only 172.16.10.0/24", "listen only"} {
		if !strings.Contains(view, want) {
			t.Errorf("warning is missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "waiting") {
		t.Error("the status bar should say the scan is waiting")
	}

	// The highlight starts on the /24: Enter alone never sweeps the /16.
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(a, cmd)
	if got := o.calls[len(o.calls)-1]; got != SweepLocal {
		t.Errorf("Enter chose %v, want SweepLocal", got)
	}

	// Asking again and going up to "all" sweeps the whole subnet.
	run(a, a.switchTo(big, SweepAsk))
	a.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, cmd = a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(a, cmd)
	if got := o.calls[len(o.calls)-1]; got != SweepAll || a.opts.Engine == nil {
		t.Errorf("chose %v, want SweepAll and a running session", got)
	}

	// Esc listens only.
	run(a, a.switchTo(big, SweepAsk))
	_, cmd = a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	run(a, cmd)
	if got := o.calls[len(o.calls)-1]; got != SweepNone {
		t.Errorf("Esc chose %v, want SweepNone", got)
	}
}

func TestAboveASlash16OnlyThePartsCanBeChosen(t *testing.T) {
	huge := candidate(t, "eth0", "10.20.30.40", "10.0.0.0/8")
	a, o := switchingApp(t, huge)
	run(a, a.switchTo(huge, SweepAsk))
	if !strings.Contains(ansi.Strip(a.View()), "refused, more than a /16") {
		t.Error("a /8 should say the whole sweep is refused")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyUp}) // cannot reach "all"
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(a, cmd)
	if got := o.calls[len(o.calls)-1]; got != SweepLocal {
		t.Errorf("chose %v, want SweepLocal", got)
	}
}

func TestTheDemoHasNoInterfacePicker(t *testing.T) {
	a, _ := sized(t, 150, 40)
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if cmd != nil || a.ifaces.open {
		t.Error("i should do nothing in the demo")
	}
	if strings.Contains(ansi.Strip(a.View()), "i iface") {
		t.Error("the demo's key bar should not offer i")
	}
}

func TestDuration(t *testing.T) {
	cases := map[[2]int]string{
		{254, 100}:    "about 3 s",
		{65534, 500}:  "about 2 min 12 s",
		{65534, 100}:  "about 10 min 56 s",
		{11999, 100}:  "about 2 min",
		{1000000, 50}: "about 333 min 21 s",
	}
	for in, want := range cases {
		if got := duration(in[0], in[1]); got != want {
			t.Errorf("duration(%d, %d) = %q, want %q", in[0], in[1], got, want)
		}
	}
	if got := thousands(65534); got != "65,534" {
		t.Errorf("thousands = %q", got)
	}
}

func TestASessionWithoutRawAccessSaysWhyInTheLog(t *testing.T) {
	eth0 := candidate(t, "eth0", "172.16.10.5", "172.16.10.0/24")
	a, o := switchingApp(t, eth0)
	o.note = "Npcap is not installed, so shoal reads Windows' neighbour table instead of sweeping with ARP."

	run(a, a.switchTo(eth0, SweepAsk))
	var found bool
	for _, ev := range a.events {
		if ev.Message == o.note {
			found = true
			if ev.Probe != "arp" || ev.Kind != engine.KindInfo {
				t.Errorf("the note is logged as %s/%s, want arp/info: it explains why arp is not running", ev.Probe, ev.Kind)
			}
		}
	}
	if !found {
		t.Fatal("the session's access note is not in the log")
	}
	// The log wraps the selected event in full; the note must be readable
	// there, its first words and its last, not cut off at the pane's edge.
	a.focus = paneHood
	a.Update(tea.KeyMsg{Type: tea.KeyUp})
	view := ansi.Strip(a.View())
	for _, want := range []string{"Npcap is not installed", "sweeping with ARP."} {
		if !strings.Contains(view, want) {
			t.Errorf("the note cannot be read in full in the log: %q is missing", want)
		}
	}

	// A session with nothing to say adds nothing.
	before := len(a.events)
	o.note = ""
	run(a, a.switchTo(eth0, SweepAsk))
	for _, ev := range a.events[before:] {
		if ev.Probe == "arp" {
			t.Errorf("an empty note was logged: %+v", ev)
		}
	}
}
