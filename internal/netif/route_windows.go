//go:build windows

package netif

import (
	"errors"
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

const gatewaySource = "default gateway from the adapter table (GetAdaptersAddresses), lowest metric"

// defaultRoute finds the adapter the IPv4 default route leaves through from
// the gateways configured on each adapter. Its name comes from
// net.InterfaceByIndex, which on Windows is the friendly name ("Ethernet")
// net.Interfaces uses, so the rest of netif finds it by that name.
func defaultRoute() (net.IP, string, error) {
	adapters, err := adapterRoutes()
	if err != nil {
		return nil, "", err
	}
	gateway, index, err := pickDefaultRoute(adapters)
	if err != nil {
		return nil, "", err
	}
	ni, err := net.InterfaceByIndex(index)
	if err != nil {
		return nil, "", fmt.Errorf("adapter %d: %w", index, err)
	}
	return gateway, ni.Name, nil
}

// adapterRoutes reads each IPv4 adapter's state, metric and gateways.
func adapterRoutes() ([]adapterRoute, error) {
	const flags = windows.GAA_FLAG_INCLUDE_GATEWAYS | windows.GAA_FLAG_SKIP_ANYCAST |
		windows.GAA_FLAG_SKIP_MULTICAST | windows.GAA_FLAG_SKIP_DNS_SERVER
	// The table's size is not known in advance: start where Microsoft
	// suggests and grow to what the call says it needs, which can change
	// between calls if an adapter appears.
	size := uint32(15 << 10)
	for range 3 {
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_INET, flags, 0, first, &size)
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("GetAdaptersAddresses: %w", err)
		}
		var out []adapterRoute
		for a := first; a != nil; a = a.Next {
			r := adapterRoute{Index: int(a.IfIndex), Up: a.OperStatus == windows.IfOperStatusUp, Metric: a.Ipv4Metric}
			for g := a.FirstGatewayAddress; g != nil; g = g.Next {
				if ip := g.Address.IP(); ip != nil {
					r.Gateways = append(r.Gateways, append(net.IP(nil), ip...)) // ip points into buf
				}
			}
			out = append(out, r)
		}
		return out, nil
	}
	return nil, errors.New("GetAdaptersAddresses: the adapter table kept growing")
}
