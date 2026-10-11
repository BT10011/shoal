package netif

import (
	"net"
	"strings"
	"testing"
)

const procRouteFixture = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
wlan0	00000000	0101A8C0	0003	0	0	600	00000000	0	0	0
eth0	00000000	FE00A8C0	0003	0	0	100	00000000	0	0	0
eth0	0000A8C0	00000000	0001	0	0	100	00FFFFFF	0	0	0
docker0	000011AC	00000000	0001	0	0	0	0000FFFF	0	0	0
`

func TestParseProcRoutePicksLowestMetricDefault(t *testing.T) {
	gw, name, err := parseProcRoute(strings.NewReader(procRouteFixture))
	if err != nil {
		t.Fatal(err)
	}
	if name != "eth0" {
		t.Fatalf("iface = %q, want eth0 (metric 100 beats wlan0's 600)", name)
	}
	if !gw.Equal(net.IPv4(192, 168, 0, 254)) {
		t.Fatalf("gateway = %v, want 192.168.0.254", gw)
	}
}

func TestParseProcRouteNoDefault(t *testing.T) {
	in := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
		"eth0\t0000A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n"
	if _, _, err := parseProcRoute(strings.NewReader(in)); err == nil {
		t.Fatal("expected error when only connected routes exist")
	}
	if _, _, err := parseProcRoute(strings.NewReader("")); err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestHosts(t *testing.T) {
	cases := map[string]int{
		"10.0.0.0/24": 254,
		"10.0.0.0/22": 1022,
		"10.0.0.0/31": 2,
		"10.0.0.1/32": 1,
		"10.0.0.0/0":  1 << 30,
	}
	for cidr, want := range cases {
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		if got := (Interface{Subnet: ipnet}).Hosts(); got != want {
			t.Errorf("%s: Hosts = %d, want %d", cidr, got, want)
		}
	}
	if got := (Interface{}).Hosts(); got != 0 {
		t.Errorf("nil subnet: Hosts = %d, want 0", got)
	}
}

func TestDescribeShowsProvenance(t *testing.T) {
	_, ipnet, _ := net.ParseCIDR("192.168.1.0/24")
	i := Interface{
		Name: "en0", Index: 4, MTU: 1500,
		MAC:           net.HardwareAddr{0x3c, 0x22, 0xfb, 0x00, 0x00, 0x01},
		IP:            net.IPv4(192, 168, 1, 42).To4(),
		Subnet:        ipnet,
		Gateway:       net.IPv4(192, 168, 1, 1).To4(),
		GatewaySource: "default route from test",
	}
	out := i.Describe()
	for _, want := range []string{"en0", "3c:22:fb:00:00:01", "192.168.1.42", "192.168.1.0/24", "254 usable hosts", "192.168.1.1", "default route from test"} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains((Interface{Name: "x"}).Describe(), "(none)") {
		t.Error("empty interface should print (none) placeholders")
	}
}

// TestDefaultSmoke exercises the real routing table; it only fails on
// errors that indicate a bug rather than an offline machine.
func TestDefaultSmoke(t *testing.T) {
	i, err := Default()
	if err != nil {
		t.Skipf("no usable interface on this machine: %v", err)
	}
	if i.Name == "" || i.IP == nil || i.Subnet == nil || i.GatewaySource == "" {
		t.Fatalf("incomplete interface: %+v", i)
	}
	if i.Gateway != nil && !i.Subnet.Contains(i.Gateway) {
		t.Logf("note: gateway %v is outside subnet %v (possible on point-to-point links)", i.Gateway, i.Subnet)
	}
	if _, err := ByName(i.Name); err != nil {
		t.Fatalf("ByName(%q): %v", i.Name, err)
	}
	if _, err := ByName("definitely-not-an-interface"); err == nil {
		t.Fatal("ByName of unknown interface should fail")
	}
}

func TestExpandOffersEveryAddressAndGivesTheGatewayToItsSubnet(t *testing.T) {
	ni := net.Interface{Name: "eth0", Index: 2, MTU: 1500, HardwareAddr: net.HardwareAddr{0, 0, 0x5e, 0, 0x53, 1}}
	addrs := []net.Addr{
		&net.IPNet{IP: net.IPv4(172, 16, 10, 5), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("2001:db8::5"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.IPv4(198, 51, 100, 5), Mask: net.CIDRMask(24, 32)},
	}
	gw := net.IPv4(198, 51, 100, 1)

	got := expand(ni, addrs, gw, "eth0", nil)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2 (IPv6 skipped): %+v", len(got), got)
	}
	if got[0].Gateway != nil || !strings.Contains(got[0].GatewaySource, "not in 172.16.10.0/24") {
		t.Errorf("first address should not get the gateway, and say why: %+v", got[0])
	}
	if !got[1].Gateway.Equal(gw) || got[1].Subnet.String() != "198.51.100.0/24" {
		t.Errorf("second address should get the gateway: %+v", got[1])
	}

	other := expand(ni, addrs[:1], gw, "wlan0", nil)
	if other[0].Gateway != nil || !strings.Contains(other[0].GatewaySource, "via wlan0") {
		t.Errorf("an interface the default route does not use gets no gateway: %+v", other[0])
	}
}

func TestSame(t *testing.T) {
	a := Interface{Name: "eth0", IP: net.IPv4(172, 16, 10, 5).To4(), Subnet: &net.IPNet{IP: net.IPv4(172, 16, 10, 0).To4(), Mask: net.CIDRMask(24, 32)}}
	b := a
	b.IP = net.IPv4(172, 16, 10, 5)
	if !a.Same(b) {
		t.Error("the same address in two forms should be the same network")
	}
	b.Name = "eth1"
	if a.Same(b) {
		t.Error("another interface is not the same")
	}
}

func cidr(t *testing.T, s string) *net.IPNet {
	t.Helper()
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return &net.IPNet{IP: ip.To4(), Mask: n.Mask}
}

func TestChooseAddressPicksTheGatewaysSubnet(t *testing.T) {
	cases := []struct {
		name    string
		nets    []string
		gw      string
		want    int
		because string
	}{
		{"gateway on the second", []string{"172.16.10.5/24", "198.51.100.5/24"}, "198.51.100.1", 1, "its subnet holds the gateway 198.51.100.1 (eth0 has 2 IPv4 addresses)"},
		{"gateway on the first", []string{"198.51.100.5/24", "172.16.10.5/24"}, "198.51.100.1", 0, "its subnet holds the gateway 198.51.100.1"},
		{"gateway on none", []string{"172.16.10.5/24", "198.51.100.5/24"}, "192.0.2.1", 0, "first of 2 IPv4 addresses on eth0; none of their subnets holds the gateway 192.0.2.1"},
		{"no gateway", []string{"172.16.10.5/24", "198.51.100.5/24"}, "", 0, "first of 2 IPv4 addresses on eth0; no gateway to choose by"},
		{"only one", []string{"172.16.10.5/24"}, "172.16.10.1", 0, "the only IPv4 address on eth0"},
		{"only one, gateway outside", []string{"172.16.10.5/32"}, "172.16.10.1", 0, "the gateway 172.16.10.1 is outside its subnet"},
		{"narrowest of two that hold it", []string{"172.16.0.5/16", "172.16.10.5/24"}, "172.16.10.1", 1, "its subnet holds the gateway 172.16.10.1"},
	}
	for _, c := range cases {
		var nets []*net.IPNet
		for _, s := range c.nets {
			nets = append(nets, cidr(t, s))
		}
		var gw net.IP
		if c.gw != "" {
			gw = net.ParseIP(c.gw)
		}
		got, why := chooseAddress("eth0", nets, gw)
		if got != c.want {
			t.Errorf("%s: chose %s, want %s", c.name, nets[got], nets[c.want])
		}
		if !strings.Contains(why, c.because) {
			t.Errorf("%s: reason %q, want it to say %q", c.name, why, c.because)
		}
	}
}

// TestFromAddrsScansTheSubnetTheGatewayIsOn is the reported bug: an
// interface holding two addresses, the gateway on the second, used to be
// described by the first.
func TestFromAddrsScansTheSubnetTheGatewayIsOn(t *testing.T) {
	ni := net.Interface{Name: "eth0", Index: 2, MTU: 1500, HardwareAddr: net.HardwareAddr{0, 0, 0x5e, 0, 0x53, 1}}
	addrs := []net.Addr{
		&net.IPNet{IP: net.IPv4(172, 16, 10, 5), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("2001:db8::5"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.IPv4(198, 51, 100, 5), Mask: net.CIDRMask(24, 32)},
	}
	gw := net.IPv4(198, 51, 100, 1)

	i, ok := fromAddrs(ni, addrs, gw)
	if !ok {
		t.Fatal("an interface with IPv4 addresses should be usable")
	}
	if !i.IP.Equal(net.IPv4(198, 51, 100, 5)) || i.Subnet.String() != "198.51.100.0/24" {
		t.Fatalf("scans %s in %s, want 198.51.100.5 in 198.51.100.0/24", i.IP, i.Subnet)
	}
	if out := i.Describe(); !strings.Contains(out, "its subnet holds the gateway 198.51.100.1") {
		t.Errorf("shoal iface should say why this address:\n%s", out)
	}

	// The startup choice and the interface picker agree on which entry
	// carries the gateway.
	for _, c := range expand(ni, addrs, gw, "eth0", nil) {
		if (c.Gateway != nil) != c.Same(i) {
			t.Errorf("picker entry %s (gateway %v) disagrees with the startup choice %s", c.IP, c.Gateway, i.IP)
		}
	}

	if first, _ := fromAddrs(ni, addrs, nil); !first.IP.Equal(net.IPv4(172, 16, 10, 5)) {
		t.Errorf("with no gateway the first address is kept, got %s", first.IP)
	}
	if _, ok := fromAddrs(ni, addrs[1:2], gw); ok {
		t.Error("an interface with only IPv6 has nothing to scan")
	}
}
