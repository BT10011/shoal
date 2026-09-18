// Package rogue flags addresses the local subnet does not explain. It sends
// nothing and reads nothing from the network: it compares what other probes
// have already learned against what the scanning interface's subnet allows,
// and says in plain words why an address does not belong.
//
// The point is a technician on a venue floor. AV-over-IP gear moves between
// sites and arrives carrying a static address from the last one, or with no
// configuration at all and falls back to a link-local address, and the
// question is always which box it is. shoal answers that by listening: a
// device on the wrong subnet still speaks ARP and multicasts on the segment
// it is plugged into, and every such frame carries its address.
package rogue

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

// Flag values. They are facts about an address a device has claimed; they
// are never retracted, so a device that was on the wrong address at 12:01
// is still remembered as such after it has been fixed.
const (
	FlagLinkLocal = "link-local-ip"
	FlagOffSubnet = "off-subnet-ip"
)

// linkLocal is the IPv4 range a host assigns itself when no DHCP server
// answers (RFC 3927).
var linkLocal = &net.IPNet{IP: net.IPv4(169, 254, 0, 0).To4(), Mask: net.CIDRMask(16, 32)}

// LinkLocal reports whether ip is an IPv4 link-local address.
func LinkLocal(ip net.IP) bool { return linkLocal.Contains(ip) }

// Enricher holds the subnet everything is judged against.
type Enricher struct {
	subnet *net.IPNet
}

// New creates the probe for a subnet. A nil subnet flags nothing: with no
// idea what belongs, nothing can be said not to.
func New(subnet *net.IPNet) *Enricher { return &Enricher{subnet: subnet} }

func (e *Enricher) Name() string            { return "rogue" }
func (e *Enricher) Triggers() []model.Field { return []model.Field{model.FieldIP} }
func (e *Enricher) Produces() model.Field   { return model.FieldFlag }
func (e *Enricher) Concurrency() int        { return 1 }

// Subnet returns what addresses are judged against, for display.
func (e *Enricher) Subnet() *net.IPNet { return e.subnet }

// Enrich looks at every live address the device has and flags the ones the
// subnet does not explain.
func (e *Enricher) Enrich(_ context.Context, d model.DeviceSnapshot, emit engine.Emit, report engine.Report) error {
	if e.subnet == nil {
		return nil
	}
	for _, o := range d.Live(model.FieldIP, time.Now()) {
		ip := net.ParseIP(o.Value)
		if ip == nil || ip.To4() == nil {
			continue
		}
		flag, why := e.Judge(ip)
		if flag == "" {
			continue
		}
		method := fmt.Sprintf("%s Learned from %s: %s", why, o.Source, o.Method)
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: d.Key, Message: fmt.Sprintf("%s: %s", flag, why)})
		emit(model.Observation{DeviceKey: d.Key, Field: model.FieldFlag, Value: flag, Confidence: 1, Method: method})
	}
	return nil
}

// Judge says whether an address belongs to the subnet and, when it does
// not, which flag applies and why, in words meant for the screen.
func (e *Enricher) Judge(ip net.IP) (flag, why string) {
	switch {
	case e.subnet == nil || e.subnet.Contains(ip):
		return "", ""
	case LinkLocal(ip):
		return FlagLinkLocal, fmt.Sprintf("%s is inside 169.254.0.0/16, the range a host assigns itself when no DHCP server answers (RFC 3927); this interface's subnet is %s. The device probably asked for an address and got no reply: a DHCP server that is down or absent on this VLAN, a cable in the wrong socket, or a device that was never configured.", ip, e.subnet)
	default:
		return FlagOffSubnet, fmt.Sprintf("%s is outside this interface's subnet %s, yet the device is on this segment, so nothing here will route to it. Usually a static address left over from another network or venue, or a lease from a different VLAN; occasionally a second uplink.", ip, e.subnet)
	}
}
