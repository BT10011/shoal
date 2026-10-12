//go:build windows

package netif

import (
	"fmt"
	"net"
)

const gatewaySource = "default gateway from the adapter table (GetAdaptersAddresses), lowest metric"

// defaultRoute finds the adapter the IPv4 default route leaves through from
// the gateways configured on each adapter. Its name comes from
// net.InterfaceByIndex, which on Windows is the friendly name ("Ethernet")
// net.Interfaces uses, so the rest of netif finds it by that name.
func defaultRoute() (net.IP, string, error) {
	adapters, err := readAdapters()
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
