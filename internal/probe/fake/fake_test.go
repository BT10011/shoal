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

func demoIface(t *testing.T, name string) netif.Interface {
	t.Helper()
	for _, iface := range Interfaces() {
		if iface.Name == name {
			return iface
		}
	}
	t.Fatalf("no demo interface %s", name)
	return netif.Interface{}
}

// The picker's list: the demo starts on its first entry, and each network
// that can be swept has a cast inside its own subnet. The VPN cannot be.
func TestInterfacesEachHaveTheirOwnNetwork(t *testing.T) {
	list := Interfaces()
	if len(list) < 2 || !list[0].Same(Interface()) {
		t.Fatalf("Interfaces() = %+v, want the interface the demo starts on first", list)
	}
	noMAC := 0
	for _, iface := range list {
		if !iface.Subnet.Contains(iface.IP) {
			t.Errorf("%s: %s is outside its own subnet %s", iface.Name, iface.IP, iface.Subnet)
		}
		if len(iface.MAC) != 6 {
			noMAC++
			continue
		}
		cast := DevicesFor(iface)
		if len(cast) == 0 {
			t.Errorf("%s has no devices", iface.Name)
		}
		for _, dev := range cast {
			if !dev.Overheard && !iface.Subnet.Contains(net.ParseIP(dev.IP)) {
				t.Errorf("%s: %s is outside %s", iface.Name, dev.IP, iface.Subnet)
			}
		}
	}
	if noMAC != 1 {
		t.Errorf("want one interface with no MAC, to show one ARP cannot sweep; found %d", noMAC)
	}
	if cast := DevicesFor(netif.Interface{Name: "en99"}); cast == nil || len(cast) != 0 {
		t.Errorf("an unknown network's cast = %v, want empty, and not nil, which means the default cast", cast)
	}
}

func TestDiscovererSweepsTheInterfaceItRunsOn(t *testing.T) {
	iface := demoIface(t, "en7")
	opts := fast
	opts.Devices = DevicesFor(iface)
	c := &collector{}
	if err := NewDiscoverer(opts).Run(context.Background(), iface, c.emit, c.report); err != nil {
		t.Fatal(err)
	}
	if got := c.count(engine.KindSent); got != 254 {
		t.Errorf("sent %d requests, want 254 for %s", got, iface.Subnet)
	}
	if got := c.count(engine.KindReceived); got != len(opts.Devices) {
		t.Errorf("got %d replies, want %d", got, len(opts.Devices))
	}
	if first := c.events[1]; first.Kind != engine.KindSent || first.Target != "172.16.10.1" {
		t.Errorf("first request = %+v, want who-has 172.16.10.1", first)
	}
}

// Sweeping only the /24 around the machine finds what is in it, and none
// of the rest of the subnet.
func TestDiscovererSweepsOnlyItsRange(t *testing.T) {
	iface := demoIface(t, "en8")
	opts := fast
	opts.Devices = DevicesFor(iface)
	_, opts.Range, _ = net.ParseCIDR("172.20.4.0/24")
	c := &collector{}
	if err := NewDiscoverer(opts).Run(context.Background(), iface, c.emit, c.report); err != nil {
		t.Fatal(err)
	}
	if got := c.count(engine.KindSent); got != 254 {
		t.Errorf("sent %d requests, want 254 for %s", got, opts.Range)
	}
	inside := 0
	for _, dev := range opts.Devices {
		_, found := c.observation(dev.MAC, model.FieldIP)
		want := opts.Range.Contains(net.ParseIP(dev.IP))
		if found != want {
			t.Errorf("%s: found = %v, want %v", dev.IP, found, want)
		}
		if want {
			inside++
		}
	}
	if inside == 0 || inside == len(opts.Devices) {
		t.Errorf("%d of %d devices are in %s; want some in and some out, so the range shows", inside, len(opts.Devices), opts.Range)
	}
}

// Listening asks nothing and has no total: the devices are overheard one
// by one, and the listener carries on until its scan ends.
func TestDiscovererListensWithoutSweeping(t *testing.T) {
	iface := demoIface(t, "en8")
	opts := fast
	opts.Devices = DevicesFor(iface)
	opts.Listen = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &collector{}
	done := make(chan error, 1)
	go func() { done <- NewDiscoverer(opts).Run(ctx, iface, c.emit, c.report) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		c.mu.Lock()
		n := len(c.obs)
		c.mu.Unlock()
		if n == 2*len(opts.Devices) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("overheard %d observations, want ip and mac for each of %d devices", n, len(opts.Devices))
		}
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned %v before its scan ended; a listener keeps listening", err)
	default:
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got := c.count(engine.KindSent); got != 0 {
		t.Errorf("sent %d requests, want none: listening asks nothing", got)
	}
	for _, e := range c.events {
		if e.Kind == engine.KindProgress {
			t.Errorf("progress %+v: a listener has no total", e)
		}
	}
}

func TestHostRange(t *testing.T) {
	for _, tc := range []struct {
		cidr, first string
		n           int
	}{
		{"192.168.1.0/24", "192.168.1.1", 254},
		{"172.20.0.0/16", "172.20.0.1", 65534},
		{"192.0.2.6/31", "192.0.2.6", 2},
		{"198.51.100.7/32", "198.51.100.7", 1},
	} {
		_, n, _ := net.ParseCIDR(tc.cidr)
		first, count := hostRange(n)
		if ipv4(first) != tc.first || count != tc.n {
			t.Errorf("%s: %s and %d hosts, want %s and %d", tc.cidr, ipv4(first), count, tc.first, tc.n)
		}
	}
}
