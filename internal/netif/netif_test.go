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
