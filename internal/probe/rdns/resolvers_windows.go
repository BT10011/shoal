//go:build windows

package rdns

import (
	"fmt"
	"net"

	"github.com/BT10011/shoal/internal/netif"
)

// ResolverSource names where the system's DNS servers are read from, for
// messages saying none was found there.
const ResolverSource = "the network adapters' DNS settings"

// systemResolvers reads the DNS servers set on the up adapters, IPv4 first:
// Windows has no resolv.conf.
func systemResolvers() []Resolver {
	servers, err := netif.Nameservers()
	if err != nil {
		return nil
	}
	resolvers := make([]Resolver, 0, len(servers))
	for _, s := range servers {
		resolvers = append(resolvers, Resolver{
			Addr:   net.JoinHostPort(s.IP.String(), DefaultPort),
			Origin: fmt.Sprintf("DNS server set on %s (GetAdaptersAddresses)", s.Adapter),
		})
	}
	return resolvers
}
