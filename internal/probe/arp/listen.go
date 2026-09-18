package arp

import (
	"context"
	"fmt"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/netif"
)

// ListenOptions tune the passive listener.
type ListenOptions struct {
	Open func(netif.Interface) (Conn, error) // OpenConn
	// StandAside, when set and true, means another probe is already
	// decoding every ARP frame on this interface (the sweep, which listens
	// while it asks), so this one lets those frames pass rather than log
	// each a second time. Pass Sweep.Sweeping.
	StandAside func() bool
}

func (o ListenOptions) withDefaults() ListenOptions {
	if o.Open == nil {
		o.Open = OpenConn
	}
	return o
}

// Listener is a discoverer that sends nothing and writes down every ARP
// frame it sees, for as long as it runs. The sweep only listens while it
// asks, a few seconds, and a device with the wrong address may not speak in
// that window: a camera on a link-local address ARPs for its gateway when
// something prods it, a host with a stale static address announces itself
// when it links up. Listening continuously catches them whenever they do.
type Listener struct {
	opts ListenOptions
}

// NewListener creates the passive probe.
func NewListener(opts ListenOptions) *Listener {
	return &Listener{opts: opts.withDefaults()}
}

// Name is "arp", like the sweep: the facts are the same kind, learned the
// same way, and the method strings say "overheard".
func (l *Listener) Name() string { return "arp" }

// Run listens until the context is cancelled.
func (l *Listener) Run(ctx context.Context, iface netif.Interface, emit engine.Emit, report engine.Report) error {
	if len(iface.MAC) != 6 || iface.IP.To4() == nil || iface.Subnet == nil {
		return fmt.Errorf("interface %q has no Ethernet MAC, IPv4 address or subnet", iface.Name)
	}
	conn, err := l.opts.Open(iface)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("listening for ARP on %s; nothing is sent. Every device that speaks ARP on this segment is written down, whatever address it has", iface.Name)
	if l.opts.StandAside != nil {
		msg += ". While the sweep runs it reads these same frames, so this listener takes over when the sweep finishes"
	}
	report(engine.ProbeEvent{Kind: engine.KindInfo, Message: msg})

	heard := 0
	lst := &listener{iface: iface, emit: emit, report: report, seen: make(map[string]bool)}
	lst.skip = l.opts.StandAside
	lst.onFrame = func() {
		heard++
		report(engine.ProbeEvent{Kind: engine.KindProgress, Done: heard, Message: "listening"})
	}
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	lst.loop(conn)
	report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("stopped listening after %d ARP frames", heard)})
	return nil
}
