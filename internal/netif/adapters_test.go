package netif

import (
	"net"
	"testing"
)

func TestPickDefaultRouteTakesTheLowestMetricUpAdapterWithAGateway(t *testing.T) {
	adapters := []adapter{
		{Index: 3, Up: false, Metric: 5, Gateways: []net.IP{net.IPv4(192, 0, 2, 1)}},                           // down
		{Index: 4, Up: true, Metric: 1},                                                                        // no gateway
		{Index: 5, Up: true, Metric: 2, Gateways: []net.IP{net.ParseIP("fe80::1")}},                            // IPv6 only
		{Index: 6, Up: true, Metric: 3, Gateways: []net.IP{net.IPv4zero}},                                      // on-link default
		{Index: 7, Up: true, Metric: 35, Gateways: []net.IP{net.IPv4(198, 51, 100, 1)}},                        // wifi
		{Index: 8, Up: true, Metric: 25, Gateways: []net.IP{net.ParseIP("fe80::1"), net.IPv4(172, 16, 10, 1)}}, // wired
	}
	gw, index, err := pickDefaultRoute(adapters)
	if err != nil {
		t.Fatal(err)
	}
	if index != 8 || !gw.Equal(net.IPv4(172, 16, 10, 1)) {
		t.Fatalf("picked %v on adapter %d, want 172.16.10.1 on 8: the lowest metric among up adapters with an IPv4 gateway", gw, index)
	}
	if len(gw) != net.IPv4len {
		t.Errorf("gateway is %d bytes, want the 4-byte form the rest of netif compares", len(gw))
	}
}

func TestPickDefaultRouteKeepsTheFirstOnATie(t *testing.T) {
	adapters := []adapter{
		{Index: 11, Up: true, Metric: 25, Gateways: []net.IP{net.IPv4(172, 16, 10, 1)}},
		{Index: 12, Up: true, Metric: 25, Gateways: []net.IP{net.IPv4(198, 51, 100, 1)}},
	}
	if _, index, err := pickDefaultRoute(adapters); err != nil || index != 11 {
		t.Fatalf("picked adapter %d (%v), want the first listed, 11", index, err)
	}
}

func TestPickDefaultRouteSaysWhenThereIsNone(t *testing.T) {
	for _, adapters := range [][]adapter{
		nil,
		{{Index: 3, Up: true}},
		{{Index: 3, Up: false, Gateways: []net.IP{net.IPv4(192, 0, 2, 1)}}},
		{{Index: 3, Up: true, Gateways: []net.IP{net.IPv4zero, net.ParseIP("fe80::1")}}},
	} {
		if gw, index, err := pickDefaultRoute(adapters); err == nil {
			t.Errorf("%+v: picked %v on %d, want an error", adapters, gw, index)
		}
	}
}

func TestOrderNameserversAsksIPv4FirstByAdapterMetric(t *testing.T) {
	adapters := []adapter{
		{Index: 7, Name: "Wi-Fi", Up: true, Metric: 35, Nameservers: []net.IP{
			net.IPv4(198, 51, 100, 53),
			net.ParseIP("2001:db8::53"),
		}},
		{Index: 3, Name: "Old NIC", Up: false, Metric: 1, Nameservers: []net.IP{net.IPv4(192, 0, 2, 53)}},
		{Index: 8, Name: "Ethernet", Up: true, Metric: 25, Nameservers: []net.IP{
			net.ParseIP("fec0:0:0:ffff::1"), // Windows' placeholder: never answers
			net.ParseIP("fe80::1"),          // link-local: needs a zone
			net.IPv4(172, 16, 10, 53),
			net.IPv4(172, 16, 10, 54),
			net.IPv4(198, 51, 100, 53), // also on Wi-Fi: asked once
		}},
	}
	got := orderNameservers(adapters)
	want := []Nameserver{
		{IP: net.IPv4(172, 16, 10, 53).To4(), Adapter: "Ethernet"},
		{IP: net.IPv4(172, 16, 10, 54).To4(), Adapter: "Ethernet"},
		{IP: net.IPv4(198, 51, 100, 53).To4(), Adapter: "Ethernet"},
		{IP: net.ParseIP("2001:db8::53"), Adapter: "Wi-Fi"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].IP.Equal(want[i].IP) || got[i].Adapter != want[i].Adapter {
			t.Errorf("server %d = %v on %q, want %v on %q", i, got[i].IP, got[i].Adapter, want[i].IP, want[i].Adapter)
		}
	}
	if len(got[0].IP) != net.IPv4len {
		t.Errorf("an IPv4 server is %d bytes, want 4", len(got[0].IP))
	}
}

func TestOrderNameserversWithNoneSet(t *testing.T) {
	adapters := []adapter{
		{Index: 8, Name: "Ethernet", Up: true, Nameservers: []net.IP{
			net.ParseIP("fec0:0:0:ffff::1"), net.ParseIP("fec0:0:0:ffff::2"), net.ParseIP("fec0:0:0:ffff::3"),
		}},
	}
	if got := orderNameservers(adapters); len(got) != 0 {
		t.Fatalf("got %v, want none: placeholders are not servers", got)
	}
}
