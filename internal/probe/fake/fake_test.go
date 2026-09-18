package fake

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/probe/oui"
	"github.com/BT10011/shoal/internal/store"
)

var fast = Options{Interval: 50 * time.Microsecond, Latency: 200 * time.Microsecond, Seed: 7}

type collector struct {
	mu     sync.Mutex
	obs    []model.Observation
	events []engine.ProbeEvent
}

func (c *collector) emit(o model.Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.obs = append(c.obs, o)
}

func (c *collector) report(e engine.ProbeEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *collector) observation(key string, f model.Field) (model.Observation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, o := range c.obs {
		if o.DeviceKey == key && o.Field == f {
			return o, true
		}
	}
	return model.Observation{}, false
}

func (c *collector) count(kind engine.EventKind) int {
	n := 0
	for _, e := range c.events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func TestDiscovererSweepsEveryAddress(t *testing.T) {
	d := NewDiscoverer(fast)
	c := &collector{}
	if err := d.Run(context.Background(), netif.Interface{}, c.emit, c.report); err != nil {
		t.Fatal(err)
	}
	if got := c.count(engine.KindSent); got != hosts {
		t.Fatalf("sent %d requests, want %d", got, hosts)
	}
	if got := c.count(engine.KindReceived); got != len(DefaultDevices()) {
		t.Fatalf("got %d replies, want one per device (%d)", got, len(DefaultDevices()))
	}
	overheard := 0
	for _, dev := range DefaultDevices() {
		if !dev.Overheard {
			continue
		}
		overheard++
		if Subnet().Contains(net.ParseIP(dev.IP)) {
			t.Errorf("%s is overheard but inside the demo subnet", dev.IP)
		}
		o, ok := c.observation(dev.MAC, model.FieldIP)
		if !ok || o.Value != dev.IP || o.Confidence != 0.9 || !strings.Contains(o.Method, "overheard") || !strings.Contains(o.Method, "outside 192.168.1.0/24") {
			t.Errorf("overheard %s: %+v ok=%v", dev.IP, o, ok)
		}
	}
	if overheard != 2 {
		t.Errorf("the cast should hold two visitors from the wrong subnet, found %d", overheard)
	}
	last := c.events[len(c.events)-2]
	if last.Kind != engine.KindProgress || last.Done != hosts || last.Total != hosts {
		t.Fatalf("final progress event = %+v", last)
	}
	if len(c.obs) != 2*len(DefaultDevices()) {
		t.Fatalf("emitted %d observations, want ip+mac per device", len(c.obs))
	}
	for _, o := range c.obs {
		if o.Method == "" || !strings.Contains(o.Method, "demo") {
			t.Fatalf("observation must say it is simulated: %+v", o)
		}
		if o.DeviceKey == "" || o.Field == "" || o.Value == "" {
			t.Fatalf("incomplete observation: %+v", o)
		}
	}
}

func TestDiscovererStopsOnCancel(t *testing.T) {
	d := NewDiscoverer(Options{Interval: 10 * time.Millisecond, Seed: 1})
	ctx, cancel := context.WithCancel(context.Background())
	c := &collector{}
	go func() {
		time.Sleep(25 * time.Millisecond)
		cancel()
	}()
	err := d.Run(ctx, netif.Interface{}, c.emit, c.report)
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := c.count(engine.KindSent); got == 0 || got >= hosts {
		t.Fatalf("sent %d before cancel, expected a partial sweep", got)
	}
}

func TestEnrichersAreConsistentWithScript(t *testing.T) {
	ens := NewEnrichers(fast)
	if len(ens) != 3 {
		t.Fatalf("got %d enrichers", len(ens))
	}
	names := map[string]bool{}
	for _, en := range ens {
		names[en.Name()] = true
		if en.Concurrency() < 1 || len(en.Triggers()) == 0 {
			t.Fatalf("%s: bad concurrency/triggers", en.Name())
		}
	}
	for _, want := range []string{"rdns", "mdns", "icmp"} {
		if !names[want] {
			t.Fatalf("missing enricher %q", want)
		}
	}
}

func TestEndToEndThroughEngine(t *testing.T) {
	st := store.NewMemory()
	e := engine.New(st, netif.Interface{})
	if err := e.AddDiscoverer(NewDiscoverer(fast)); err != nil {
		t.Fatal(err)
	}
	reg, err := oui.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range append([]engine.Enricher{oui.New(reg)}, NewEnrichers(fast)...) {
		if err := e.AddEnricher(en); err != nil {
			t.Fatal(err)
		}
	}
	var errs []engine.ProbeEvent
	var mu sync.Mutex
	e.Subscribe(func(ev engine.ProbeEvent) {
		if ev.Kind == engine.KindError {
			mu.Lock()
			errs = append(errs, ev)
			mu.Unlock()
		}
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := e.Status()
		busy := s.Discoverers[0].State == engine.StateRunning
		for _, en := range s.Enrichers {
			busy = busy || en.Running > 0 || en.Queued > 0
		}
		if !busy {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(errs) != 0 {
		t.Fatalf("engine reported errors: %+v", errs)
	}
	if st.Len() != len(DefaultDevices()) {
		t.Fatalf("store has %d devices, want %d", st.Len(), len(DefaultDevices()))
	}
	now := time.Now()

	nas, _ := st.Get("00:11:32:7f:a2:c4")
	if v, _ := nas.ResolvedAt(model.FieldVendor, now); v.Value != "Synology Incorporated" || v.Source != "oui" {
		t.Errorf("nas vendor = %+v", v)
	}
	if h, _ := nas.ResolvedAt(model.FieldHostname, now); h.Value != "synology.local" || h.Source != "mdns" {
		t.Errorf("nas hostname should prefer mdns: %+v", h)
	}
	if svcs := nas.Values(model.FieldService, now); len(svcs) != 3 {
		t.Errorf("nas services = %v", svcs)
	}
	if _, ok := nas.ResolvedAt(model.FieldLatency, now); !ok {
		t.Error("nas has no latency")
	}

	printer, _ := st.Get("00:1e:0b:55:d3:1a")
	if !printer.Conflicting(model.FieldHostname, now) {
		t.Errorf("printer should have a hostname conflict: %v", printer.Values(model.FieldHostname, now))
	}

	phone, _ := st.Get("da:3b:91:0c:44:e2")
	if f, _ := phone.ResolvedAt(model.FieldFlag, now); f.Value != "locally-administered-mac" {
		t.Errorf("phone flag = %+v", f)
	}
	if _, ok := phone.ResolvedAt(model.FieldVendor, now); ok {
		t.Error("randomised MAC must not get a vendor")
	}
	if _, ok := phone.ResolvedAt(model.FieldLatency, now); ok {
		t.Error("silent device must not get a latency")
	}

	for _, key := range []string{"84:cc:a8:01:02:03", "84:cc:a8:04:05:06"} {
		plug, _ := st.Get(key)
		found := false
		for _, v := range plug.Values(model.FieldFlag, now) {
			found = found || v == "duplicate-ip"
		}
		if !found {
			t.Errorf("%s should be flagged duplicate-ip: %v", key, plug.Values(model.FieldFlag, now))
		}
	}
}

func TestReverseName(t *testing.T) {
	if got := reverseName("192.168.1.20"); got != "20.1.168.192.in-addr.arpa" {
		t.Fatalf("reverseName = %q", got)
	}
}
