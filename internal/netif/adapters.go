package netif

import (
	"errors"
	"net"
	"sort"
)

// adapter is what Windows' adapter table says about one adapter: its name,
// whether it is up, its IPv4 metric, and the gateways and DNS servers
// configured on it. It is kept apart from the Windows call that fills it,
// so the choices below are tested on every platform.
type adapter struct {
	Index       int
	Name        string // the friendly name, as net.Interfaces gives it
	Up          bool
	Metric      uint32 // the interface's IPv4 metric; lower is preferred
	Gateways    []net.IP
	Nameservers []net.IP
}

// pickDefaultRoute chooses the adapter the IPv4 default route leaves
// through: an up adapter with an IPv4 gateway, the lowest metric winning
// and the first listed on a tie, as Windows' own route choice does when the
// routes' metrics are equal. It returns the gateway and the adapter's index.
func pickDefaultRoute(adapters []adapter) (net.IP, int, error) {
	var (
		gateway net.IP
		index   int
		best    uint32
	)
	for _, a := range adapters {
		if !a.Up {
			continue
		}
		for _, g := range a.Gateways {
			// 0.0.0.0 is how a point-to-point link, such as some VPNs,
			// says its default route is on-link: there is no gateway to name.
			g4 := g.To4()
			if g4 == nil || g4.IsUnspecified() {
				continue
			}
			if gateway == nil || a.Metric < best {
				gateway, index, best = g4, a.Index, a.Metric
			}
			break
		}
	}
	if gateway == nil {
		return nil, 0, errors.New("no up adapter has an IPv4 gateway")
	}
	return gateway, index, nil
}

// Nameserver is a DNS server and the adapter it is configured on.
type Nameserver struct {
	IP      net.IP
	Adapter string
}

// orderNameservers lists the DNS servers configured on the up adapters in
// the order to ask them: every IPv4 server before any IPv6 one, adapters by
// metric (lowest first, as for the default route), each adapter's servers
// in its own order, each address once. Two kinds of IPv6 server are left
// out: fec0:0:0:ffff::1 to ::3, which Windows lists on an adapter with no
// IPv6 DNS configured and which never answer, and link-local servers, which
// cannot be reached without naming a zone.
func orderNameservers(adapters []adapter) []Nameserver {
	up := make([]adapter, 0, len(adapters))
	for _, a := range adapters {
		if a.Up {
			up = append(up, a)
		}
	}
	sort.SliceStable(up, func(i, j int) bool { return up[i].Metric < up[j].Metric })

	var v4, v6 []Nameserver
	seen := make(map[string]bool)
	for _, a := range up {
		for _, ip := range a.Nameservers {
			if ip4 := ip.To4(); ip4 != nil {
				ip = ip4
			} else if ip.To16() == nil || isSiteLocal(ip) || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip.IsUnspecified() || seen[ip.String()] {
				continue
			}
			seen[ip.String()] = true
			ns := Nameserver{IP: ip, Adapter: a.Name}
			if len(ip) == net.IPv4len {
				v4 = append(v4, ns)
			} else {
				v6 = append(v6, ns)
			}
		}
	}
	return append(v4, v6...)
}

// isSiteLocal reports whether ip is in fec0::/10, the deprecated IPv6
// site-local range (RFC 3879) Windows' placeholder DNS servers sit in.
func isSiteLocal(ip net.IP) bool {
	return len(ip) == net.IPv6len && ip[0] == 0xfe && ip[1]&0xc0 == 0xc0
}
