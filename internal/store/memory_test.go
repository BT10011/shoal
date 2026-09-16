package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/BT10011/shoal/internal/model"
)

var t0 = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) listen(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) kinds() []EventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]EventKind, len(r.events))
	for i, e := range r.events {
		out[i] = e.Kind
	}
	return out
}

func newTestStore() (*Memory, *recorder) {
	rec := &recorder{}
	m := NewMemory(WithClock(func() time.Time { return t0 }), WithListener(rec.listen))
	return m, rec
}

func obs(key string, f model.Field, value, source string) model.Observation {
	return model.Observation{
		DeviceKey:  key,
		Field:      f,
		Value:      value,
		Source:     source,
		Method:     source + " reported " + value,
		Confidence: 0.9,
		At:         t0,
	}
}

func TestApplyRejectsObservationsWithoutProvenance(t *testing.T) {
	m, _ := newTestStore()
	good := obs("k", model.FieldIP, "10.0.0.1", "arp")

	cases := map[string]func(o *model.Observation){
		"no key":         func(o *model.Observation) { o.DeviceKey = "" },
		"no field":       func(o *model.Observation) { o.Field = "" },
		"no value":       func(o *model.Observation) { o.Value = "" },
		"no source":      func(o *model.Observation) { o.Source = "" },
		"no method":      func(o *model.Observation) { o.Method = "" },
		"confidence > 1": func(o *model.Observation) { o.Confidence = 1.5 },
		"confidence < 0": func(o *model.Observation) { o.Confidence = -0.1 },
	}
	for name, mutate := range cases {
		o := good
		mutate(&o)
		if err := m.Apply(o); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if m.Len() != 0 {
		t.Fatal("rejected observations must not create devices")
	}
	if err := m.Apply(good); err != nil {
		t.Fatalf("valid observation rejected: %v", err)
	}
}

func TestApplyFillsMissingTimestamp(t *testing.T) {
	m, _ := newTestStore()
	o := obs("k", model.FieldIP, "10.0.0.1", "arp")
	o.At = time.Time{}
	if err := m.Apply(o); err != nil {
		t.Fatal(err)
	}
	snap, _ := m.Get("k")
	if got := snap.Facts[model.FieldIP][0].At; !got.Equal(t0) {
		t.Fatalf("At = %v, want store clock %v", got, t0)
	}
}

func TestApplyEmitsAddedThenUpdated(t *testing.T) {
	m, rec := newTestStore()
	must(t, m.Apply(obs("k", model.FieldIP, "10.0.0.1", "arp")))
	must(t, m.Apply(obs("k", model.FieldHostname, "host", "mdns")))

	kinds := rec.kinds()
	if len(kinds) != 2 || kinds[0] != EventDeviceAdded || kinds[1] != EventDeviceUpdated {
		t.Fatalf("events = %v", kinds)
	}
	if rec.events[1].Field != model.FieldHostname {
		t.Fatalf("update event field = %q", rec.events[1].Field)
	}
	if got, _ := rec.events[1].Device.ResolvedAt(model.FieldHostname, t0); got.Value != "host" {
		t.Fatalf("event snapshot missing hostname: %+v", rec.events[1].Device)
	}
}

func TestApplyEmitsConflictOnDisagreement(t *testing.T) {
	m, rec := newTestStore()
	must(t, m.Apply(obs("k", model.FieldHostname, "printer", "mdns")))
	must(t, m.Apply(obs("k", model.FieldHostname, "printer", "rdns")))
	if kinds := rec.kinds(); len(kinds) != 2 {
		t.Fatalf("agreeing sources produced %v", kinds)
	}

	must(t, m.Apply(obs("k", model.FieldHostname, "PRINTER-01", "nbns")))
	kinds := rec.kinds()
	if len(kinds) != 4 || kinds[3] != EventConflict {
		t.Fatalf("events = %v, want conflict last", kinds)
	}
	if !rec.events[3].Device.Conflicting(model.FieldHostname, t0) {
		t.Fatal("conflict event snapshot should show the conflict")
	}

	must(t, m.Apply(obs("k", model.FieldHostname, "PRINTER-01", "nbns")))
	if kinds := rec.kinds(); len(kinds) != 5 || kinds[4] != EventDeviceUpdated {
		t.Fatalf("repeat of an existing value should not re-raise conflict: %v", kinds)
	}
}

func TestApplyNeverConflictsOnMultiValued(t *testing.T) {
	m, rec := newTestStore()
	must(t, m.Apply(obs("k", model.FieldService, "_ssh._tcp", "mdns")))
	must(t, m.Apply(obs("k", model.FieldService, "_http._tcp", "mdns")))
	for _, k := range rec.kinds() {
		if k == EventConflict {
			t.Fatal("services must not conflict")
		}
	}
}

func TestDuplicateIPFlagsEveryClaimant(t *testing.T) {
	m, rec := newTestStore()
	must(t, m.Apply(obs("mac-a", model.FieldIP, "10.0.0.5", "arp")))
	if snap, _ := m.Get("mac-a"); len(snap.Facts[model.FieldFlag]) != 0 {
		t.Fatal("single claimant must not be flagged")
	}

	must(t, m.Apply(obs("mac-b", model.FieldIP, "10.0.0.5", "arp")))
	for _, key := range []string{"mac-a", "mac-b"} {
		snap, _ := m.Get(key)
		flag, ok := snap.ResolvedAt(model.FieldFlag, t0)
		if !ok || flag.Value != "duplicate-ip" {
			t.Fatalf("%s: flag = %+v ok=%v", key, flag, ok)
		}
		if flag.Source != "store" || flag.Method == "" {
			t.Fatalf("%s: flag lacks provenance: %+v", key, flag)
		}
	}
	snapA, _ := m.Get("mac-a")
	if got := snapA.Facts[model.FieldFlag][0].Method; got != "IP 10.0.0.5 is also claimed by [mac-b]" {
		t.Fatalf("mac-a method = %q", got)
	}

	var flagged []string
	for _, e := range rec.events {
		if e.Kind == EventDeviceUpdated && e.Field == model.FieldFlag {
			flagged = append(flagged, e.Key)
		}
	}
	if len(flagged) != 2 {
		t.Fatalf("flag update events for %v, want both devices", flagged)
	}

	before := len(rec.kinds())
	must(t, m.Apply(obs("mac-b", model.FieldIP, "10.0.0.5", "arp")))
	if got := len(rec.kinds()); got != before+1 {
		t.Fatalf("repeat claim emitted %d events, want 1 plain update", got-before)
	}
}

func TestDevicesSortedAndSnapshotted(t *testing.T) {
	m, _ := newTestStore()
	must(t, m.Apply(obs("zz", model.FieldIP, "10.0.0.2", "arp")))
	must(t, m.Apply(obs("aa", model.FieldIP, "10.0.0.1", "arp")))

	devs := m.Devices()
	if len(devs) != 2 || devs[0].Key != "aa" || devs[1].Key != "zz" {
		t.Fatalf("Devices = %v", devs)
	}
	devs[0].Facts[model.FieldIP][0].Value = "tampered"
	if snap, _ := m.Get("aa"); snap.Facts[model.FieldIP][0].Value != "10.0.0.1" {
		t.Fatal("Devices returned a live reference, not a snapshot")
	}
	if _, ok := m.Get("missing"); ok {
		t.Fatal("Get of unknown key reported ok")
	}
}

func TestEventChangedFlag(t *testing.T) {
	m, rec := newTestStore()
	must(t, m.Apply(obs("k", model.FieldIP, "10.0.0.1", "arp")))
	must(t, m.Apply(obs("k", model.FieldIP, "10.0.0.1", "arp")))
	must(t, m.Apply(obs("k", model.FieldIP, "10.0.0.2", "arp")))
	if len(rec.events) != 3 {
		t.Fatalf("got %d events", len(rec.events))
	}
	if !rec.events[0].Changed || rec.events[1].Changed || !rec.events[2].Changed {
		t.Fatalf("Changed flags = %v %v %v, want true false true",
			rec.events[0].Changed, rec.events[1].Changed, rec.events[2].Changed)
	}
}

func TestSubscribeFansOutInOrder(t *testing.T) {
	m, first := newTestStore()
	second := &recorder{}
	m.Subscribe(second.listen)
	must(t, m.Apply(obs("k", model.FieldIP, "10.0.0.1", "arp")))
	must(t, m.Apply(obs("k", model.FieldHostname, "h", "mdns")))
	a, b := first.kinds(), second.kinds()
	if len(a) != 2 || len(b) != 2 || a[0] != b[0] || a[1] != b[1] {
		t.Fatalf("listeners diverged: %v vs %v", a, b)
	}
}

func TestListenerMayCallBackIntoStore(t *testing.T) {
	var m *Memory
	var seen int
	m = NewMemory(WithListener(func(e Event) {
		seen = m.Len()
	}))
	must(t, m.Apply(obs("k", model.FieldIP, "10.0.0.1", "arp")))
	if seen != 1 {
		t.Fatalf("listener saw Len()=%d, want 1", seen)
	}
}

func TestConcurrentApply(t *testing.T) {
	m, _ := newTestStore()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				key := fmt.Sprintf("dev-%d", j%5)
				must(t, m.Apply(obs(key, model.FieldLatency, fmt.Sprintf("%dms", i), "icmp")))
				must(t, m.Apply(obs(key, model.FieldIP, fmt.Sprintf("10.0.0.%d", j%3), "arp")))
				m.Devices()
			}
		}(i)
	}
	wg.Wait()
	if m.Len() != 5 {
		t.Fatalf("Len = %d, want 5", m.Len())
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
