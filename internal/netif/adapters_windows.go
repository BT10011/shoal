//go:build windows

package netif

import (
	"errors"
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// readAdapters reads each IPv4 adapter's names, state, metric, gateways and
// DNS servers from the adapter table.
func readAdapters() ([]adapter, error) {
	const flags = windows.GAA_FLAG_INCLUDE_GATEWAYS | windows.GAA_FLAG_SKIP_ANYCAST |
		windows.GAA_FLAG_SKIP_MULTICAST
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
		var out []adapter
		for a := first; a != nil; a = a.Next {
			ad := adapter{
				Index:  int(a.IfIndex),
				Name:   windows.UTF16PtrToString(a.FriendlyName),
				GUID:   windows.BytePtrToString(a.AdapterName),
				Up:     a.OperStatus == windows.IfOperStatusUp,
				Metric: a.Ipv4Metric,
			}
			for g := a.FirstGatewayAddress; g != nil; g = g.Next {
				ad.Gateways = appendIP(ad.Gateways, g.Address)
			}
			for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
				ad.Nameservers = appendIP(ad.Nameservers, d.Address)
			}
			out = append(out, ad)
		}
		return out, nil
	}
	return nil, errors.New("GetAdaptersAddresses: the adapter table kept growing")
}

// appendIP appends a copy of the address, since it points into the table's
// buffer.
func appendIP(ips []net.IP, sa windows.SocketAddress) []net.IP {
	if ip := sa.IP(); ip != nil {
		return append(ips, append(net.IP(nil), ip...))
	}
	return ips
}

// Nameservers returns the DNS servers configured on the up adapters, in the
// order to ask them (see orderNameservers). Windows has no resolv.conf; this
// is what the rdns probe reads instead.
func Nameservers() ([]Nameserver, error) {
	adapters, err := readAdapters()
	if err != nil {
		return nil, err
	}
	return orderNameservers(adapters), nil
}

// AdapterGUID returns the GUID Windows knows an interface by, braces
// included. Npcap names its capture device after it: \Device\NPF_{GUID}.
func AdapterGUID(index int) (string, error) {
	adapters, err := readAdapters()
	if err != nil {
		return "", err
	}
	for _, a := range adapters {
		if a.Index == index && a.GUID != "" {
			return a.GUID, nil
		}
	}
	return "", fmt.Errorf("no IPv4 adapter has interface index %d", index)
}
