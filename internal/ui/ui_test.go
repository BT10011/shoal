package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/probe/av"
	"github.com/BT10011/shoal/internal/probe/fake"
	"github.com/BT10011/shoal/internal/probe/history"
	"github.com/BT10011/shoal/internal/probe/oui"
	"github.com/BT10011/shoal/internal/probe/rogue"
	"github.com/BT10011/shoal/internal/store"
)

var t0 = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

func obs(key string, f model.Field, value, source string, conf float32) model.Observation {
	return model.Observation{DeviceKey: key, Field: f, Value: value, Source: source, Method: source + " said " + value, Confidence: conf, At: t0}
}

func seededStore(t *testing.T) *store.Memory {
	t.Helper()
	st := store.NewMemory(store.WithClock(func() time.Time { return t0 }))
	for _, o := range []model.Observation{
		obs("mac-b", model.FieldMAC, "00:11:32:7f:a2:c4", "arp", 1),
		obs("mac-b", model.FieldIP, "192.168.1.20", "arp", 1),
		obs("mac-b", model.FieldHostname, "synology.local", "mdns", 0.9),
		obs("mac-b", model.FieldHostname, "nas.lan", "rdns", 0.7),
		obs("mac-b", model.FieldVendor, "Synology Inc.", "oui", 0.9),
		obs("mac-b", model.FieldService, "_smb._tcp", "mdns", 0.9),
		obs("mac-a", model.FieldMAC, "2c:c8:1b:4a:10:01", "arp", 1),
		obs("mac-a", model.FieldIP, "192.168.1.1", "arp", 1),
		obs("mac-c", model.FieldMAC, "aa:aa:aa:aa:aa:aa", "arp", 1),
	} {
		if err := st.Apply(o); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func sized(t *testing.T, w, h int) (*app, *store.Memory) {
	t.Helper()
	st := seededStore(t)
	a := newApp(Options{Store: st, Engine: engine.New(st, netif.Interface{}), Demo: true, Theme: "catppuccin-mocha"})
	a.Update(tea.WindowSizeMsg{Width: w, Height: h})
	a.Update(Batch{
		Devices: st.Devices(),
		Events: []engine.ProbeEvent{
			{Probe: "arp", Kind: engine.KindSent, Message: "who-has 192.168.1.20", At: t0},
			{Probe: "arp", Kind: engine.KindReceived, Message: "192.168.1.20 is-at 00:11:32:7f:a2:c4", At: t0},
			{Probe: "store", Kind: engine.KindInfo, Message: "hostname conflict", At: t0},
		},
		Status: engine.Status{
			Scan: 1, ScanStarted: t0,
			Discoverers: []engine.DiscovererStatus{{Name: "arp", State: engine.StateRunning, Done: 20, Total: 254}},
			Enrichers:   []engine.EnricherStatus{{Name: "rdns", Produces: model.FieldHostname, Running: 1, Queued: 2, Completed: 3, Asked: 4, Answered: 2}},
		},
		At: t0.Add(5 * time.Second),
	})
	return a, st
}

func keys(devs []model.DeviceSnapshot) []string {
	out := make([]string, len(devs))
	for i, d := range devs {
		out[i] = d.Key
	}
	return out
}

func TestSortByIPPutsAddresslessLast(t *testing.T) {
	st := seededStore(t)
	if got := keys(sortDevices(st.Devices(), sortState{}, t0)); strings.Join(got, " ") != "mac-a mac-b mac-c" {
		t.Fatalf("order = %v", got)
	}
}

func TestSortByColumnAndReverse(t *testing.T) {
	st := seededStore(t)
	for _, o := range []model.Observation{
		obs("mac-a", model.FieldLatency, "10.5ms", "icmp", 1),
		obs("mac-b", model.FieldLatency, "2ms", "icmp", 1),
		obs("mac-c", model.FieldHostname, "alpha", "mdns", 0.9),
	} {
		if err := st.Apply(o); err != nil {
			t.Fatal(err)
		}
	}
	col := func(f model.Field) int {
		for i, c := range columns {
			if c.field == f {
				return i
			}
		}
		t.Fatalf("no column for %s", f)
		return -1
	}
	cases := []struct {
		name string
		st   sortState
		want string
	}{
		{"ip", sortState{col: col(model.FieldIP)}, "mac-a mac-b mac-c"},
		{"ip reversed keeps addressless last", sortState{col: col(model.FieldIP), reverse: true}, "mac-b mac-a mac-c"},
		{"hostname", sortState{col: col(model.FieldHostname)}, "mac-c mac-b mac-a"},
		{"hostname reversed", sortState{col: col(model.FieldHostname), reverse: true}, "mac-b mac-c mac-a"},
		{"rtt numeric not lexical", sortState{col: col(model.FieldLatency)}, "mac-b mac-a mac-c"},
		{"rtt reversed", sortState{col: col(model.FieldLatency), reverse: true}, "mac-a mac-b mac-c"},
	}
	for _, c := range cases {
		if got := strings.Join(keys(sortDevices(st.Devices(), c.st, t0)), " "); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}

	a, _ := sized(t, 120, 40)
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}}) // MAC is dropped at this width; HOSTNAME is next
	if a.sort.col != 2 || !strings.Contains(ansi.Strip(a.View()), "HOSTNAME ▾") {
		t.Fatalf("s should sort by the next column and mark the header; sort=%+v\n%s", a.sort, ansi.Strip(a.View()))
	}
	if got := strings.Join(keys(a.devices), " "); got != "mac-b mac-a mac-c" {
		t.Fatalf("table order by hostname = %q", got)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
	if !a.sort.reverse || !strings.Contains(ansi.Strip(a.View()), "HOSTNAME ▴") {
		t.Fatal("S should reverse and flip the arrow")
	}
}

func TestFilterMatchesValuesSubstringOrGlob(t *testing.T) {
	st := seededStore(t)
	match := func(pattern string) string {
		f := filter{pattern: pattern}
		var got []string
		for _, d := range st.Devices() {
			if f.matches(d, t0) {
				got = append(got, d.Key)
			}
		}
		return strings.Join(got, " ")
	}
	cases := map[string]string{
		"":             "mac-a mac-b mac-c",
		"SYN":          "mac-b", // case-insensitive, hostname
		"_smb":         "mac-b", // service value
		"Synology Inc": "mac-b", // vendor
		"192.168.1.":   "mac-a mac-b",
		"192.168.1.*":  "mac-a mac-b", // glob
		"*.1":          "mac-a",       // glob must match the whole value
		"mac-c":        "mac-c",       // the key itself
		"nomatch":      "",
	}
	for pattern, want := range cases {
		if got := match(pattern); got != want {
			t.Errorf("%q: got %q want %q", pattern, got, want)
		}
	}
}

func TestFilterKeysNarrowLiveThenKeepOrClear(t *testing.T) {
	a, _ := sized(t, 120, 40)
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !a.filter.editing {
		t.Fatal("/ should start editing")
	}
	for _, r := range "syn" {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(a.devices) != 1 || a.selected != "mac-b" {
		t.Fatalf("live filter: %v selected %q", keys(a.devices), a.selected)
	}
	plain := ansi.Strip(a.View())
	for _, want := range []string{"/syn", "1 of 3 match", "⏎ keep", "esc clear"} {
		if !strings.Contains(plain, want) {
			t.Errorf("editing view missing %q\n%s", want, plain)
		}
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}) // typed into the filter, not quit
	if a.filter.pattern != "synq" {
		t.Fatalf("pattern = %q", a.filter.pattern)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.filter.editing || !a.filter.active() || len(a.devices) != 1 {
		t.Fatalf("enter should keep the filter: editing=%v pattern=%q shown=%d", a.filter.editing, a.filter.pattern, len(a.devices))
	}
	plain = ansi.Strip(a.View())
	if !strings.Contains(plain, "/syn  1 of 3") || !strings.Contains(plain, "Devices") || !strings.Contains(plain, " 1/3") {
		t.Errorf("kept filter should show in the status bar and pane hint\n%s", plain)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.filter.active() || len(a.devices) != 3 {
		t.Fatal("esc should clear the filter")
	}

	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "zzz" {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !strings.Contains(ansi.Strip(a.View()), "nothing matches zzz") {
		t.Error("an empty result should say so")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.filter.editing || a.filter.active() {
		t.Fatal("esc while editing clears and closes")
	}
}

func TestBridgeCoalesces(t *testing.T) {
	st := store.NewMemory()
	eng := engine.New(st, netif.Interface{})
	var sent []tea.Msg
	b := newBridge(st, eng, func(m tea.Msg) { sent = append(sent, m) }, time.Hour)

	if _, ok := b.flush(); ok {
		t.Fatal("nothing happened yet; flush should be empty")
	}
	for _, o := range []model.Observation{
		obs("k", model.FieldHostname, "a", "mdns", 0.9),
		obs("k", model.FieldHostname, "b", "rdns", 0.8),
	} {
		if err := st.Apply(o); err != nil {
			t.Fatal(err)
		}
	}
	batch, ok := b.flush()
	if !ok || len(batch.Devices) != 1 {
		t.Fatalf("batch = %+v ok=%v", batch, ok)
	}
	if len(batch.Events) != 1 || batch.Events[0].Probe != "store" {
		t.Fatalf("expected the conflict event only, got %+v", batch.Events)
	}
	if _, ok := b.flush(); ok {
		t.Fatal("second flush with no changes should be empty")
	}

	b.onProbe(engine.ProbeEvent{Probe: "arp", Kind: engine.KindSent})
	batch, ok = b.flush()
	if !ok || batch.Devices != nil || len(batch.Events) != 1 {
		t.Fatalf("probe-only flush = %+v ok=%v (Devices must be nil when unchanged)", batch, ok)
	}
	if len(sent) != 0 {
		t.Fatal("flush must not send; run does")
	}
}

func TestBridgeDropsOldestWhenBacklogged(t *testing.T) {
	st := store.NewMemory()
	b := newBridge(st, engine.New(st, netif.Interface{}), func(tea.Msg) {}, time.Hour)
	for i := 0; i < maxPending+10; i++ {
		b.onProbe(engine.ProbeEvent{Probe: "arp", Done: i})
	}
	batch, _ := b.flush()
	if len(batch.Events) != maxPending || batch.Events[0].Done != 10 {
		t.Fatalf("kept %d events starting at %d", len(batch.Events), batch.Events[0].Done)
	}
}

func TestViewFitsTerminalExactly(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 18}, {40, 12}} {
		a, _ := sized(t, size[0], size[1])
		view := a.View()
		lines := strings.Split(view, "\n")
		if len(lines) != size[1] {
			t.Errorf("%v: %d lines", size, len(lines))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w != size[0] {
				t.Errorf("%v: line %d is %d wide: %q", size, i, w, ansi.Strip(l))
			}
		}
	}
}

func TestViewShowsDevicesDetailsAndLog(t *testing.T) {
	a, _ := sized(t, 120, 40)
	a.Update(tea.KeyMsg{Type: tea.KeyDown}) // select 192.168.1.20
	plain := ansi.Strip(a.View())
	for _, want := range []string{
		"Devices", "Details", "Under the hood",
		"192.168.1.1", "192.168.1.20", "synology.local !", "Synology Inc.",
		"mdns said synology.local", "5s ago",
		"arp    ", "20/254", "running", "rdns   4 asked · 2 named · 1 running",
		"who-has 192.168.1.20", "hostname conflict",
		"DEMO", "3 devices", "q quit",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("view missing %q\n%s", want, plain)
		}
	}
	details := ansi.Strip(joinLines(a.renderDetails(80)))
	for _, want := range []string{"nas.lan  (disagrees)", "rdns · conf 0.7", "conf 0.9", "heard from directly 5s ago via arp, since scan 1 began"} {
		if !strings.Contains(details, want) {
			t.Errorf("details missing %q\n%s", want, details)
		}
	}
}

func TestKeysMoveSelectionAndQuit(t *testing.T) {
	a, _ := sized(t, 100, 30)
	if a.selected != "mac-a" {
		t.Fatalf("initial selection = %q", a.selected)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.selected != "mac-b" {
		t.Fatalf("after down = %q", a.selected)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if a.selected != "mac-c" {
		t.Fatalf("after G = %q", a.selected)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if a.selected != "mac-b" {
		t.Fatalf("after k = %q", a.selected)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if a.selected != "mac-a" || a.cursor != 0 {
		t.Fatalf("after g = %q cursor %d", a.selected, a.cursor)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyUp})
	if a.cursor != 0 {
		t.Fatal("up at top should clamp")
	}

	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q should quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q produced %T, want tea.QuitMsg", cmd())
	}
}

func TestSelectionSurvivesReorder(t *testing.T) {
	a, st := sized(t, 100, 30)
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.selected != "mac-b" {
		t.Fatal("setup")
	}
	if err := st.Apply(obs("mac-c", model.FieldIP, "192.168.1.5", "arp", 1)); err != nil {
		t.Fatal(err)
	}
	a.Update(Batch{Devices: st.Devices(), At: t0})
	if a.selected != "mac-b" || a.devices[a.cursor].Key != "mac-b" {
		t.Fatalf("selection lost: %q at cursor %d", a.selected, a.cursor)
	}
	if a.cursor != 2 {
		t.Fatalf("mac-b should now be third (after .1 and .5), cursor = %d", a.cursor)
	}
}

func TestTabScrollsDetailsInsteadOfTable(t *testing.T) {
	a, _ := sized(t, 100, 30)
	a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.cursor != 0 {
		t.Fatal("down with details focused must not move the table cursor")
	}
	if a.details.Offset() != 1 {
		t.Fatalf("details offset = %d", a.details.Offset())
	}
	a.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.cursor != 1 || a.details.Offset() != 0 {
		t.Fatalf("switching device should reset details scroll: cursor=%d offset=%d", a.cursor, a.details.Offset())
	}
}

func TestLogPinsSelectsAndExpands(t *testing.T) {
	a, _ := sized(t, 120, 40)
	long := strings.Repeat("PTR answer from 192.168.1.20 with a very long message ", 3)
	for i := 0; i < 30; i++ {
		a.Update(Batch{Events: []engine.ProbeEvent{{Probe: "arp", Kind: engine.KindSent, Target: "10.0.0.1", Message: fmt.Sprintf("who-has 10.0.0.%d", i), At: t0.Add(time.Duration(i) * time.Second)}}, At: t0})
	}
	a.Update(Batch{Events: []engine.ProbeEvent{{Probe: "mdns", Kind: engine.KindReceived, Target: "192.168.1.20", Message: long, At: t0.Add(31 * time.Second)}}, At: t0})
	if !a.log.follow {
		t.Fatal("the log follows by default")
	}
	plain := ansi.Strip(a.View())
	if !strings.Contains(plain, "who-has 10.0.0.29") || strings.Contains(plain, "who-has 10.0.0.0\n") {
		t.Errorf("following should show the tail\n%s", plain)
	}
	if !strings.Contains(plain, "…") {
		t.Errorf("a long line is cut off while following\n%s", plain)
	}

	a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a.Update(tea.KeyMsg{Type: tea.KeyTab})
	if a.focus != paneHood {
		t.Fatal("the log pane must take focus in the three-pane layout too")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyUp})
	if a.log.follow || a.log.cursor != len(a.events)-1 {
		t.Fatalf("first step up pins the newest event: follow=%v cursor=%d", a.log.follow, a.log.cursor)
	}
	plain = ansi.Strip(a.View())
	for _, want := range []string{"pinned at 12:00:31 · G follows again", "received from 192.168.1.20"} {
		if !strings.Contains(plain, want) {
			t.Errorf("pinned view missing %q\n%s", want, plain)
		}
	}
	expanded := strings.Join(strings.Fields(ansi.Strip(strings.Join(a.renderSelectedEvent(a.events[len(a.events)-1], 51), " "))), " ")
	if !strings.Contains(expanded, strings.Join(strings.Fields(long), " ")) {
		t.Errorf("the selected event must be shown in full, wrapped:\n%s", expanded)
	}
	if strings.Contains(plain, "long message …") {
		t.Errorf("the selected event must not be truncated\n%s", plain)
	}
	if a.cursor != 0 {
		t.Fatal("log keys must not move the device table")
	}

	for i := 0; i < 5; i++ {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	}
	if a.log.cursor != len(a.events)-6 {
		t.Fatalf("cursor = %d", a.log.cursor)
	}
	plain = ansi.Strip(a.View())
	if !strings.Contains(plain, "sent to 10.0.0.1") || !strings.Contains(plain, "who-has 10.0.0.25") {
		t.Errorf("selected sent event should be described\n%s", plain)
	}
	// New events arrive while pinned: the selection stays put.
	a.Update(Batch{Events: []engine.ProbeEvent{{Probe: "arp", Kind: engine.KindSent, Message: "who-has 10.0.0.99", At: t0.Add(time.Minute)}}, At: t0})
	if a.log.follow || a.events[a.log.cursor].Message != "who-has 10.0.0.25" {
		t.Fatalf("selection moved: follow=%v now on %q", a.log.follow, a.events[a.log.cursor].Message)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if a.log.cursor != 0 || !strings.Contains(ansi.Strip(a.View()), "who-has 10.0.0.0") {
		t.Fatal("g goes to the oldest event")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if a.log.cursor == 0 {
		t.Fatal("pgdown pages")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if !a.log.follow {
		t.Fatal("G follows again")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyUp})
	a.Update(tea.KeyMsg{Type: tea.KeyUp})
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if !a.log.follow {
		t.Fatal("stepping down onto the newest event follows again")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.focus != paneDevices {
		t.Fatal("esc returns to the table")
	}
}

func TestLogSelectionSurvivesTrimming(t *testing.T) {
	a, _ := sized(t, 100, 30)
	var events []engine.ProbeEvent
	for i := 0; i < logKeep; i++ {
		events = append(events, engine.ProbeEvent{Probe: "arp", Kind: engine.KindSent, Message: fmt.Sprintf("e%d", i), At: t0})
	}
	a.Update(Batch{Events: events, At: t0})
	a.focus = paneHood
	a.Update(tea.KeyMsg{Type: tea.KeyUp})
	for i := 0; i < 10; i++ {
		a.Update(tea.KeyMsg{Type: tea.KeyUp})
	}
	want := a.events[a.log.cursor].Message
	a.Update(Batch{Events: events[:100], At: t0}) // pushes 100 old events out
	if len(a.events) != logKeep || a.events[a.log.cursor].Message != want {
		t.Fatalf("selection drifted from %q to %q", want, a.events[a.log.cursor].Message)
	}
}

func TestListenerRowRollsAWaveAndCountsWhatItHeard(t *testing.T) {
	a, _ := sized(t, 120, 40)
	listening := engine.DiscovererStatus{Name: "mdns", State: engine.StateRunning, Done: 57, Message: "listening"}
	a.status = engine.Status{Scan: 1, ScanStarted: t0, Discoverers: []engine.DiscovererStatus{
		{Name: "arp", State: engine.StateDone, Done: 428, Total: 428}, listening}}
	plain := ansi.Strip(a.View())
	if !strings.Contains(plain, "listening · 57 heard") || strings.Contains(plain, "57 running") {
		t.Errorf("listener row should say what it heard\n%s", plain)
	}
	row := ""
	for _, l := range strings.Split(plain, "\n") {
		if strings.Contains(l, "listening · 57 heard") {
			row = l
		}
	}
	if !strings.ContainsAny(row, strings.Join(waveGlyphs, "")) {
		t.Errorf("listener row should carry the wave: %q", row)
	}
	if strings.Contains(plain, "428/428 done") && !strings.Contains(plain, "⣿") {
		t.Errorf("the sweep keeps its bar\n%s", plain)
	}

	// The wave rolls as time passes, even with nothing new heard.
	before := ansi.Strip(listenWave(30, a.waveFrame(), true, a.barStyles()))
	a.now = a.now.Add(waveEvery)
	after := ansi.Strip(listenWave(30, a.waveFrame(), true, a.barStyles()))
	if before == after || lipgloss.Width(before) != 30 || lipgloss.Width(after) != 30 {
		t.Errorf("wave should advance one step per tick: %q then %q", before, after)
	}
	if before[3:] != after[:len(after)-3] && before[:len(before)-3] != after[3:] {
		t.Errorf("wave should travel, not scramble: %q then %q", before, after)
	}
	if cmd := a.tick(); cmd == nil {
		t.Fatal("tick")
	}

	// Stopped: a flat line and the count it reached.
	listening.State = engine.StateCancelled
	a.status.Discoverers[1] = listening
	plain = ansi.Strip(a.View())
	if !strings.Contains(plain, "stopped · 57 heard") || !strings.Contains(plain, strings.Repeat(waveFlat, 10)) {
		t.Errorf("stopped listener should go flat\n%s", plain)
	}
	if got := ansi.Strip(listenWave(8, 5, true, barStyles{plain: true})); lipgloss.Width(got) != 8 || strings.ContainsAny(got, "⣀⠉") {
		t.Errorf("plain themes get an ASCII wave: %q", got)
	}

	// A sweep that has not announced its total yet is not a listener.
	if isListener(engine.DiscovererStatus{Name: "arp", State: engine.StateRunning, Message: "sweeping 10.0.0.0/24"}) {
		t.Error("a sweep without a total yet must not be drawn as a listener")
	}
}

func TestFlagsColumnShowsBadgesAndFiltersByFlag(t *testing.T) {
	a, st := sized(t, 120, 40)
	for _, o := range []model.Observation{
		obs("mac-c", model.FieldIP, "169.254.37.12", "arp", 0.9),
		obs("mac-c", model.FieldFlag, "link-local-ip", "rogue", 1),
		obs("mac-a", model.FieldFlag, "this-host", "netif", 1),
		obs("mac-a", model.FieldFlag, "locally-administered-mac", "oui", 1),
	} {
		if err := st.Apply(o); err != nil {
			t.Fatal(err)
		}
	}
	a.Update(Batch{Devices: st.Devices(), At: t0})
	plain := ansi.Strip(a.View())
	if !strings.Contains(plain, "FLAGS") {
		t.Fatalf("no FLAGS column\n%s", plain)
	}
	// Table rows sit in the left pane; the details pane shares the lines.
	row := func(ip string) string {
		for _, l := range strings.Split(plain, "\n") {
			left := ansi.Truncate(l, 72, "")
			if strings.Contains(left, "  "+ip+" ") {
				return left
			}
		}
		return ""
	}
	if r := row("169.254.37.12"); !strings.Contains(r, "link-local") {
		t.Errorf("link-local row should carry its badge: %q", r)
	}
	if r := row("192.168.1.1"); !strings.Contains(r, "self,rand") {
		t.Errorf("host row should carry self and rand-mac badges: %q", r)
	}
	if !strings.Contains(plain, "self,rand-mac ") {
		t.Errorf("two badges should fit whole at 120 columns\n%s", plain)
	}

	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "link-local" {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(a.devices) != 1 || a.devices[0].Key != "mac-c" {
		t.Fatalf("/link-local should leave the flagged device: %v", keys(a.devices))
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	details := ansi.Strip(joinLines(a.renderDetails(100)))
	if !strings.Contains(details, "link-local-ip") || !strings.Contains(details, "rogue said link-local-ip") {
		t.Errorf("details should show the flag with its provenance\n%s", details)
	}
}

func TestBadgeText(t *testing.T) {
	if got := badgeText([]string{"locally-administered-mac", "this-host", "duplicate-ip", "custom-flag", "link-local-ip"}); got != "link-local,dup-ip,self,rand-mac,custom-flag" {
		t.Fatalf("badges = %q", got)
	}
	if got := badgeText(nil); got != "" {
		t.Fatalf("no flags = %q", got)
	}
}

func TestFitBadgesKeepsWholeBadges(t *testing.T) {
	cases := []struct {
		text  string
		width int
		want  string
	}{
		{"off-subnet,dante,new", 20, "off-subnet,dante,new"},
		{"off-subnet,dante,new", 19, "off-subnet,dante +1"},
		{"off-subnet,dante,new", 16, "off-subnet +2"},
		{"link-local,ndi,new", 16, "link-local,ndi +1"[:0] + "link-local +2"},
		{"dup-ip", 16, "dup-ip"},
		{"", 16, ""},
	}
	for _, c := range cases {
		if got := fitBadges(c.text, c.width); got != c.want {
			t.Errorf("fitBadges(%q, %d) = %q, want %q", c.text, c.width, got, c.want)
		}
	}
}

func TestLayerTwoOnlyDeviceIsExplained(t *testing.T) {
	a, _ := sized(t, 120, 40)
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}}) // mac-c: MAC only
	details := ansi.Strip(joinLines(a.renderDetails(100)))
	if !strings.Contains(details, "seen at layer 2 only") {
		t.Errorf("a device with no address should say so\n%s", details)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if details := ansi.Strip(joinLines(a.renderDetails(100))); strings.Contains(details, "layer 2 only") {
		t.Error("a device with an address must not")
	}
}

func TestEnricherRowsCountAskedAgainstAnswered(t *testing.T) {
	a, _ := sized(t, 120, 40)
	row := func(e engine.EnricherStatus) string { return ansi.Strip(a.renderEnricher(e, 60)) }
	cases := []struct {
		e    engine.EnricherStatus
		want string
	}{
		{engine.EnricherStatus{Name: "oui", Produces: model.FieldVendor, Completed: 23, Asked: 23, Answered: 21}, "oui    23 asked · 21 named"},
		{engine.EnricherStatus{Name: "rdns", Produces: model.FieldHostname, Completed: 23, Asked: 23, Answered: 2}, "rdns   23 asked · 2 named"},
		{engine.EnricherStatus{Name: "icmp", Produces: model.FieldLatency, Completed: 23, Asked: 23, Answered: 21}, "icmp   23 asked · 21 answered"},
		{engine.EnricherStatus{Name: "rogue", Produces: model.FieldFlag, Completed: 23, Asked: 23, Answered: 1}, "rogue  23 asked · 1 flagged"},
		{engine.EnricherStatus{Name: "nbns", Produces: model.FieldHostname, Running: 4, Queued: 9, Completed: 10, Failed: 1, Asked: 15, Answered: 3}, "nbns   15 asked · 3 named · 4 running · 9 queued · 1 failed"},
		{engine.EnricherStatus{Name: "x"}, "x      0 asked · 0 answered"},
		{engine.EnricherStatus{Name: "mdns", Produces: model.FieldHostname, Completed: 23, Asked: 23, Answered: 9, Refreshing: 2, Renewed: 14}, "mdns   23 asked · 9 named · 14 renewed"},
	}
	for _, c := range cases {
		if got := strings.TrimRight(row(c.e), " "); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
	if strings.Contains(row(cases[1].e), "done") {
		t.Error("the old 'done' wording should be gone")
	}
}

func TestSweepAndItsListenerReadAsOneProbe(t *testing.T) {
	a, _ := sized(t, 120, 40)
	sweep := engine.DiscovererStatus{Name: "arp", State: engine.StateRunning, Done: 120, Total: 253}
	listen := engine.DiscovererStatus{Name: "arp", State: engine.StateRunning, Message: "listening for ARP on eth0"}
	mdns := engine.DiscovererStatus{Name: "mdns", State: engine.StateRunning, Done: 9, Message: "listening"}
	a.status = engine.Status{Scan: 1, ScanStarted: t0, Discoverers: []engine.DiscovererStatus{sweep, listen, mdns}}

	rows := func() []string {
		var out []string
		for _, l := range a.renderHood(56, 10)[:3] {
			out = append(out, ansi.Strip(l))
		}
		return out
	}
	r := rows()
	if !strings.HasPrefix(r[0], "arp ") || !strings.HasPrefix(r[1], "  └") || strings.HasPrefix(r[1], "arp") {
		t.Fatalf("the listener should hang under the sweep, not repeat its name:\n%s", strings.Join(r, "\n"))
	}
	if !strings.Contains(r[1], "listens once the sweep ends") || !strings.Contains(r[1], strings.Repeat(waveFlat, 5)) {
		t.Errorf("while the sweep runs the listener waits, flat:\n%s", r[1])
	}
	if !strings.HasPrefix(r[2], "mdns ") || !strings.Contains(r[2], "listening · 9 heard") {
		t.Errorf("a listener with no sweep keeps its own name:\n%s", r[2])
	}

	a.status.Discoverers[0].State, a.status.Discoverers[0].Done = engine.StateDone, 253
	a.status.Discoverers[1].Done = 14
	r = rows()
	if !strings.Contains(r[0], "253/253 done") || !strings.Contains(r[1], "still listening · 14 heard") || strings.Contains(r[1], strings.Repeat(waveFlat, 8)) {
		t.Errorf("after the sweep the listener carries on, rolling:\n%s", strings.Join(r, "\n"))
	}
}

func TestStopIsInEveryKeyBarAndReadsStopped(t *testing.T) {
	a, _ := sized(t, 80, 24)
	bar := ansi.Strip(a.View())
	if !strings.Contains(bar, "c stop") {
		t.Errorf("stop must survive even at 80 columns: %q", bar[strings.LastIndex(bar, "\n")+1:])
	}
	a.status = engine.Status{Scan: 1, ScanStarted: t0,
		Discoverers: []engine.DiscovererStatus{{Name: "arp", State: engine.StateCancelled, Total: 254, Done: 40}}}
	plain := ansi.Strip(a.View())
	if !strings.Contains(plain, "scan 1 stopped") || !strings.Contains(plain, "40/254 stopped") || strings.Contains(plain, "cancelled") {
		t.Errorf("a stopped scan should read as stopped everywhere\n%s", plain)
	}
}

func TestLayoutColumnsNeverExceedWidth(t *testing.T) {
	for w := 10; w <= 160; w++ {
		cols, widths := layoutColumns(w, "")
		total := len(cols) - 1
		for _, cw := range widths {
			total += cw
		}
		if total > w {
			t.Fatalf("width %d: columns need %d", w, total)
		}
		if len(cols) == 0 {
			t.Fatalf("width %d: no columns", w)
		}
	}
	cols, _ := layoutColumns(200, "")
	if len(cols) != len(columns) {
		t.Fatalf("wide table dropped columns: %v", cols)
	}
}

func TestProgressBar(t *testing.T) {
	isFilled := func(r rune, plain bool) bool {
		if plain {
			return r == ':' || r == ';'
		}
		return r >= 0x2800 && r <= 0x28ff && !strings.ContainsRune(strings.Join(dotHead, ""), r)
	}
	bar := func(done, total, width int, plain bool) string {
		return ansi.Strip(progressBar(done, total, width, done, barStyles{plain: plain}))
	}
	for _, plain := range []bool{false, true} {
		if got := bar(0, 0, 4, plain); got != "    " {
			t.Fatalf("plain=%v zero total should be blank: %q", plain, got)
		}
		if got := bar(20, 10, 4, plain); lipgloss.Width(got) != 4 {
			t.Fatalf("plain=%v overflow width: %q", plain, got)
		}
		prev := -1
		for done := 0; done <= 254; done++ {
			b := bar(done, 254, 30, plain)
			if w := lipgloss.Width(b); w != 30 {
				t.Fatalf("plain=%v done=%d: width %d: %q", plain, done, w, b)
			}
			if strings.ContainsAny(b, "·.") {
				t.Fatalf("plain=%v done=%d: empty cells must be blank: %q", plain, done, b)
			}
			n := 0
			for _, r := range b {
				if isFilled(r, plain) {
					n++
				}
			}
			if n < prev {
				t.Fatalf("plain=%v done=%d: fill went backwards (%d -> %d): %q", plain, done, prev, n, b)
			}
			prev = n
		}
		full := strings.Repeat("⣿", 30)
		if plain {
			full = strings.Repeat(":", 30)
		}
		if got := bar(254, 254, 30, plain); got != full {
			t.Fatalf("plain=%v: complete bar must be solid, got %q", plain, got)
		}
	}
	same := func(frame int) string {
		return ansi.Strip(progressBar(100, 254, 30, frame, barStyles{}))
	}
	if same(7) != same(7) {
		t.Fatal("same frame must render identically")
	}
	if same(7) == same(8) {
		t.Fatal("texture should shimmer between frames")
	}
	distinct := map[rune]bool{}
	for _, r := range bar(200, 254, 30, false) {
		distinct[r] = true
	}
	if len(distinct) < 3 {
		t.Fatalf("running bar should vary in texture, got %d distinct glyphs", len(distinct))
	}
}

func TestSparkAgeFlaresThenFades(t *testing.T) {
	flares, bright := 0, 0
	for cell := 0; cell < 30; cell++ {
		for frame := 0; frame < 600; frame++ {
			switch age := sparkAge(cell, frame); {
			case age == 0:
				bright++
				if frame > 0 && sparkAge(cell, frame-1) == -1 {
					flares++
					if sparkAge(cell, frame+1) != 0 || sparkAge(cell, frame+2) != 1 || sparkAge(cell, frame+3) != 1 || sparkAge(cell, frame+4) != -1 {
						t.Fatalf("cell %d frame %d: flare does not fade bright→mid→dim", cell, frame)
					}
				}
			}
		}
	}
	if flares < 1000 || flares > 2000 {
		t.Fatalf("%d flares over 3000 cell-windows; expected roughly one in two", flares)
	}
	if share := float64(bright) / 18000; share < 0.10 || share > 0.25 {
		t.Fatalf("%.0f%% of cell-frames bright; expected about a sixth", share*100)
	}
	phases := map[int]bool{}
	for cell := 0; cell < 30; cell++ {
		for frame := 0; frame < sparkWindow; frame++ {
			if sparkAge(cell, frame) == 0 && (frame == 0 || sparkAge(cell, frame-1) != 0) {
				phases[frame] = true
			}
		}
	}
	if len(phases) < 3 {
		t.Fatalf("flares start in only %d distinct phases; cells should not pulse in unison", len(phases))
	}
}

func TestBarBrightnessLevelsAreDistinct(t *testing.T) {
	a, _ := sized(t, 120, 40)
	st := a.barStyles()
	if st.dim.GetForeground() == st.mid.GetForeground() {
		t.Fatal("dim and fading cells must differ in colour")
	}
	if st.bright.GetForeground() == st.mid.GetForeground() || !st.bright.GetBold() {
		t.Fatal("peak flare must be a distinct colour and bold")
	}
	if st.solid.GetForeground() != a.renderer.Styles.Theme.Unread || st.solid.GetBold() {
		t.Fatal("a finished bar is solid in the theme's Unread colour, the green of received packets")
	}
	if a.renderer.Styles.Badge.GetForeground() != st.solid.GetForeground() {
		t.Fatal("the finished bar and the received-packet badge must share a colour")
	}
}

func TestFreshnessFollowsScans(t *testing.T) {
	st := seededStore(t)
	dev, _ := st.Get("mac-b")
	if err := st.Apply(obs("mac-d", model.FieldHostname, "ghost.lan", "rdns", 0.7)); err != nil {
		t.Fatal(err)
	}
	ghost, _ := st.Get("mac-d")

	scan2 := t0.Add(time.Minute)
	running := engine.Status{Scan: 2, ScanStarted: scan2,
		Discoverers: []engine.DiscovererStatus{{Name: "arp", State: engine.StateRunning, Total: 254, Done: 3}}}
	done := engine.Status{Scan: 2, ScanStarted: scan2,
		Discoverers: []engine.DiscovererStatus{{Name: "arp", State: engine.StateDone, Total: 254, Done: 254}},
		Enrichers:   []engine.EnricherStatus{{Name: "icmp"}}}
	doneButBusy := done
	doneButBusy.Enrichers = []engine.EnricherStatus{{Name: "icmp", Queued: 1}}
	cancelled := engine.Status{Scan: 2, ScanStarted: scan2,
		Discoverers: []engine.DiscovererStatus{{Name: "arp", State: engine.StateCancelled, Total: 254, Done: 9}}}

	cases := []struct {
		name string
		st   engine.Status
		want freshness
	}{
		{"first scan", engine.Status{Scan: 1, ScanStarted: t0}, fresh},
		{"no scan yet", engine.Status{}, fresh},
		{"scan 2 running", running, stale},
		{"scan 2 done, lookups pending", doneButBusy, stale},
		{"scan 2 settled", done, notAnswering},
		{"scan 2 cancelled", cancelled, stale},
	}
	for _, c := range cases {
		if got := classify(dev, c.st); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if got := classify(ghost, done); got != unheard {
		t.Errorf("resolver-only device = %v, want unheard", got)
	}

	// Heard from during scan 2 by any direct probe: fresh again.
	if err := st.Apply(model.Observation{DeviceKey: "mac-b", Field: model.FieldLatency, Value: "3ms", Source: "icmp", Method: "echo reply", Confidence: 1, At: scan2.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	dev, _ = st.Get("mac-b")
	if got := classify(dev, done); got != fresh {
		t.Errorf("after icmp reply = %v, want fresh", got)
	}
	// A resolver answering during scan 2 is not contact.
	if err := st.Apply(model.Observation{DeviceKey: "mac-a", Field: model.FieldHostname, Value: "gw.lan", Source: "rdns", Method: "PTR", Confidence: 0.7, At: scan2.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	gw, _ := st.Get("mac-a")
	if got := classify(gw, done); got != notAnswering {
		t.Errorf("after rdns only = %v, want notAnswering", got)
	}

	a, _ := sized(t, 120, 40)
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	a.Update(Batch{Devices: st.Devices(), Status: done, At: scan2.Add(10 * time.Second)})
	if a.selected != "mac-b" {
		t.Fatal("setup")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyUp}) // mac-a, which did not answer
	plain := ansi.Strip(a.View())
	if !strings.Contains(plain, "✗ 192.168.1.1 ") {
		t.Errorf("silent device should carry the ✗ mark\n%s", plain)
	}
	if details := ansi.Strip(joinLines(a.renderDetails(100))); !strings.Contains(details, "✗ did not answer scan 2, which has finished; last contact 1m ago via arp") {
		t.Errorf("details should explain the freshness\n%s", details)
	}
	if !strings.Contains(plain, "  192.168.1.20 ") {
		t.Errorf("answering device should have a blank mark\n%s", plain)
	}
}

type blockingDiscoverer struct{}

func (blockingDiscoverer) Name() string { return "arp" }
func (blockingDiscoverer) Run(ctx context.Context, _ netif.Interface, _ engine.Emit, report engine.Report) error {
	report(engine.ProbeEvent{Kind: engine.KindProgress, Done: 1, Total: 2})
	<-ctx.Done()
	return ctx.Err()
}

func TestRescanAndCancelKeysDriveTheEngine(t *testing.T) {
	st := store.NewMemory()
	eng := engine.New(st, netif.Interface{})
	if err := eng.AddDiscoverer(blockingDiscoverer{}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	a := newApp(Options{Store: st, Engine: eng, Theme: "nord"})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	wait := func(what string, cond func(engine.Status) bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if cond(eng.Status()) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	wait("scan 1", func(s engine.Status) bool { return s.Scan == 1 && s.Discoverers[0].Total == 2 })
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	wait("cancelled", func(s engine.Status) bool { return s.Discoverers[0].State == engine.StateCancelled })
	a.Update(Batch{Status: eng.Status(), At: t0})
	if got := a.scanLabel(); got != "scan 1 stopped" {
		t.Fatalf("label = %q", got)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	wait("scan 2", func(s engine.Status) bool { return s.Scan == 2 && s.Discoverers[0].State == engine.StateRunning })
	a.Update(Batch{Status: eng.Status(), At: t0})
	if got := a.scanLabel(); got != "scan 2 running" {
		t.Fatalf("label = %q", got)
	}
}

func TestScanLabel(t *testing.T) {
	a, _ := sized(t, 100, 30)
	set := func(ds []engine.DiscovererStatus, es []engine.EnricherStatus) {
		a.status = engine.Status{Scan: 3, ScanStarted: t0, Discoverers: ds, Enrichers: es}
	}
	a.status = engine.Status{}
	if got := a.scanLabel(); got != "starting" {
		t.Errorf("before start: %q", got)
	}
	set([]engine.DiscovererStatus{{State: engine.StateRunning, Total: 254, Done: 1}}, nil)
	if got := a.scanLabel(); got != "scan 3 running" {
		t.Errorf("running: %q", got)
	}
	set([]engine.DiscovererStatus{{State: engine.StateDone, Total: 254, Done: 254}}, []engine.EnricherStatus{{Running: 1}})
	if got := a.scanLabel(); got != "scan 3 finishing" {
		t.Errorf("finishing: %q", got)
	}
	set([]engine.DiscovererStatus{{State: engine.StateDone, Total: 254, Done: 254}, {State: engine.StateRunning}}, []engine.EnricherStatus{{}})
	if got := a.scanLabel(); got != "scan 3 done" {
		t.Errorf("done with a listener still up: %q", got)
	}
	set([]engine.DiscovererStatus{{State: engine.StateRunning}}, nil)
	if got := a.scanLabel(); got != "scan 3 listening" {
		t.Errorf("listener only: %q", got)
	}
	set([]engine.DiscovererStatus{{State: engine.StateFailed, Total: 254}}, nil)
	if got := a.scanLabel(); got != "scan 3 failed" {
		t.Errorf("failed: %q", got)
	}
}

func TestKeyBarDropsWholeEntriesRatherThanWrapping(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {100, 30}, {80, 24}} {
		a, _ := sized(t, size[0], size[1])
		lines := strings.Split(ansi.Strip(a.View()), "\n")
		bar := lines[len(lines)-1]
		if w := lipgloss.Width(bar); w != size[0] {
			t.Errorf("%v: status line is %d wide", size, w)
		}
		if !strings.Contains(bar, "? help") || !strings.Contains(bar, "q quit") {
			t.Errorf("%v: help and quit must always be in the key bar: %q", size, bar)
		}
		if !strings.Contains(bar, "3 devices") || !strings.Contains(bar, "scan 1 running") {
			t.Errorf("%v: status text missing: %q", size, bar)
		}
		for _, b := range tableBindings {
			entry := b.key + " " + b.label
			if strings.Contains(bar, entry) {
				continue
			}
			// Absent entirely is fine; a truncated fragment is not.
			for i := len(entry) - 1; i > len(b.key)+1; i-- {
				if strings.HasSuffix(strings.TrimRight(bar, " "), entry[:i]) {
					t.Errorf("%v: key bar truncated %q to %q", size, entry, entry[:i])
				}
			}
		}
	}
	a, _ := sized(t, 140, 40)
	bar := ansi.Strip(a.View())
	for _, b := range tableBindings {
		if !strings.Contains(bar, b.key+" "+b.label) {
			t.Errorf("140 columns should fit every binding, missing %q", b.key+" "+b.label)
		}
	}
	a, _ = sized(t, 120, 40)
	bar = ansi.Strip(a.View())
	shown := 0
	for _, b := range tableBindings {
		if strings.Contains(bar, b.key+" "+b.label) {
			shown++
		}
	}
	if shown < 7 || strings.Contains(bar, "t theme") {
		t.Errorf("120 columns should fit all but the least important bindings: %q", bar)
	}
	for _, size := range [][2]int{{80, 24}, {100, 30}, {120, 40}} {
		a, _ := sized(t, size[0], size[1])
		lines := strings.Split(ansi.Strip(a.View()), "\n")
		bar := lines[len(lines)-1]
		if !strings.Contains(bar, "c stop") || !strings.Contains(bar, "r rescan") {
			t.Errorf("%v: stop and rescan must both be visible: %q", size, bar)
		}
	}
	if got := keyBar(a.renderer.Styles, tableBindings, 5); got != "" {
		t.Errorf("no room: %q", got)
	}
}

func TestThemePickerPreviewsLiveAndRestoresOnEsc(t *testing.T) {
	a, _ := sized(t, 120, 40)
	original := a.renderer.Styles.Theme.Name
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if !a.picker.Opened() {
		t.Fatal("t should open the picker")
	}
	if plain := ansi.Strip(a.View()); !strings.Contains(plain, "shoal") || !strings.Contains(plain, "theme") || !strings.Contains(plain, "esc revert") {
		t.Errorf("picker modal missing\n%s", plain)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	previewed := a.renderer.Styles.Theme.Name
	if previewed == original {
		t.Fatal("moving the highlight should re-render in the previewed theme")
	}
	if a.theme != original {
		t.Fatal("preview must not confirm")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.picker.Opened() || a.renderer.Styles.Theme.Name != original || a.theme != original {
		t.Fatalf("esc should close and restore %s, got %s", original, a.renderer.Styles.Theme.Name)
	}

	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.picker.Opened() || a.theme == original || a.renderer.Styles.Theme.Name != a.theme {
		t.Fatalf("enter should keep the highlighted theme: confirmed %s rendering %s", a.theme, a.renderer.Styles.Theme.Name)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.renderer.Styles.Theme.Name != a.theme {
		t.Fatal("esc after a confirmed change should restore the confirmed theme, not the original")
	}
}

func TestKeptThemeIsRemembered(t *testing.T) {
	a, _ := sized(t, 120, 40)
	var saved []string
	a.opts.SaveTheme = func(name string) error { saved = append(saved, name); return nil }

	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || len(saved) != 0 {
		t.Fatal("reverting with Esc must not remember anything")
	}

	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	_, cmd = a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("keeping a theme should save it")
	}
	a.Update(cmd())
	if len(saved) != 1 || saved[0] != a.theme {
		t.Fatalf("saved %v, want the kept theme %q", saved, a.theme)
	}
	if last := a.events[len(a.events)-1]; last.Probe != "config" || !strings.Contains(last.Message, "kept for next time") {
		t.Errorf("the log should say it was kept: %+v", last)
	}

	a.opts.SaveTheme = func(string) error { return errors.New("read-only file system") }
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	_, cmd = a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a.Update(cmd())
	if last := a.events[len(a.events)-1]; last.Kind != engine.KindError || !strings.Contains(last.Message, "could not be kept for next time: read-only file system") {
		t.Errorf("a failed save should be logged as an error: %+v", last)
	}
}

func TestHelpIsAScrollableManual(t *testing.T) {
	a, _ := sized(t, 120, 40)
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !a.help.open {
		t.Fatal("? should open help")
	}
	plain := ansi.Strip(a.View())
	for _, want := range []string{"HOW TO READ THIS SCREEN", "Shoal finds the devices", "esc close", "of "} {
		if !strings.Contains(plain, want) {
			t.Errorf("help missing %q\n%s", want, plain)
		}
	}
	lines := a.helpLines()
	if len(lines) < 100 {
		t.Fatalf("manual is only %d lines; it should be a manual, not a key list", len(lines))
	}
	width := a.helpPanelWidth() - 4
	for i, l := range lines {
		if w := lipgloss.Width(l); w > width {
			t.Errorf("line %d is %d wide, panel text width %d: %q", i, w, width, ansi.Strip(l))
		}
	}
	joined := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"THE PANES", "PROBES: DISCOVERERS AND ENRICHERS", "THE COLUMNS", "READING THE DETAILS PANE", "FRESHNESS", "SCANS", "FILTER AND SORT", "KEYS", "GOING DEEPER", "docs/protocols/", "shoal probe arp", "Confidence"} {
		if !strings.Contains(joined, want) {
			t.Errorf("manual lacks %q", want)
		}
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if a.help.scroll.Offset() != 1 {
		t.Fatalf("j should scroll, offset %d", a.help.scroll.Offset())
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if off := a.help.scroll.Offset(); off != len(lines)-a.helpRows() {
		t.Fatalf("G should stop at the last page: offset %d of %d lines, %d rows", off, len(lines), a.helpRows())
	}
	if a.cursor != 0 {
		t.Fatal("scrolling help must not move the table")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if a.help.open {
		t.Fatal("q should close help, not quit")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.help.open {
		t.Fatal("esc should close help")
	}
	// Small terminals still get a readable manual.
	a, _ = sized(t, 40, 12)
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	for i, l := range strings.Split(a.View(), "\n") {
		if w := lipgloss.Width(l); w != 40 {
			t.Errorf("40x12 help: line %d is %d wide", i, w)
		}
	}
}

func TestTabbedLayoutOnSmallTerminals(t *testing.T) {
	a, _ := sized(t, 60, 18)
	if !a.tabbed() {
		t.Fatal("60x18 should be tabbed")
	}
	plain := ansi.Strip(a.View())
	if !strings.Contains(plain, "> Devices") || !strings.Contains(plain, "  Details") || !strings.Contains(plain, "Under the") {
		t.Errorf("tab bar missing\n%s", plain)
	}
	if !strings.Contains(plain, "192.168.1.20") || strings.Contains(plain, "arp said") {
		t.Errorf("devices tab should show the table only\n%s", plain)
	}
	if strings.Contains(plain, "Detail 1") {
		t.Errorf("a tab title must not be truncated by a hint\n%s", plain)
	}
	narrow, _ := sized(t, 80, 24)
	if plain := ansi.Strip(narrow.View()); !strings.Contains(plain, "Under the hood") || !strings.Contains(plain, "simulated, no packets sent") {
		t.Errorf("a hint that does not fit the header should move into the pane, not truncate the title\n%s", plain)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyTab})
	plain = ansi.Strip(a.View())
	if a.focus != paneDetails || !strings.Contains(plain, "> Details") || !strings.Contains(plain, "first seen") {
		t.Errorf("tab should show details\n%s", plain)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyTab})
	plain = ansi.Strip(a.View())
	if a.focus != paneHood || !strings.Contains(plain, "who-has 192.168.1.20") || !strings.Contains(plain, "simulated, no packets sent") {
		t.Errorf("second tab should show the log, led by the mode\n%s", plain)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyTab})
	if a.focus != paneDevices {
		t.Fatal("tab should wrap to devices")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.focus != paneDetails {
		t.Fatal("enter should open details")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.focus != paneDevices {
		t.Fatal("esc should return to devices")
	}

	wide, _ := sized(t, 120, 40)
	if wide.tabbed() {
		t.Fatal("120x40 must not be tabbed")
	}
	for i, want := range []pane{paneDetails, paneHood, paneDevices} {
		wide.Update(tea.KeyMsg{Type: tea.KeyTab})
		if wide.focus != want {
			t.Fatalf("tab %d: focus %d, want %d", i+1, wide.focus, want)
		}
	}
}

func TestRawToggleShowsHexDump(t *testing.T) {
	a, st := sized(t, 120, 40)
	raw := []byte("\x00\x01\x08\x00\x06\x04\x00\x02ARP reply bytes here")
	if err := st.Apply(model.Observation{DeviceKey: "mac-a", Field: model.FieldIP, Value: "192.168.1.1", Source: "arp", Method: "ARP reply", Confidence: 1, At: t0, Raw: raw}); err != nil {
		t.Fatal(err)
	}
	a.Update(Batch{Devices: st.Devices(), At: t0})
	plain := ansi.Strip(a.View())
	if strings.Contains(plain, "0000  ") || !strings.Contains(plain, "x shows the raw packet") {
		t.Errorf("raw view should be off by default with a hint\n%s", plain)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	plain = ansi.Strip(a.View())
	if !strings.Contains(plain, "raw: 28 bytes") || !strings.Contains(plain, "0000  00 01 08 00 06 04 00 02  ........") {
		t.Errorf("the pane at this width should dump 8 bytes per line\n%s", plain)
	}
	details := ansi.Strip(joinLines(a.renderDetails(100)))
	for _, want := range []string{"raw: 28 bytes", "0000  00 01 08 00 06 04 00 02  41 52 50 20 72 65 70 6c  ........ARP repl", "0010  79 20 62 79 74 65 73 20  68 65 72 65", "(no raw packet kept for this value)"} {
		if !strings.Contains(details, want) {
			t.Errorf("hex view missing %q\n%s", want, details)
		}
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if strings.Contains(ansi.Strip(a.View()), "0000  ") {
		t.Error("x again should hide the dump")
	}
}

func TestHexDump(t *testing.T) {
	b := make([]byte, 20)
	for i := range b {
		b[i] = byte(0x41 + i)
	}
	wide := hexDump(b, 80)
	if len(wide) != 2 || wide[0] != "0000  41 42 43 44 45 46 47 48  49 4a 4b 4c 4d 4e 4f 50  ABCDEFGHIJKLMNOP" || wide[1] != "0010  51 52 53 54                                       QRST" {
		t.Errorf("16 per line:\n%q", wide)
	}
	if narrow := hexDump(b, 50); len(narrow) != 3 || narrow[0] != "0000  41 42 43 44 45 46 47 48  ABCDEFGH" {
		t.Errorf("8 per line:\n%q", narrow)
	}
	if tiny := hexDump(b, 30); len(tiny) != 5 || tiny[4] != "0010  51 52 53 54  QRST" {
		t.Errorf("4 per line:\n%q", tiny)
	}
	big := make([]byte, rawLimit+100)
	lines := hexDump(big, 80)
	if len(lines) != rawLimit/16+1 || lines[len(lines)-1] != "… 100 more bytes not shown" {
		t.Errorf("cap: %d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

func TestDetailsFocusScrollsAndKeyBarChanges(t *testing.T) {
	a, _ := sized(t, 120, 40)
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.focus != paneDetails {
		t.Fatal("enter focuses details")
	}
	bar := ansi.Strip(a.View())
	if !strings.Contains(bar, "esc back") || !strings.Contains(bar, "x raw") {
		t.Errorf("details key bar missing\n%s", bar)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.details.Offset() != 1 || a.cursor != 0 {
		t.Fatal("down scrolls details")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.focus != paneDevices {
		t.Fatal("esc returns")
	}
}

// TestDemoPointsAtTheBoxesOnTheWrongSubnet runs the real engine over the
// demo cast with the rogue enricher, as `shoal --demo` does, and checks the
// Phase 3a promise: the table flags the link-local camera and the Dante box
// carrying another venue's address, the filter isolates them, and the
// details pane says why and which probe saw the address.
func TestDemoPointsAtTheBoxesOnTheWrongSubnet(t *testing.T) {
	st := store.NewMemory()
	eng := engine.New(st, netif.Interface{})
	opts := fake.Options{Interval: 50 * time.Microsecond, Latency: 200 * time.Microsecond, Seed: 7}
	if err := eng.AddDiscoverer(fake.NewDiscoverer(opts)); err != nil {
		t.Fatal(err)
	}
	for _, en := range append([]engine.Enricher{rogue.New(fake.Subnet())}, fake.NewEnrichers(opts)...) {
		if err := eng.AddEnricher(en); err != nil {
			t.Fatal(err)
		}
	}
	if err := eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	deadline := time.Now().Add(10 * time.Second)
	for !eng.Status().Settled() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !eng.Status().Settled() {
		t.Fatal("demo scan did not settle")
	}

	a := newApp(Options{Store: st, Engine: eng, Demo: true, Theme: "catppuccin-mocha"})
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	a.Update(Batch{Devices: st.Devices(), Status: eng.Status(), At: time.Now()})

	plain := ansi.Strip(a.View())
	for _, want := range []string{"169.254.37.12", "192.168.0.77", "link-local", "off-subnet"} {
		if !strings.Contains(plain, want) {
			t.Errorf("table missing %q\n%s", want, plain)
		}
	}

	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "off-subnet" {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(a.devices) != 1 || a.devices[0].Key != "00:1d:c1:12:34:56" {
		t.Fatalf("/off-subnet should isolate the Dante box: %v", keys(a.devices))
	}
	details := ansi.Strip(joinLines(a.renderDetails(120)))
	for _, want := range []string{
		"off-subnet-ip",
		"192.168.0.77 is outside this interface's subnet 192.168.1.0/24",
		"Learned from arp: simulated ARP request from 192.168.0.77 overheard during the sweep, asking",
		"who-has 192.168.0.1",
	} {
		if !strings.Contains(strings.Join(strings.Fields(details), " "), want) {
			t.Errorf("details missing %q\n%s", want, details)
		}
	}

	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "link-local" {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(a.devices) != 1 || a.devices[0].Key != "00:01:4a:7c:2e:01" {
		t.Fatalf("/link-local should isolate the camera: %v", keys(a.devices))
	}
	if !strings.Contains(ansi.Strip(a.View()), "1 of 13 match") {
		t.Errorf("status bar should count the match\n%s", ansi.Strip(a.View()))
	}
}

func TestHistoryShowsInTableAndDetails(t *testing.T) {
	a, st := sized(t, 120, 40)
	lastWeek := t0.Add(-7 * 24 * time.Hour)
	past := func(key string, f model.Field, v string) model.Observation {
		return model.Observation{DeviceKey: key, Field: f, Value: v, Source: model.SourceHistory,
			Method: "remembered from an earlier visit: last heard on this network on 2026-09-09 12:00 by arp", Confidence: 0.5, At: lastWeek}
	}
	for _, o := range []model.Observation{
		// A projector from last week that has not answered.
		past("aa:26:ab:00:00:04", model.FieldMAC, "aa:26:ab:00:00:04"),
		past("aa:26:ab:00:00:04", model.FieldIP, "192.168.1.88"),
		past("aa:26:ab:00:00:04", model.FieldHostname, "EPSON-PROJ.local"),
		// The NAS, seen today at .20, was at .21 last week.
		past("mac-b", model.FieldIP, "192.168.1.21"),
		past("mac-b", model.FieldFirstSeen, lastWeek.Add(-30*24*time.Hour).Format(time.RFC3339)),
		{DeviceKey: "mac-b", Field: model.FieldFlag, Value: "ip-changed", Source: "history", Method: "was at 192.168.1.21 when last heard on 2026-09-09 12:00; now at 192.168.1.20 (arp)", Confidence: 1, At: t0},
	} {
		if err := st.Apply(o); err != nil {
			t.Fatal(err)
		}
	}
	settled := engine.Status{Scan: 1, ScanStarted: t0,
		Discoverers: []engine.DiscovererStatus{{Name: "arp", State: engine.StateDone, Done: 254, Total: 254},
			{Name: "history", State: engine.StateRunning, Message: "visit 2 · 5 known · 1 new · 1 missing"}}}
	a.Update(Batch{Devices: st.Devices(), Status: settled, At: t0.Add(5 * time.Second)})

	row := func(ip string) string {
		for _, l := range strings.Split(ansi.Strip(a.View()), "\n") {
			if left := ansi.Truncate(l, 72, ""); strings.Contains(left, " "+ip+" ") {
				return left
			}
		}
		return ""
	}
	if r := row("192.168.1.88"); !strings.Contains(r, "✗") || !strings.Contains(r, "missing") {
		t.Errorf("a remembered device the scan did not hear should be marked missing: %q", r)
	}
	if r := row("192.168.1.20"); !strings.Contains(r, "new-ip") {
		t.Errorf("the NAS should carry its new-ip badge: %q", r)
	}
	if nas, _ := st.Get("mac-b"); nas.Conflicting(model.FieldIP, t0) {
		t.Error("last week's address is not a conflict")
	}
	if !strings.Contains(ansi.Strip(a.View()), "history visit 2 · 5 known · 1 new · 1 missing") {
		t.Errorf("the history probe's row should say where the visit stands\n%s", ansi.Strip(a.View()))
	}

	// Sorting by FIRST SEEN keeps that column on screen, oldest first.
	for a.sort.field() != model.FieldFirstSeen {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	}
	plain := ansi.Strip(a.View())
	if !strings.Contains(plain, "FIRST SEEN ▾") || !strings.Contains(plain, "37d ago") {
		t.Errorf("the sort column must stay visible, with the NAS first seen 37 days ago\n%s", plain)
	}
	if a.devices[0].Key != "mac-b" {
		t.Errorf("oldest first: %v", keys(a.devices))
	}

	for i, d := range a.devices {
		if d.Key == "mac-b" {
			a.cursor = i
		}
	}
	a.syncSelection()
	details := strings.Join(strings.Fields(ansi.Strip(joinLines(a.renderDetails(100)))), " ")
	for _, want := range []string{"192.168.1.21 (last visit)", "first_seen", "was at 192.168.1.21 when last heard", "first seen 2026-08-10"} {
		if !strings.Contains(details, want) {
			t.Errorf("NAS details missing %q\n%s", want, details)
		}
	}
	if strings.Contains(details, "192.168.1.21 (disagrees)") {
		t.Error("a remembered value must read as last visit, not a disagreement")
	}
	for i, d := range a.devices {
		if d.Key == "aa:26:ab:00:00:04" {
			a.cursor = i
		}
	}
	a.syncSelection()
	details = strings.Join(strings.Fields(ansi.Strip(joinLines(a.renderDetails(100)))), " ")
	for _, want := range []string{"remembered from an earlier visit to this network and not heard on this one", "did not answer scan 1, which has finished; last heard on an earlier visit, 7d ago"} {
		if !strings.Contains(details, want) {
			t.Errorf("projector details missing %q\n%s", want, details)
		}
	}
}

func TestSortColumnIsNeverDropped(t *testing.T) {
	for w := 20; w <= 160; w += 7 {
		for _, c := range columns {
			cols, _ := layoutColumns(w, c.field)
			found := false
			for _, got := range cols {
				found = found || got.field == c.field
			}
			if !found {
				t.Fatalf("width %d: sorting by %s dropped it", w, c.title)
			}
		}
	}
}

// TestDemoRemembersThePreviousVisit runs the demo as shoal --demo does,
// with its invented visit three days ago, and checks each change reads as
// it should once the scan has run its course.
func TestDemoRemembersThePreviousVisit(t *testing.T) {
	st := store.NewMemory()
	eng := engine.New(st, fake.Interface())
	opts := fake.Options{Interval: 50 * time.Microsecond, Latency: 200 * time.Microsecond, Seed: 7}
	h, err := store.OpenHistory("")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := fake.SeedHistory(h, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := eng.AddDiscoverer(fake.NewDiscoverer(opts)); err != nil {
		t.Fatal(err)
	}
	if err := eng.AddDiscoverer(history.New(history.Options{History: h, Store: st, Status: eng.Status, Poll: 5 * time.Millisecond})); err != nil {
		t.Fatal(err)
	}
	for _, en := range append([]engine.Enricher{rogue.New(fake.Subnet())}, fake.NewEnrichers(opts)...) {
		if err := eng.AddEnricher(en); err != nil {
			t.Fatal(err)
		}
	}
	if err := eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()

	flagsOf := func(key string) string {
		d, _ := st.Get(key)
		return strings.Join(d.Values(model.FieldFlag, time.Now()), ",")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if eng.Status().Settled() && strings.Contains(flagsOf("b8:27:eb:6d:2f:90"), "name-changed") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	for key, want := range map[string]string{
		"00:1e:0b:55:d3:1a": "ip-changed",   // the printer, .51 last time
		"b8:27:eb:6d:2f:90": "name-changed", // the Pi, octopi.local last time
		"f4:f5:d8:12:34:56": "new-device",   // the Chromecast
	} {
		if got := flagsOf(key); !strings.Contains(got, want) {
			t.Errorf("%s flags = %q, want %s", key, got, want)
		}
	}
	if got := flagsOf("2c:c8:1b:4a:10:01"); got != "" {
		t.Errorf("the gateway was here last time and has not changed: %q", got)
	}

	a := newApp(Options{Store: st, Engine: eng, Demo: true, Theme: "nord"})
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	a.Update(Batch{Devices: st.Devices(), Status: eng.Status(), At: time.Now()})
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "epson" {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(a.devices) != 1 {
		t.Fatalf("/epson should find the projector remembered from last time: %v", keys(a.devices))
	}
	plain := ansi.Strip(a.View())
	if !strings.Contains(plain, "192.168.1.88") || !strings.Contains(plain, "missing") {
		t.Errorf("the projector should be a missing row\n%s", plain)
	}
	var row string
	for _, d := range eng.Status().Discoverers {
		if d.Name == "history" {
			row = d.Message
		}
	}
	if row != "visit 2 · 11 known · 3 new · 1 missing" {
		t.Errorf("the history row should sum up the visit: %q", row)
	}
}

func TestWrapNeverOverflows(t *testing.T) {
	text := "in the per-user data directory; shoal probe history lists what it holds, --history moves it and --no-history turns it off"
	for w := 8; w <= 80; w++ {
		for _, l := range wrap(text, w) {
			if lipgloss.Width(l) > w {
				t.Fatalf("width %d: %q is %d wide", w, l, lipgloss.Width(l))
			}
		}
	}
}

func TestServicesText(t *testing.T) {
	d := model.NewDevice("k", t0)
	for _, v := range []string{"_http._tcp", "_netaudio-arc._udp", "_netaudio-cmc._udp", "_ipp._tcp", "_ndi._tcp", "_airplay._tcp"} {
		d.Add(model.Observation{DeviceKey: "k", Field: model.FieldService, Value: v, Source: "mdns", Method: "m", Confidence: 0.9, At: t0})
	}
	if got := servicesText(d.Snapshot(), t0); got != "dante,ndi,airplay,http,ipp" {
		t.Fatalf("services = %q: Dante once and first, NDI next, the rest in order", got)
	}
	if got := servicesText(model.NewDevice("e", t0).Snapshot(), t0); got != "" {
		t.Fatalf("no services = %q", got)
	}
}

// TestDemoFlagsDanteAndNDI runs the demo's probes and checks the stagebox
// and the camera are badged, with services shown and filterable.
func TestDemoFlagsDanteAndNDI(t *testing.T) {
	st := store.NewMemory()
	eng := engine.New(st, fake.Interface())
	opts := fake.Options{Interval: 50 * time.Microsecond, Latency: 200 * time.Microsecond, Seed: 7}
	if err := eng.AddDiscoverer(fake.NewDiscoverer(opts)); err != nil {
		t.Fatal(err)
	}
	reg, err := oui.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range append([]engine.Enricher{av.New(), oui.New(reg)}, fake.NewEnrichers(opts)...) {
		if err := eng.AddEnricher(en); err != nil {
			t.Fatal(err)
		}
	}
	if err := eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	deadline := time.Now().Add(10 * time.Second)
	for !eng.Status().Settled() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	a := newApp(Options{Store: st, Engine: eng, Demo: true, Theme: "nord"})
	a.Update(tea.WindowSizeMsg{Width: 180, Height: 40})
	a.Update(Batch{Devices: st.Devices(), Status: eng.Status(), At: time.Now()})
	row := func(ip string) string {
		for _, l := range strings.Split(ansi.Strip(a.View()), "\n") {
			if left := ansi.Truncate(l, 108, ""); strings.Contains(left, " "+ip+" ") {
				return left
			}
		}
		return ""
	}
	if r := row("192.168.0.77"); strings.Count(r, "dante") < 2 {
		t.Errorf("the stagebox should be badged dante and list dante among its services: %q", r)
	}
	if r := row("169.254.37.12"); !strings.Contains(r, "ndi") || !strings.Contains(r, "rtsp") {
		t.Errorf("the camera should be badged ndi and show rtsp among its services: %q", r)
	}
	if !strings.Contains(ansi.Strip(a.View()), "SERVICES") {
		t.Errorf("SERVICES should fit at 180 columns\n%s", ansi.Strip(a.View()))
	}

	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for _, r := range "dante" {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(a.devices) != 1 || a.devices[0].Key != "00:1d:c1:12:34:56" {
		t.Fatalf("/dante should find the stagebox: %v", keys(a.devices))
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	details := strings.Join(strings.Fields(ansi.Strip(joinLines(a.renderDetails(120)))), " ")
	for _, want := range []string{"dante-device", "a Dante audio-over-IP device", "announces _netaudio-arc._udp over mDNS", "made by Audinate"} {
		if !strings.Contains(details, want) {
			t.Errorf("details missing %q\n%s", want, details)
		}
	}
}
