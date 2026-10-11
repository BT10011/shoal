// Package netif chooses the network interface to scan and describes it:
// its addresses, subnet and default gateway, and how each was learned.
package netif

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// Interface is the scan target: one interface, its IPv4 subnet and gateway.
type Interface struct {
	Name          string
	Index         int
	MTU           int
	MAC           net.HardwareAddr
	IP            net.IP
	AddressSource string // why this address, when the interface holds several
	Subnet        *net.IPNet
	Gateway       net.IP
	GatewaySource string
}

// Hosts returns the number of usable host addresses in the subnet.
func (i Interface) Hosts() int {
	if i.Subnet == nil {
		return 0
	}
	ones, bits := i.Subnet.Mask.Size()
	switch {
	case bits-ones >= 31:
		return 1 << 30
	case ones >= 31:
		return 1 << (bits - ones)
	default:
		return (1 << (bits - ones)) - 2
	}
}

// Describe renders the interface and where each value came from.
func (i Interface) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "interface  %-18s index %d, mtu %d\n", i.Name, i.Index, i.MTU)
	fmt.Fprintf(&b, "mac        %-18s interface hardware address\n", orNone(i.MAC.String()))
	why := i.AddressSource
	if why == "" {
		why = "IPv4 address on " + i.Name
	}
	fmt.Fprintf(&b, "ip         %-18s %s\n", orNone(i.IP.String()), why)
	if i.Subnet != nil {
		fmt.Fprintf(&b, "subnet     %-18s %d usable hosts\n", i.Subnet.String(), i.Hosts())
	} else {
		fmt.Fprintf(&b, "subnet     %-18s\n", "(none)")
	}
	if i.Gateway != nil {
		fmt.Fprintf(&b, "gateway    %-18s %s\n", i.Gateway.String(), i.GatewaySource)
	} else {
		fmt.Fprintf(&b, "gateway    %-18s %s\n", "(none)", i.GatewaySource)
	}
	return b.String()
}

func orNone(s string) string {
	if s == "" || s == "<nil>" {
		return "(none)"
	}
	return s
}

// ErrNoInterface is returned when no usable interface exists.
var ErrNoInterface = errors.New("no up, non-loopback interface with an IPv4 address")

// List returns every up, non-loopback interface that has an IPv4 address.
// Gateways are not resolved; use Default or ByName for that.
func List() ([]Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []Interface
	for _, ni := range ifaces {
		if i, ok := fromNet(ni, nil); ok {
			out = append(out, i)
		}
	}
	return out, nil
}

// Default returns the interface carrying the default IPv4 route. If no
// default route can be found, it falls back to the first candidate from List
// and says so in GatewaySource.
func Default() (Interface, error) {
	gw, name, err := defaultRoute()
	if err == nil {
		ni, err := net.InterfaceByName(name)
		if err != nil {
			return Interface{}, fmt.Errorf("default route interface %q: %w", name, err)
		}
		i, ok := fromNet(*ni, gw)
		if !ok {
			return Interface{}, fmt.Errorf("default route interface %q has no IPv4 address", name)
		}
		i.Gateway = gw
		i.GatewaySource = gatewaySource
		return i, nil
	}

	candidates, lerr := List()
	if lerr != nil {
		return Interface{}, lerr
	}
	if len(candidates) == 0 {
		return Interface{}, ErrNoInterface
	}
	i := candidates[0]
	i.GatewaySource = fmt.Sprintf("no default route found (%v); picked first usable interface", err)
	return i, nil
}

// ByName describes a specific interface, attaching the gateway only if the
// default route goes through it.
func ByName(name string) (Interface, error) {
	ni, err := net.InterfaceByName(name)
	if err != nil {
		return Interface{}, err
	}
	gw, gwName, rerr := defaultRoute()
	var via net.IP // the gateway, only if the default route leaves through this interface
	if rerr == nil && gwName == name {
		via = gw
	}
	i, ok := fromNet(*ni, via)
	if !ok {
		return Interface{}, fmt.Errorf("interface %q is down, loopback or has no IPv4 address", name)
	}
	switch {
	case rerr != nil:
		i.GatewaySource = fmt.Sprintf("no default route found (%v)", rerr)
	case gwName != name:
		i.GatewaySource = fmt.Sprintf("default route is via %s, not %s", gwName, name)
	default:
		i.Gateway = gw
		i.GatewaySource = gatewaySource
	}
	return i, nil
}

// Candidates lists every network shoal could scan from this machine: one
// entry per IPv4 address on every up, non-loopback interface, so an
// interface holding two addresses offers both networks. An entry gets the
// default gateway only when the default route leaves through its interface
// and the gateway is inside its subnet; GatewaySource says why otherwise.
func Candidates() ([]Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	gw, gwName, rerr := defaultRoute()
	var out []Interface
	for _, ni := range ifaces {
		if ni.Flags&net.FlagUp == 0 || ni.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ni.Addrs()
		if err != nil {
			continue
		}
		out = append(out, expand(ni, addrs, gw, gwName, rerr)...)
	}
	return out, nil
}

// expand turns one interface and its addresses into candidates, attaching
// the gateway to the address whose subnet holds it.
func expand(ni net.Interface, addrs []net.Addr, gw net.IP, gwName string, routeErr error) []Interface {
	var out []Interface
	for _, n := range ipv4Nets(addrs) {
		i := newInterface(ni, n)
		switch {
		case routeErr != nil:
			i.GatewaySource = fmt.Sprintf("no default route found (%v)", routeErr)
		case gwName != ni.Name:
			i.GatewaySource = fmt.Sprintf("default route is via %s, not %s", gwName, ni.Name)
		case !i.Subnet.Contains(gw):
			i.GatewaySource = fmt.Sprintf("default route leaves through %s, but its gateway %s is not in %s", ni.Name, gw, i.Subnet)
		default:
			i.Gateway = gw
			i.GatewaySource = gatewaySource
		}
		out = append(out, i)
	}
	return out
}

// Same reports whether two descriptions are the same network on the same
// interface: one name, one address, one subnet.
func (i Interface) Same(o Interface) bool {
	return i.Name == o.Name && i.IP.Equal(o.IP) && i.Subnet.String() == o.Subnet.String()
}

// fromNet describes an interface by the one IPv4 address shoal should scan
// from: the one whose subnet holds gw, if gw is given (see chooseAddress).
func fromNet(ni net.Interface, gw net.IP) (Interface, bool) {
	if ni.Flags&net.FlagUp == 0 || ni.Flags&net.FlagLoopback != 0 {
		return Interface{}, false
	}
	addrs, err := ni.Addrs()
	if err != nil {
		return Interface{}, false
	}
	return fromAddrs(ni, addrs, gw)
}

// fromAddrs is fromNet without the system calls, so it can be tested.
func fromAddrs(ni net.Interface, addrs []net.Addr, gw net.IP) (Interface, bool) {
	nets := ipv4Nets(addrs)
	if len(nets) == 0 {
		return Interface{}, false
	}
	n, why := chooseAddress(ni.Name, nets, gw)
	i := newInterface(ni, nets[n])
	i.AddressSource = why
	return i, true
}

// chooseAddress picks which of an interface's IPv4 addresses to scan from.
// An interface can hold addresses on several networks, and the OS lists
// them in no meaningful order, so the first is not necessarily the network
// the gateway is on. The address whose subnet holds the gateway is chosen,
// the narrowest if several do, as routing would. With no gateway, or none
// of the subnets holding it, the first is kept. Either way the reason comes
// back, so the choice is never silent. nets must not be empty.
func chooseAddress(name string, nets []*net.IPNet, gw net.IP) (int, string) {
	best := -1
	if gw != nil {
		for k, n := range nets {
			if !n.Contains(gw) {
				continue
			}
			if best < 0 || prefixLen(n) > prefixLen(nets[best]) {
				best = k
			}
		}
	}
	switch {
	case len(nets) == 1 && (gw == nil || best == 0):
		return 0, "the only IPv4 address on " + name
	case len(nets) == 1:
		return 0, fmt.Sprintf("the only IPv4 address on %s; the gateway %s is outside its subnet", name, gw)
	case best >= 0:
		return best, fmt.Sprintf("its subnet holds the gateway %s (%s has %d IPv4 addresses)", gw, name, len(nets))
	case gw == nil:
		return 0, fmt.Sprintf("first of %d IPv4 addresses on %s; no gateway to choose by", len(nets), name)
	default:
		return 0, fmt.Sprintf("first of %d IPv4 addresses on %s; none of their subnets holds the gateway %s", len(nets), name, gw)
	}
}

func prefixLen(n *net.IPNet) int {
	ones, _ := n.Mask.Size()
	return ones
}

// ipv4Nets keeps the IPv4 addresses among addrs, in the order listed.
func ipv4Nets(addrs []net.Addr) []*net.IPNet {
	var out []*net.IPNet
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil {
			out = append(out, &net.IPNet{IP: ip4, Mask: ipnet.Mask})
		}
	}
	return out
}

// newInterface describes one address on an interface, without a gateway.
func newInterface(ni net.Interface, n *net.IPNet) Interface {
	return Interface{
		Name:   ni.Name,
		Index:  ni.Index,
		MTU:    ni.MTU,
		MAC:    ni.HardwareAddr,
		IP:     n.IP,
		Subnet: &net.IPNet{IP: n.IP.Mask(n.Mask), Mask: n.Mask},
	}
}
