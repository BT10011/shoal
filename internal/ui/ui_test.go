package ui

import (
	"context"
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
			Enrichers:   []engine.EnricherStatus{{Name: "rdns", Running: 1, Queued: 2, Completed: 3}},
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
		"mdns said synology.local", "conf 0.9", "5s ago",
		"arp    ", "20/254", "running", "rdns   1 running · 2 queued · 3 done",
		"who-has 192.168.1.20", "hostname conflict",
		"DEMO", "3 devices", "q quit",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("view missing %q\n%s", want, plain)
		}
	}
	details := ansi.Strip(joinLines(a.renderDetails(80)))
	for _, want := range []string{"nas.lan  (disagrees)", "rdns · conf 0.7", "heard from directly 5s ago via arp, since scan 1 began"} {
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
		cols, widths := layoutColumns(w)
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
	cols, _ := layoutColumns(120)
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
