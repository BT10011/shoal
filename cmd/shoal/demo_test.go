package main

import (
	"strings"
	"testing"

	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/probe/fake"
	"github.com/BT10011/shoal/internal/store"
	"github.com/BT10011/shoal/internal/ui"
)

func demoIface(t *testing.T, name string) netif.Interface {
	t.Helper()
	for _, iface := range fake.Interfaces() {
		if iface.Name == name {
			return iface
		}
	}
	t.Fatalf("no demo interface %s", name)
	return netif.Interface{}
}

// Choosing a network in the demo goes the way it would on a real machine:
// a scan, a question about a large subnet, or a refusal for an interface
// ARP cannot sweep.
func TestDemoOpensItsNetworksAsARealScanWould(t *testing.T) {
	h, err := store.OpenHistory("")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	d := demo{history: h}

	for _, name := range []string{"en0", "en7"} {
		s, large, err := d.open(demoIface(t, name), ui.SweepAsk)
		if err != nil || large != nil || s.Engine == nil || s.Store == nil || s.Iface.Name != name {
			t.Errorf("%s: session %+v, large %+v, err %v; want a session", name, s, large, err)
		}
	}

	en8 := demoIface(t, "en8")
	s, large, err := d.open(en8, ui.SweepAsk)
	if err != nil || s.Engine != nil || large == nil {
		t.Fatalf("en8: session %+v, large %+v, err %v; want the large-network question", s, large, err)
	}
	if large.Hosts != 65534 || large.Local.String() != "172.20.4.0/24" || large.Rate != largeRate {
		t.Errorf("en8 large = %+v", large)
	}
	for _, choice := range []ui.SweepChoice{ui.SweepAll, ui.SweepLocal, ui.SweepNone} {
		if s, large, err := d.open(en8, choice); err != nil || large != nil || s.Engine == nil {
			t.Errorf("en8, choice %d: session %+v, large %+v, err %v; want a session", choice, s, large, err)
		}
	}

	if _, _, err := d.open(demoIface(t, "utun4"), ui.SweepAsk); err == nil || !strings.Contains(err.Error(), "no Ethernet MAC") {
		t.Errorf("utun4: err = %v, want the refusal a real interface without a MAC gets", err)
	}
}
