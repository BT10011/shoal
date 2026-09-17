package ui

import (
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
			Discoverers: []engine.DiscovererStatus{{Name: "arp", State: engine.StateRunning, Done: 20, Total: 254}},
			Enrichers:   []engine.EnricherStatus{{Name: "rdns", Running: 1, Queued: 2, Completed: 3}},
		},
		At: t0.Add(5 * time.Second),
	})
	return a, st
}

func TestSortByIPPutsAddresslessLast(t *testing.T) {
	st := seededStore(t)
	sorted := sortByIP(st.Devices())
	if len(sorted) != 3 || sorted[0].Key != "mac-a" || sorted[1].Key != "mac-b" || sorted[2].Key != "mac-c" {
		keys := make([]string, len(sorted))
		for i, d := range sorted {
			keys[i] = d.Key
		}
		t.Fatalf("order = %v", keys)
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
		"mdns said synology.local", "nas.lan  (disagrees)", "conf 0.9", "5s ago",
		"arp    ", "20/254", "running", "rdns   1 running · 2 queued · 3 done",
		"who-has 192.168.1.20", "hostname conflict",
		"DEMO", "3 devices", "q quit",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("view missing %q\n%s", want, plain)
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
	a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.cursor != 1 || a.details.Offset() != 0 {
		t.Fatalf("switching device should reset details scroll: cursor=%d offset=%d", a.cursor, a.details.Offset())
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
