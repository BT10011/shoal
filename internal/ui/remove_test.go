package ui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/store"
)

const (
	silentA = "00:00:5e:00:53:0a" // heard in scan 1, silent in scan 2
	silentB = "00:00:5e:00:53:0b"
	talking = "00:00:5e:00:53:0c" // answered scan 2
)

// rememberer stands in for the history probe.
type rememberer struct {
	mu     sync.Mutex
	forgot []string
}

func (r *rememberer) Name() string { return "history" }
func (r *rememberer) Run(ctx context.Context, _ netif.Interface, _ engine.Emit, _ engine.Report) error {
	<-ctx.Done()
	return nil
}
func (r *rememberer) Remembers() bool { return true }
func (r *rememberer) Forget(key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forgot = append(r.forgot, key)
	return nil
}

// silentApp has two devices the settled second scan did not hear, and one
// it did.
func silentApp(t *testing.T, remembered bool) (*app, *store.Memory, *rememberer) {
	t.Helper()
	st := store.NewMemory()
	scan2 := t0.Add(time.Minute)
	for _, o := range []model.Observation{
		obs(silentA, model.FieldMAC, silentA, "arp", 1), obs(silentA, model.FieldIP, "172.16.10.21", "arp", 1),
		obs(silentA, model.FieldHostname, "stage-left-cam.local", "mdns", 0.9),
		obs(silentB, model.FieldMAC, silentB, "arp", 1), obs(silentB, model.FieldIP, "172.16.10.22", "arp", 1),
	} {
		must(t, st.Apply(o))
	}
	heard := obs(talking, model.FieldIP, "172.16.10.23", "arp", 1)
	heard.At = scan2.Add(time.Second)
	must(t, st.Apply(heard))

	eng := engine.New(st, netif.Interface{})
	r := &rememberer{}
	if remembered {
		must(t, eng.AddDiscoverer(r))
	}
	a := newApp(Options{Store: st, Engine: eng, Theme: "nord"})
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	a.Update(Batch{Devices: st.Devices(), At: scan2.Add(10 * time.Second), Status: engine.Status{
		Scan: 2, ScanStarted: scan2,
		Discoverers: []engine.DiscovererStatus{{Name: "arp", State: engine.StateDone, Done: 254, Total: 254}},
	}})
	return a, st, r
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func selectKey(t *testing.T, a *app, key string) {
	t.Helper()
	for i, d := range a.devices {
		if d.Key == key {
			a.cursor, a.selected = i, key
			return
		}
	}
	t.Fatalf("%s is not on screen", key)
}

func pressKey(a *app, k tea.KeyMsg) {
	_, cmd := a.Update(k)
	run(a, cmd)
}

var (
	enterKey = tea.KeyMsg{Type: tea.KeyEnter}
	upKey    = tea.KeyMsg{Type: tea.KeyUp}
	escKey   = tea.KeyMsg{Type: tea.KeyEsc}
)

func TestDRemovesASilentDeviceOnlyOnceConfirmed(t *testing.T) {
	a, st, _ := silentApp(t, false)
	selectKey(t, a, silentA)
	if !strings.Contains(ansi.Strip(a.View()), "d remove") {
		t.Error("on a device that stopped answering, the key bar offers d")
	}

	press(a, "d")
	if a.remove == nil || a.remove.cursor != removeCancel {
		t.Fatalf("d should ask first, with cancel highlighted: %+v", a.remove)
	}
	view := ansi.Strip(a.View())
	for _, want := range []string{"Remove stage-left-cam.local 172.16.10.21 " + silentA, "did not answer scan 2", "Nothing is ever removed automatically", "comes back if it answers again"} {
		if !strings.Contains(view, want) {
			t.Errorf("the modal should say %q", want)
		}
	}
	if strings.Contains(view, "network is remembered") {
		t.Error("nothing is remembered here, so the modal should not offer to forget")
	}
	pressKey(a, escKey)
	pressKey(a, enterKey) // Enter on the table afterwards focuses details; nothing removed
	if _, ok := st.Get(silentA); !ok || a.remove != nil {
		t.Fatal("Esc should cancel")
	}

	press(a, "d")
	pressKey(a, upKey) // past the forget choice, which is not on offer
	if a.remove.cursor != removeList {
		t.Fatalf("up from cancel should skip the unavailable forget choice, cursor %d", a.remove.cursor)
	}
	pressKey(a, enterKey)
	if _, ok := st.Get(silentA); ok {
		t.Error("the device should be gone from the store")
	}
	if _, ok := st.Get(silentB); !ok {
		t.Error("only the selected device goes")
	}
}

func TestDLeavesADeviceThatIsAnsweringAndSaysWhy(t *testing.T) {
	a, st, _ := silentApp(t, false)
	selectKey(t, a, talking)
	if strings.Contains(ansi.Strip(a.View()), "d remove") {
		t.Error("d is not offered on a device that is answering")
	}
	press(a, "d")
	if a.remove != nil {
		t.Fatal("no modal for a device that is answering")
	}
	if msg := lastLog(a); !strings.Contains(msg, "still answering, so it stays") {
		t.Errorf("log = %q", msg)
	}
	if _, ok := st.Get(talking); !ok {
		t.Error("it must stay")
	}
}

func TestShiftDRemovesEveryDeviceNotAnswering(t *testing.T) {
	a, st, _ := silentApp(t, false)
	if !strings.Contains(ansi.Strip(a.View()), "D remove all") {
		t.Error("with devices not answering, the key bar offers D")
	}
	press(a, "D")
	if a.remove == nil || len(a.remove.keys) != 2 {
		t.Fatalf("D should cover exactly the two red rows: %+v", a.remove)
	}
	if !strings.Contains(ansi.Strip(a.View()), "Remove the 2 devices that did not answer scan 2") {
		t.Error("the modal should say how many")
	}
	pressKey(a, upKey)
	pressKey(a, enterKey)
	if st.Len() != 1 {
		t.Fatalf("left %d devices, want only the one answering", st.Len())
	}
	if _, ok := st.Get(talking); !ok {
		t.Error("the answering device must stay")
	}
}

func TestForgetIsOfferedWhereTheNetworkIsRemembered(t *testing.T) {
	a, st, r := silentApp(t, true)
	selectKey(t, a, silentB)
	press(a, "d")
	if !a.remove.canForget || !strings.Contains(ansi.Strip(a.View()), "network is remembered") {
		t.Fatal("a remembered network should offer to forget, and explain the difference")
	}
	pressKey(a, upKey) // cancel → forget
	pressKey(a, enterKey)
	if _, ok := st.Get(silentB); ok {
		t.Error("the device should be removed")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.forgot) != 1 || r.forgot[0] != silentB {
		t.Errorf("forgot %v, want %s", r.forgot, silentB)
	}
}

func TestRemovalIsOfferedOnlyWhenTheScanHasSettled(t *testing.T) {
	a, _, _ := silentApp(t, false)
	a.Update(Batch{Devices: a.all, At: a.now, Status: engine.Status{Scan: 3, ScanStarted: a.now,
		Discoverers: []engine.DiscovererStatus{{Name: "arp", State: engine.StateRunning, Done: 10, Total: 254}}}})
	selectKey(t, a, silentA)
	press(a, "d")
	if a.remove != nil {
		t.Fatal("while a scan is still asking, nothing is red and nothing can be removed")
	}
	if msg := lastLog(a); !strings.Contains(msg, "wait for the scan to finish") {
		t.Errorf("log = %q", msg)
	}
	press(a, "D")
	if a.remove != nil {
		t.Error("D has nothing to remove while the scan runs")
	}
}
