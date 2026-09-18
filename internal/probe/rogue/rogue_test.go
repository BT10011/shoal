package rogue

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

func subnet(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func device(key string, ips ...string) model.DeviceSnapshot {
	d := model.NewDevice(key, time.Now())
	for _, ip := range ips {
		d.Add(model.Observation{DeviceKey: key, Field: model.FieldIP, Value: ip, Source: "arp", Method: "ARP request from " + ip + " overheard on test0", Confidence: 0.9, At: time.Now()})
	}
	return d.Snapshot()
}

type capture struct {
	obs    []model.Observation
	events []engine.ProbeEvent
}

func (c *capture) emit(o model.Observation)   { c.obs = append(c.obs, o) }
func (c *capture) report(e engine.ProbeEvent) { c.events = append(c.events, e) }

func TestJudge(t *testing.T) {
	e := New(subnet(t, "172.16.10.0/24"))
	cases := []struct {
		ip, flag, mention string
	}{
		{"172.16.10.42", "", ""},
		{"172.16.10.1", "", ""},
		{"169.254.37.12", FlagLinkLocal, "no DHCP server answers"},
		{"192.168.1.50", FlagOffSubnet, "left over from another network"},
		{"172.16.11.5", FlagOffSubnet, "outside this interface's subnet 172.16.10.0/24"},
	}
	for _, c := range cases {
		flag, why := e.Judge(net.ParseIP(c.ip))
		if flag != c.flag || !strings.Contains(why, c.mention) {
			t.Errorf("%s: flag %q why %q", c.ip, flag, why)
		}
		if flag != "" && !strings.Contains(why, c.ip) {
			t.Errorf("%s: the reasoning must name the address: %q", c.ip, why)
		}
	}
	if flag, _ := New(nil).Judge(net.ParseIP("169.254.1.1")); flag != "" {
		t.Error("with no subnet nothing can be judged")
	}
}

func TestEnrichFlagsEveryLiveAddressThatDoesNotBelong(t *testing.T) {
	e := New(subnet(t, "172.16.10.0/24"))
	c := &capture{}
	// A Dante box with the venue's address and a stale one from the last site.
	if err := e.Enrich(context.Background(), device("00:1d:c1:00:00:01", "172.16.10.77", "192.168.1.50"), c.emit, c.report); err != nil {
		t.Fatal(err)
	}
	if len(c.obs) != 1 {
		t.Fatalf("observations = %+v", c.obs)
	}
	o := c.obs[0]
	if o.Field != model.FieldFlag || o.Value != FlagOffSubnet || o.Confidence != 1 || o.TTL != 0 || o.DeviceKey != "00:1d:c1:00:00:01" {
		t.Fatalf("flag = %+v", o)
	}
	for _, want := range []string{"192.168.1.50 is outside", "Learned from arp: ARP request from 192.168.1.50 overheard on test0"} {
		if !strings.Contains(o.Method, want) {
			t.Errorf("method missing %q: %q", want, o.Method)
		}
	}
	if len(c.events) != 1 || c.events[0].Kind != engine.KindInfo || !strings.HasPrefix(c.events[0].Message, FlagOffSubnet+": 192.168.1.50 is outside") {
		t.Fatalf("events = %+v", c.events)
	}

	c = &capture{}
	if err := e.Enrich(context.Background(), device("00:01:4a:00:00:02", "169.254.37.12"), c.emit, c.report); err != nil {
		t.Fatal(err)
	}
	if len(c.obs) != 1 || c.obs[0].Value != FlagLinkLocal {
		t.Fatalf("link-local device: %+v", c.obs)
	}

	c = &capture{}
	if err := e.Enrich(context.Background(), device("aa:aa:aa:aa:aa:aa", "172.16.10.9"), c.emit, c.report); err != nil {
		t.Fatal(err)
	}
	if len(c.obs) != 0 || len(c.events) != 0 {
		t.Fatalf("a device that belongs must be left alone: %+v %+v", c.obs, c.events)
	}
	if err := New(nil).Enrich(context.Background(), device("k", "169.254.1.1"), c.emit, c.report); err != nil || len(c.obs) != 0 {
		t.Fatal("no subnet, no flags")
	}
}

func TestExpiredAddressesAreNotJudged(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	d := model.NewDevice("k", past)
	d.Add(model.Observation{DeviceKey: "k", Field: model.FieldIP, Value: "169.254.1.1", Source: "mdns", Method: "m", Confidence: 0.9, At: past, TTL: time.Second})
	c := &capture{}
	if err := New(subnet(t, "10.0.0.0/24")).Enrich(context.Background(), d.Snapshot(), c.emit, c.report); err != nil {
		t.Fatal(err)
	}
	if len(c.obs) != 0 {
		t.Fatal("an address that has expired is not the device's any more")
	}
}
