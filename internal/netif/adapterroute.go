package netif

import (
	"errors"
	"net"
)

// adapterRoute is what Windows' adapter table says about one adapter:
// whether it is up, its IPv4 metric and the gateways configured on it. It is
// kept apart from the Windows call that fills it, so the choice below is
// tested on every platform.
type adapterRoute struct {
	Index    int
	Up       bool
	Metric   uint32 // the interface's IPv4 metric; lower is preferred
	Gateways []net.IP
}

// pickDefaultRoute chooses the adapter the IPv4 default route leaves
// through: an up adapter with an IPv4 gateway, the lowest metric winning
// and the first listed on a tie, as Windows' own route choice does when the
// routes' metrics are equal. It returns the gateway and the adapter's index.
func pickDefaultRoute(adapters []adapterRoute) (net.IP, int, error) {
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
