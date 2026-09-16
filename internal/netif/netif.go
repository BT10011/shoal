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
	fmt.Fprintf(&b, "ip         %-18s first IPv4 address on %s\n", orNone(i.IP.String()), i.Name)
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
		if i, ok := fromNet(ni); ok {
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
		i, ok := fromNet(*ni)
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
	i, ok := fromNet(*ni)
	if !ok {
		return Interface{}, fmt.Errorf("interface %q is down, loopback or has no IPv4 address", name)
	}
	gw, gwName, err := defaultRoute()
	switch {
	case err != nil:
		i.GatewaySource = fmt.Sprintf("no default route found (%v)", err)
	case gwName != name:
		i.GatewaySource = fmt.Sprintf("default route is via %s, not %s", gwName, name)
	default:
		i.Gateway = gw
		i.GatewaySource = gatewaySource
	}
	return i, nil
}

func fromNet(ni net.Interface) (Interface, bool) {
	if ni.Flags&net.FlagUp == 0 || ni.Flags&net.FlagLoopback != 0 {
		return Interface{}, false
	}
	addrs, err := ni.Addrs()
	if err != nil {
		return Interface{}, false
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil {
			continue
		}
		return Interface{
			Name:   ni.Name,
			Index:  ni.Index,
			MTU:    ni.MTU,
			MAC:    ni.HardwareAddr,
			IP:     ip4,
			Subnet: &net.IPNet{IP: ip4.Mask(ipnet.Mask), Mask: ipnet.Mask},
		}, true
	}
	return Interface{}, false
}
