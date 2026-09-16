package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/store"
)

type scriptedDiscoverer struct {
	name    string
	devices int
	block   bool
	fail    error
	noMeth  bool
}

func (s scriptedDiscoverer) Name() string { return s.name }

func (s scriptedDiscoverer) Run(ctx context.Context, _ netif.Interface, emit Emit, report Report) error {
	for i := 1; i <= s.devices; i++ {
		key := fmt.Sprintf("mac-%d", i)
		ip := fmt.Sprintf("10.0.0.%d", i)
		report(ProbeEvent{Kind: KindSent, Target: ip, Message: "who-has " + ip})
		o := model.Observation{DeviceKey: key, Field: model.FieldMAC, Value: key, Method: "reply from " + ip, Confidence: 1}
		if s.noMeth {
			o.Method = ""
		}
		emit(o)
		emit(model.Observation{DeviceKey: key, Field: model.FieldIP, Value: ip, Method: "reply from " + ip, Confidence: 1})
		report(ProbeEvent{Kind: KindProgress, Done: i, Total: s.devices})
	}
	if s.fail != nil {
		return s.fail
	}
	if s.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

type countingEnricher struct {
	name        string
	triggers    []model.Field
	concurrency int
	delay       time.Duration
	fail        bool

	mu      sync.Mutex
	calls   map[string]int
	inUse   int32
	maxUse  int32
	started chan struct{}
}

func newCountingEnricher(name string, concurrency int, triggers ...model.Field) *countingEnricher {
	return &countingEnricher{name: name, triggers: triggers, concurrency: concurrency, calls: make(map[string]int)}
}

func (c *countingEnricher) Name() string            { return c.name }
func (c *countingEnricher) Triggers() []model.Field { return c.triggers }
func (c *countingEnricher) Concurrency() int        { return c.concurrency }

func (c *countingEnricher) Enrich(ctx context.Context, d model.DeviceSnapshot, emit Emit, report Report) error {
	n := atomic.AddInt32(&c.inUse, 1)
	defer atomic.AddInt32(&c.inUse, -1)
	for {
		m := atomic.LoadInt32(&c.maxUse)
		if n <= m || atomic.CompareAndSwapInt32(&c.maxUse, m, n) {
			break
		}
	}
	c.mu.Lock()
	c.calls[d.Key]++
	c.mu.Unlock()
	if c.started != nil {
		select {
		case c.started <- struct{}{}:
		default:
		}
	}
	if c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if c.fail {
		return errors.New("enrich failed")
	}
	report(ProbeEvent{Kind: KindSent, Target: d.Key, Message: "lookup"})
	emit(model.Observation{DeviceKey: d.Key, Field: model.FieldVendor, Value: "Vendor of " + d.Key, Method: "lookup", Confidence: 0.8})
	return nil
}

func (c *countingEnricher) callCount(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[key]
}

type eventLog struct {
	mu     sync.Mutex
	events []ProbeEvent
}

func (l *eventLog) add(ev ProbeEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
}

func (l *eventLog) filter(pred func(ProbeEvent) bool) []ProbeEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []ProbeEvent
	for _, ev := range l.events {
		if pred(ev) {
			out = append(out, ev)
		}
	}
	return out
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func enricherStatus(e *Engine, name string) EnricherStatus {
	for _, s := range e.Status().Enrichers {
		if s.Name == name {
			return s
		}
	}
	return EnricherStatus{}
}

func settled(e *Engine, name string, completed int) func() bool {
	return func() bool {
		s := enricherStatus(e, name)
		return s.Running == 0 && s.Queued == 0 && s.Completed+s.Failed == completed
	}
}

func TestDiscovererPopulatesStoreAndStatus(t *testing.T) {
	st := store.NewMemory()
	e := New(st, netif.Interface{})
	log := &eventLog{}
	e.Subscribe(log.add)
	if err := e.AddDiscoverer(scriptedDiscoverer{name: "arp", devices: 3}); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()

	eventually(t, "discoverer done", func() bool { return e.Status().Discoverers[0].State == StateDone })
	if st.Len() != 3 {
		t.Fatalf("store has %d devices, want 3", st.Len())
	}
	snap, _ := st.Get("mac-2")
	if got, _ := snap.ResolvedAt(model.FieldIP, time.Now()); got.Value != "10.0.0.2" || got.Source != "arp" {
		t.Fatalf("mac-2 ip = %+v; source should default to the probe name", got)
	}

	ds := e.Status().Discoverers[0]
	if ds.Name != "arp" || ds.Done != 3 || ds.Total != 3 {
		t.Fatalf("status = %+v", ds)
	}
	sent := log.filter(func(ev ProbeEvent) bool { return ev.Kind == KindSent })
	if len(sent) != 3 || sent[0].Probe != "arp" || sent[0].At.IsZero() {
		t.Fatalf("sent events = %+v", sent)
	}
}

func TestEnricherTriggeredOncePerChange(t *testing.T) {
	st := store.NewMemory()
	e := New(st, netif.Interface{})
	log := &eventLog{}
	e.Subscribe(log.add)
	en := newCountingEnricher("oui", 2, model.FieldMAC)
	if err := e.AddEnricher(en); err != nil {
		t.Fatal(err)
	}
	if err := e.AddDiscoverer(scriptedDiscoverer{name: "arp", devices: 4}); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()

	eventually(t, "enricher settled", settled(e, "oui", 4))
	for i := 1; i <= 4; i++ {
		key := fmt.Sprintf("mac-%d", i)
		if n := en.callCount(key); n != 1 {
			t.Errorf("%s enriched %d times, want 1", key, n)
		}
		snap, _ := st.Get(key)
		if v, ok := snap.ResolvedAt(model.FieldVendor, time.Now()); !ok || v.Source != "oui" {
			t.Errorf("%s vendor = %+v ok=%v", key, v, ok)
		}
	}

	// A repeat of an already-known value must not re-trigger.
	if err := st.Apply(model.Observation{DeviceKey: "mac-1", Field: model.FieldMAC, Value: "mac-1", Source: "arp", Method: "again", Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	// A change to an untriggered field must not either.
	if err := st.Apply(model.Observation{DeviceKey: "mac-1", Field: model.FieldHostname, Value: "h", Source: "mdns", Method: "m", Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if n := en.callCount("mac-1"); n != 1 {
		t.Fatalf("mac-1 re-enriched: %d calls", n)
	}
	if got := enricherStatus(e, "oui"); got.Completed != 4 {
		t.Fatalf("status = %+v", got)
	}
}

func TestEnricherConcurrencyBound(t *testing.T) {
	st := store.NewMemory()
	e := New(st, netif.Interface{})
	en := newCountingEnricher("slow", 2, model.FieldMAC)
	en.delay = 30 * time.Millisecond
	if err := e.AddEnricher(en); err != nil {
		t.Fatal(err)
	}
	if err := e.AddDiscoverer(scriptedDiscoverer{name: "arp", devices: 6}); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()

	eventually(t, "queue observed", func() bool { return enricherStatus(e, "slow").Running > 0 })
	eventually(t, "enricher settled", settled(e, "slow", 6))
	if max := atomic.LoadInt32(&en.maxUse); max != 2 {
		t.Fatalf("max concurrent = %d, want exactly 2 (pool saturated but bounded)", max)
	}
}

func TestEnqueueDedupesWhileQueued(t *testing.T) {
	en := &enricher{queued: make(map[string]bool), wake: make(chan struct{}, 1)}
	en.enqueue("a")
	en.enqueue("a")
	en.enqueue("b")
	if len(en.queue) != 2 {
		t.Fatalf("queue = %v, want [a b]", en.queue)
	}
	if k, _ := en.pop(); k != "a" {
		t.Fatalf("popped %q", k)
	}
	en.enqueue("a")
	if len(en.queue) != 2 {
		t.Fatalf("re-enqueue after pop should be allowed: %v", en.queue)
	}
}

func TestRejectedObservationBecomesErrorEvent(t *testing.T) {
	st := store.NewMemory()
	e := New(st, netif.Interface{})
	log := &eventLog{}
	e.Subscribe(log.add)
	if err := e.AddDiscoverer(scriptedDiscoverer{name: "bad", devices: 1, noMeth: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()

	eventually(t, "discoverer done", func() bool { return e.Status().Discoverers[0].State == StateDone })
	errs := log.filter(func(ev ProbeEvent) bool { return ev.Kind == KindError })
	if len(errs) != 1 || errs[0].Probe != "bad" || errs[0].Target != "mac-1" {
		t.Fatalf("error events = %+v", errs)
	}
	snap, _ := st.Get("mac-1")
	if _, ok := snap.ResolvedAt(model.FieldMAC, time.Now()); ok {
		t.Fatal("observation without method was stored")
	}
}

func TestFailingProbesAreReported(t *testing.T) {
	st := store.NewMemory()
	e := New(st, netif.Interface{})
	log := &eventLog{}
	e.Subscribe(log.add)
	en := newCountingEnricher("flaky", 1, model.FieldMAC)
	en.fail = true
	if err := e.AddEnricher(en); err != nil {
		t.Fatal(err)
	}
	if err := e.AddDiscoverer(scriptedDiscoverer{name: "arp", devices: 1, fail: errors.New("socket closed")}); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()

	eventually(t, "discoverer failed", func() bool { return e.Status().Discoverers[0].State == StateFailed })
	eventually(t, "enricher settled", settled(e, "flaky", 1))
	if ds := e.Status().Discoverers[0]; ds.Err != "socket closed" {
		t.Fatalf("discoverer status = %+v", ds)
	}
	if es := enricherStatus(e, "flaky"); es.Failed != 1 || es.Completed != 0 {
		t.Fatalf("enricher status = %+v", es)
	}
	errs := log.filter(func(ev ProbeEvent) bool { return ev.Kind == KindError })
	if len(errs) != 2 {
		t.Fatalf("error events = %+v, want one per failure", errs)
	}
}

func TestConflictSurfacesAsEvent(t *testing.T) {
	st := store.NewMemory()
	e := New(st, netif.Interface{})
	log := &eventLog{}
	e.Subscribe(log.add)
	for _, o := range []model.Observation{
		{DeviceKey: "k", Field: model.FieldHostname, Value: "printer", Source: "mdns", Method: "m", Confidence: 0.9},
		{DeviceKey: "k", Field: model.FieldHostname, Value: "PRINTER-01", Source: "nbns", Method: "m", Confidence: 0.6},
	} {
		if err := st.Apply(o); err != nil {
			t.Fatal(err)
		}
	}
	infos := log.filter(func(ev ProbeEvent) bool { return ev.Probe == "store" && ev.Kind == KindInfo })
	if len(infos) != 1 || infos[0].Target != "k" || infos[0].Message != "hostname conflict: sources disagree (printer vs PRINTER-01)" {
		t.Fatalf("conflict events = %+v", infos)
	}
}

func TestStopCancelsBlockingDiscoverer(t *testing.T) {
	st := store.NewMemory()
	e := New(st, netif.Interface{})
	if err := e.AddDiscoverer(scriptedDiscoverer{name: "listen", devices: 1, block: true}); err != nil {
		t.Fatal(err)
	}
	en := newCountingEnricher("slow", 1, model.FieldMAC)
	en.delay = time.Hour
	en.started = make(chan struct{}, 1)
	if err := e.AddEnricher(en); err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-en.started

	done := make(chan struct{})
	go func() { e.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
	if s := e.Status().Discoverers[0].State; s != StateCancelled {
		t.Fatalf("state = %s, want cancelled", s)
	}
	if err := e.AddDiscoverer(scriptedDiscoverer{name: "late"}); err == nil {
		t.Fatal("AddDiscoverer after Start should fail")
	}
	if err := e.Start(context.Background()); err == nil {
		t.Fatal("second Start should fail")
	}
}

func TestStopBeforeStartIsSafe(t *testing.T) {
	e := New(store.NewMemory(), netif.Interface{})
	e.Stop()
}
