package neigh

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
)

const procARPFixture = `IP address       HW type     Flags       HW address            Mask     Device
10.0.0.1         0x1         0x2         00:00:5e:00:53:02     *        eth0
10.0.0.9         0x1         0x0         00:00:00:00:00:00     *        eth0
10.0.0.20        0x1         0x6         b8:27:eb:6d:2f:90     *        eth0
10.0.0.30        0x1         0x2         3c:22:fb:9e:01:77     *        wlan0
garbage
`

func TestParseProcARP(t *testing.T) {
	entries, err := parseProcARP(strings.NewReader(procARPFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3 (the incomplete one is skipped): %+v", len(entries), entries)
	}
	if entries[0].IP.String() != "10.0.0.1" || entries[0].MAC.String() != "00:00:5e:00:53:02" || entries[0].Interface != "eth0" {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[0].Permanent {
		t.Error("flags 0x2 is complete but not permanent")
	}
	if !entries[1].Permanent {
		t.Error("flags 0x6 includes ATF_PERM")
	}
	if entries[2].Interface != "wlan0" {
		t.Errorf("entry 2 = %+v", entries[2])
	}
	if _, err := parseProcARP(strings.NewReader("")); err == nil {
		t.Error("empty input should error")
	}
}

func TestHostIPs(t *testing.T) {
	_, s24, _ := net.ParseCIDR("10.0.0.0/24")
	ips := hostIPs(s24, net.IPv4(10, 0, 0, 2))
	if len(ips) != 253 || ips[0].String() != "10.0.0.1" || ips[1].String() != "10.0.0.3" || ips[252].String() != "10.0.0.254" {
		t.Fatalf("/24: %d hosts %v", len(ips), ips[:3])
	}
	_, top, _ := net.ParseCIDR("255.255.255.248/29")
	if ips := hostIPs(top, nil); len(ips) != 6 || ips[5].String() != "255.255.255.254" {
		t.Fatalf("top of address space must not wrap: %v", ips)
	}
}

type capture struct {
	mu     sync.Mutex
	obs    []model.Observation
	events []engine.ProbeEvent
}

func (c *capture) emit(o model.Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.obs = append(c.obs, o)
}

func (c *capture) report(e engine.ProbeEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *capture) observation(key string, f model.Field) (model.Observation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, o := range c.obs {
		if o.DeviceKey == key && o.Field == f {
			return o, true
		}
	}
	return model.Observation{}, false
}

func (c *capture) count(kind engine.EventKind) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

var (
	ourMAC = net.HardwareAddr{0x5c, 0xe9, 0x1e, 0x88, 0x60, 0x58}
	ourIP  = net.IPv4(10, 0, 0, 2).To4()
)

func testIface(t *testing.T, cidr string) netif.Interface {
	t.Helper()
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return netif.Interface{Name: "en0", MAC: ourMAC, IP: ourIP, Subnet: subnet}
}

func mac(t *testing.T, s string) net.HardwareAddr {
	t.Helper()
	m, err := net.ParseMAC(s)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunEmitsUsableEntriesOnly(t *testing.T) {
	var nudged []string
	table := []Entry{
		{IP: net.IPv4(10, 0, 0, 1).To4(), MAC: mac(t, "00:00:5e:00:53:02"), Interface: "en0"},
		{IP: net.IPv4(10, 0, 0, 5).To4(), MAC: mac(t, "b8:27:eb:6d:2f:90"), Interface: "en0", Permanent: true},
		{IP: net.IPv4(10, 0, 0, 255).To4(), MAC: mac(t, "ff:ff:ff:ff:ff:ff"), Interface: "en0"},  // broadcast
		{IP: net.IPv4(224, 0, 0, 251).To4(), MAC: mac(t, "01:00:5e:00:00:fb"), Interface: "en0"}, // multicast
		{IP: net.IPv4(10, 0, 0, 6).To4(), MAC: mac(t, "00:00:00:00:00:00"), Interface: "en0"},    // incomplete
		{IP: net.IPv4(10, 0, 0, 7).To4(), MAC: mac(t, "3c:22:fb:9e:01:77"), Interface: "wlan9"},  // other interface
		{IP: net.IPv4(192, 168, 9, 9).To4(), MAC: mac(t, "00:0e:58:ab:cd:ef"), Interface: "en0"}, // off subnet
		{IP: ourIP, MAC: ourMAC, Interface: "en0"},                                               // ourselves
	}
	c := &capture{}
	d := New(Options{
		Table:  func() ([]Entry, error) { return table, nil },
		Nudge:  func(ip net.IP) error { nudged = append(nudged, ip.String()); return nil },
		Rate:   5000,
		Settle: time.Millisecond,
	})
	if err := d.Run(context.Background(), testIface(t, "10.0.0.0/29"), c.emit, c.report); err != nil {
		t.Fatal(err)
	}

	if strings.Join(nudged, " ") != "10.0.0.1 10.0.0.3 10.0.0.4 10.0.0.5 10.0.0.6" {
		t.Fatalf("nudged %v", nudged)
	}
	if c.count(engine.KindReceived) != 2 {
		t.Fatalf("received events = %d, want 2 usable entries", c.count(engine.KindReceived))
	}

	got, ok := c.observation("00:00:5e:00:53:02", model.FieldIP)
	if !ok || got.Value != "10.0.0.1" || got.Confidence != 0.8 {
		t.Fatalf("gateway ip = %+v ok=%v", got, ok)
	}
	for _, want := range []string{"kernel neighbour cache on en0", "did not see the ARP exchange", "UDP datagram to prompt"} {
		if !strings.Contains(got.Method, want) {
			t.Errorf("method %q missing %q", got.Method, want)
		}
	}
	if perm, _ := c.observation("b8:27:eb:6d:2f:90", model.FieldIP); !strings.Contains(perm.Method, "permanent") {
		t.Errorf("permanent entry should say so: %q", perm.Method)
	}
	for _, key := range []string{"ff:ff:ff:ff:ff:ff", "01:00:5e:00:00:fb", "00:00:00:00:00:00", "3c:22:fb:9e:01:77", "00:0e:58:ab:cd:ef"} {
		if _, ok := c.observation(key, model.FieldIP); ok {
			t.Errorf("%s should have been filtered out", key)
		}
	}
	self, ok := c.observation(ourMAC.String(), model.FieldFlag)
	if !ok || self.Value != FlagSelf || self.Source != "netif" {
		t.Fatalf("self flag = %+v ok=%v", self, ok)
	}
	if ip, _ := c.observation(ourMAC.String(), model.FieldIP); ip.Source != "netif" {
		t.Error("our own address must come from netif, not the cache")
	}
}

func TestRunWithoutNudging(t *testing.T) {
	c := &capture{}
	d := New(Options{
		Table: func() ([]Entry, error) {
			return []Entry{{IP: net.IPv4(10, 0, 0, 1).To4(), MAC: mac(t, "00:00:5e:00:53:02"), Interface: "en0"}}, nil
		},
		Nudge:   func(net.IP) error { t.Fatal("must not nudge"); return nil },
		NoNudge: true,
	})
	if err := d.Run(context.Background(), testIface(t, "10.0.0.0/29"), c.emit, c.report); err != nil {
		t.Fatal(err)
	}
	if c.count(engine.KindSent) != 0 {
		t.Fatal("no datagrams should be sent")
	}
	got, _ := c.observation("00:00:5e:00:53:02", model.FieldIP)
	if !strings.Contains(got.Method, "for its own traffic") {
		t.Fatalf("method should not claim a nudge: %q", got.Method)
	}
}

func TestRunReportsNudgeFailures(t *testing.T) {
	c := &capture{}
	d := New(Options{
		Table:  func() ([]Entry, error) { return nil, nil },
		Nudge:  func(net.IP) error { return errors.New("network is unreachable") },
		Rate:   5000,
		Settle: time.Millisecond,
	})
	if err := d.Run(context.Background(), testIface(t, "10.0.0.0/30"), c.emit, c.report); err != nil {
		t.Fatal(err)
	}
	if c.count(engine.KindSent) != 0 {
		t.Fatal("failed nudges must not be reported as sent")
	}
	found := false
	for _, e := range c.events {
		found = found || strings.Contains(e.Message, "nudge failed: network is unreachable")
	}
	if !found {
		t.Fatalf("no failure event in %+v", c.events)
	}
}

func TestRunRefusesBadInterfaceAndHugeSubnet(t *testing.T) {
	c := &capture{}
	d := New(Options{Table: func() ([]Entry, error) { return nil, nil }, Nudge: func(net.IP) error { return nil }})
	if err := d.Run(context.Background(), netif.Interface{Name: "utun0"}, c.emit, c.report); err == nil {
		t.Error("interface without a subnet should be rejected")
	}
	err := d.Run(context.Background(), testIface(t, "10.0.0.0/16"), c.emit, c.report)
	if err == nil || !strings.Contains(err.Error(), "limit of 1022") {
		t.Errorf("err = %v", err)
	}
}

func TestRunSurfacesTableErrors(t *testing.T) {
	c := &capture{}
	d := New(Options{
		Table:  func() ([]Entry, error) { return nil, errors.New("sysctl failed") },
		Nudge:  func(net.IP) error { return nil },
		Rate:   5000,
		Settle: time.Millisecond,
	})
	err := d.Run(context.Background(), testIface(t, "10.0.0.0/30"), c.emit, c.report)
	if err == nil || !strings.Contains(err.Error(), "sysctl failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &capture{}
	d := New(Options{
		Table:  func() ([]Entry, error) { return nil, nil },
		Nudge:  func(net.IP) error { return nil },
		Rate:   50,
		Settle: time.Hour,
	})
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, testIface(t, "10.0.0.0/24"), c.emit, c.report) }()
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestTableSmoke reads the real neighbour cache; it only fails on errors
// that indicate a bug rather than an empty table.
func TestTableSmoke(t *testing.T) {
	entries, err := Table()
	if err != nil {
		t.Skipf("cannot read the neighbour cache here: %v", err)
	}
	for _, e := range entries {
		if e.IP.To4() == nil || len(e.MAC) != 6 {
			t.Fatalf("malformed entry %+v", e)
		}
	}
	t.Logf("%d entries via %s", len(entries), TableSource)
}
