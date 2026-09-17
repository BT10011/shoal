// Package neigh discovers devices without raw packet access by reading the
// kernel's own neighbour cache — the ARP table the operating system keeps
// for its own traffic — after nudging each address into it.
package neigh

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
)

// Entry is one row of the kernel neighbour cache.
type Entry struct {
	IP        net.IP
	MAC       net.HardwareAddr
	Interface string
	Permanent bool
}

// nudgePort is the discard service. A UDP datagram to a closed port makes
// the kernel resolve the address first, which is the point; the datagram
// itself is expected to be dropped or answered with ICMP unreachable.
const nudgePort = 9

// Options tune the probe. Zero values are the defaults shown.
type Options struct {
	Table    func() ([]Entry, error) // Table
	Nudge    func(net.IP) error      // a UDP datagram to the discard port
	Rate     int                     // nudges per second: 200
	Settle   time.Duration           // wait for the kernel to resolve: 1s
	MaxHosts int                     // refuse larger subnets: 1022 (/22)
	NoNudge  bool                    // only read what the cache already holds
}

const (
	defaultRate     = 200
	defaultSettle   = time.Second
	defaultMaxHosts = 1022
)

func (o Options) withDefaults() Options {
	if o.Table == nil {
		o.Table = Table
	}
	if o.Nudge == nil {
		o.Nudge = nudge
	}
	if o.Rate <= 0 {
		o.Rate = defaultRate
	}
	if o.Settle <= 0 {
		o.Settle = defaultSettle
	}
	if o.MaxHosts <= 0 {
		o.MaxHosts = defaultMaxHosts
	}
	return o
}

// Discoverer reads the neighbour cache.
type Discoverer struct {
	opts Options
}

// New creates the probe.
func New(opts Options) *Discoverer { return &Discoverer{opts: opts.withDefaults()} }

func (d *Discoverer) Name() string { return "neigh" }

// FlagSelf marks the device that is this machine.
const FlagSelf = "this-host"

// Run nudges every address in the subnet, waits for the kernel to resolve
// them, then reports what the cache holds.
func (d *Discoverer) Run(ctx context.Context, iface netif.Interface, emit engine.Emit, report engine.Report) error {
	if iface.Subnet == nil || iface.IP.To4() == nil {
		return fmt.Errorf("interface %q has no IPv4 address or subnet", iface.Name)
	}
	if n := iface.Hosts(); n > d.opts.MaxHosts {
		return fmt.Errorf("subnet %s has %d hosts, more than the limit of %d; narrow the range", iface.Subnet, n, d.opts.MaxHosts)
	}

	if len(iface.MAC) > 0 {
		self := fmt.Sprintf("address of this machine's interface %s (not probed)", iface.Name)
		emit(model.Observation{DeviceKey: iface.MAC.String(), Field: model.FieldMAC, Value: iface.MAC.String(), Source: "netif", Method: self, Confidence: 1})
		emit(model.Observation{DeviceKey: iface.MAC.String(), Field: model.FieldIP, Value: iface.IP.String(), Source: "netif", Method: self, Confidence: 1})
		emit(model.Observation{DeviceKey: iface.MAC.String(), Field: model.FieldFlag, Value: FlagSelf, Source: "netif", Method: self, Confidence: 1})
	}

	targets := hostIPs(iface.Subnet, iface.IP)
	if !d.opts.NoNudge {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf(
			"unprivileged mode: sending one UDP datagram to each of %d addresses on %s so the kernel resolves them, then reading its neighbour cache",
			len(targets), iface.Name)})
		if err := d.nudgeAll(ctx, targets, report); err != nil {
			return err
		}
		report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("waiting %s for the kernel to finish resolving", d.opts.Settle)})
		if err := sleep(ctx, d.opts.Settle); err != nil {
			return err
		}
	} else {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Message: "reading the kernel neighbour cache without nudging"})
	}

	entries, err := d.opts.Table()
	if err != nil {
		return fmt.Errorf("read neighbour cache: %w", err)
	}
	found := 0
	for _, e := range entries {
		if !d.usable(e, iface) {
			continue
		}
		found++
		how := "the OS resolved it for its own traffic"
		if !d.opts.NoNudge {
			how = "shoal sent a UDP datagram to prompt the lookup"
		}
		perm := ""
		if e.Permanent {
			perm = ", marked permanent (a static or self-assigned entry)"
		}
		method := fmt.Sprintf("kernel neighbour cache on %s says %s is at %s%s; %s. shoal did not see the ARP exchange itself",
			iface.Name, e.IP, e.MAC, perm, how)
		report(engine.ProbeEvent{Kind: engine.KindReceived, Target: e.IP.String(), Message: fmt.Sprintf("cache: %s is-at %s", e.IP, e.MAC)})
		key := e.MAC.String()
		emit(model.Observation{DeviceKey: key, Field: model.FieldMAC, Value: e.MAC.String(), Method: method, Confidence: 0.8})
		emit(model.Observation{DeviceKey: key, Field: model.FieldIP, Value: e.IP.String(), Method: method, Confidence: 0.8})
	}
	report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("neighbour cache holds %d usable entries for %s", found, iface.Subnet)})
	return nil
}

func (d *Discoverer) nudgeAll(ctx context.Context, targets []net.IP, report engine.Report) error {
	ticker := time.NewTicker(time.Second / time.Duration(d.opts.Rate))
	defer ticker.Stop()
	for i, ip := range targets {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if err := d.opts.Nudge(ip); err != nil {
			report(engine.ProbeEvent{Kind: engine.KindInfo, Target: ip.String(), Message: "nudge failed: " + err.Error()})
		} else {
			report(engine.ProbeEvent{Kind: engine.KindSent, Target: ip.String(), Message: fmt.Sprintf("udp datagram to %s:%d to prompt a neighbour lookup", ip, nudgePort)})
		}
		report(engine.ProbeEvent{Kind: engine.KindProgress, Done: i + 1, Total: len(targets)})
	}
	return nil
}

// usable rejects cache rows that are not on-link unicast neighbours: other
// interfaces, addresses outside the subnet, this machine, and the
// broadcast and multicast MACs the kernel keeps alongside real hosts.
func (d *Discoverer) usable(e Entry, iface netif.Interface) bool {
	switch {
	case len(e.MAC) != 6 || e.IP.To4() == nil:
		return false
	case e.Interface != "" && iface.Name != "" && e.Interface != iface.Name:
		return false
	case !iface.Subnet.Contains(e.IP):
		return false
	case e.IP.Equal(iface.IP):
		return false
	case e.MAC[0]&0x01 != 0: // broadcast and multicast entries
		return false
	case isZero(e.MAC):
		return false
	}
	return true
}

func isZero(mac net.HardwareAddr) bool {
	for _, b := range mac {
		if b != 0 {
			return false
		}
	}
	return true
}

func nudge(ip net.IP) error {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: ip, Port: nudgePort})
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetWriteDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		return err
	}
	_, err = conn.Write([]byte{0})
	return err
}

// hostIPs lists every host address in the subnet except exclude.
func hostIPs(subnet *net.IPNet, exclude net.IP) []net.IP {
	ip4 := subnet.IP.To4()
	ones, bits := subnet.Mask.Size()
	if ip4 == nil || bits != 32 {
		return nil
	}
	base := be32(ip4) & be32(net.IP(subnet.Mask).To4())
	size := uint32(1) << (32 - ones)
	first, last := base, base+size-1
	if ones < 31 {
		first, last = base+1, base+size-2
	}
	var out []net.IP
	for n := first; ; n++ {
		ip := net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n)).To4()
		if !ip.Equal(exclude) {
			out = append(out, ip)
		}
		if n == last {
			break
		}
	}
	return out
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
