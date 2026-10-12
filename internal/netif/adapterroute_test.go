package netif

import (
	"net"
	"testing"
)

func TestPickDefaultRouteTakesTheLowestMetricUpAdapterWithAGateway(t *testing.T) {
	adapters := []adapterRoute{
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
	adapters := []adapterRoute{
		{Index: 11, Up: true, Metric: 25, Gateways: []net.IP{net.IPv4(172, 16, 10, 1)}},
		{Index: 12, Up: true, Metric: 25, Gateways: []net.IP{net.IPv4(198, 51, 100, 1)}},
	}
	if _, index, err := pickDefaultRoute(adapters); err != nil || index != 11 {
		t.Fatalf("picked adapter %d (%v), want the first listed, 11", index, err)
	}
}

func TestPickDefaultRouteSaysWhenThereIsNone(t *testing.T) {
	for _, adapters := range [][]adapterRoute{
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
