package mdns

import (
	"context"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
)

// MetaQuery is the name that lists the service types on a network. A
// responder answering it says "here are the kinds of thing I offer".
const MetaQuery = "_services._dns-sd._udp.local"

// readSlice is how long a read waits before the listener checks whether it
// has been cancelled. It bounds shutdown time, nothing else.
const readSlice = 500 * time.Millisecond

// ListenerOptions tune the passive listener.
type ListenerOptions struct {
	Group  *net.UDPAddr
	Listen func(ctx context.Context, iface netif.Interface, group *net.UDPAddr) (net.PacketConn, error)
}

func (o ListenerOptions) withDefaults() ListenerOptions {
	if o.Group == nil {
		o.Group = &net.UDPAddr{IP: net.ParseIP(Group), Port: Port}
	}
	if o.Listen == nil {
		o.Listen = ListenMulticast
	}
	return o
}

// Listener is a discoverer that says nothing and writes down what it hears.
// Bonjour networks are noisy: devices announce themselves when they join,
// when they wake, and whenever a service changes, so simply listening finds
// devices and their services without sending a single packet.
type Listener struct {
	opts ListenerOptions

	// own records the names each sender has claimed for itself, so that a
	// service can be credited to the device that actually offers it. Bonjour
	// devices relay each other's records — a phone will announce a PTR for a
	// laptop's instance — and crediting the sender would be wrong.
	own map[string]map[string]bool
}

// NewListener creates the passive probe.
func NewListener(opts ListenerOptions) *Listener {
	return &Listener{opts: opts.withDefaults(), own: make(map[string]map[string]bool)}
}

// claims records that key announced name as its own.
func (l *Listener) claims(key, name string) {
	names := l.own[key]
	if names == nil {
		names = make(map[string]bool)
		l.own[key] = names
	}
	names[strings.ToLower(trimDot(name))] = true
}

// owns reports whether key has announced an address record for name.
func (l *Listener) owns(key, name string) bool {
	return l.own[key][strings.ToLower(trimDot(name))]
}

func (l *Listener) Name() string { return "mdns" }

// ListenMulticast joins the mDNS group on an interface.
//
// Port 5353 is already held by the system responder — mDNSResponder on macOS,
// avahi-daemon on most Linux systems — so the socket asks for SO_REUSEADDR
// and SO_REUSEPORT before binding. Those are what let several processes share
// a multicast port, and they are why shoal can watch the conversation without
// disturbing the responder that is having it.
func ListenMulticast(ctx context.Context, iface netif.Interface, group *net.UDPAddr) (net.PacketConn, error) {
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var opErr error
			err := c.Control(func(fd uintptr) {
				if opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); opErr != nil {
					return
				}
				opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			})
			if err != nil {
				return err
			}
			return opErr
		},
	}
	conn, err := lc.ListenPacket(ctx, "udp4", fmt.Sprintf("%s:%d", group.IP, group.Port))
	if err != nil {
		return nil, fmt.Errorf("cannot bind %s: %w", group, err)
	}
	ni, err := net.InterfaceByName(iface.Name)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("interface %s: %w", iface.Name, err)
	}
	if err := ipv4.NewPacketConn(conn).JoinGroup(ni, group); err != nil {
		conn.Close()
		return nil, fmt.Errorf("cannot join %s on %s: %w", group.IP, iface.Name, err)
	}
	return conn, nil
}

// Run listens until the context is cancelled.
func (l *Listener) Run(ctx context.Context, iface netif.Interface, emit engine.Emit, report engine.Report) error {
	conn, err := l.opts.Listen(ctx, iface, l.opts.Group)
	if err != nil {
		return err
	}
	defer conn.Close()

	report(engine.ProbeEvent{Kind: engine.KindInfo,
		Message: fmt.Sprintf("listening on %s via %s, sharing the port with the system responder; nothing is sent", l.opts.Group, iface.Name)})

	heard := 0
	buf := make([]byte, 9000)
	for {
		if err := ctx.Err(); err != nil {
			report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("stopped listening after %d messages", heard)})
			return nil
		}
		if err := conn.SetReadDeadline(time.Now().Add(readSlice)); err != nil {
			return err
		}
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			continue // a deadline, so the loop can check for cancellation
		}
		heard++
		report(engine.ProbeEvent{Kind: engine.KindProgress, Done: heard, Message: "listening"})
		l.overheard(append([]byte(nil), buf[:n]...), from, emit, report)
	}
}

// overheard turns one message into facts about whoever sent it. Everything is
// attributed to the sender's address: a responder speaks only for itself, and
// the store links that address to a MAC once ARP has found one.
func (l *Listener) overheard(wire []byte, from net.Addr, emit engine.Emit, report engine.Report) {
	addr, ok := from.(*net.UDPAddr)
	if !ok {
		return
	}
	key := addr.IP.String()

	resp, err := ParseResponse(wire)
	if err != nil {
		report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
			Message: fmt.Sprintf("%d bytes from %s could not be decoded: %v", len(wire), key, err)})
		return
	}
	if len(resp.Records) == 0 {
		// A query, not an announcement. It still proves somebody is there.
		report(engine.ProbeEvent{Kind: engine.KindReceived, Target: key,
			Message: fmt.Sprintf("%s asked about %s", key, resp.Question)})
	} else {
		report(engine.ProbeEvent{Kind: engine.KindReceived, Target: key,
			Message: fmt.Sprintf("%s announced: %s", key, resp.Summary())})
	}

	emit(model.Observation{
		DeviceKey: key, Field: model.FieldIP, Value: key, Confidence: 0.9,
		Method: fmt.Sprintf("sent multicast DNS from this address to %s, overheard without asking", l.opts.Group),
	})

	// Addresses first: what a device says about its own name decides which
	// services can be credited to it below.
	for _, rec := range resp.Records {
		if rec.Type != dnsmessage.TypeA || !rec.Addr.Equal(addr.IP) {
			continue
		}
		l.claims(key, rec.Name)
		emit(model.Observation{
			DeviceKey: key, Field: model.FieldHostname, Value: rec.Name,
			Confidence: Confidence, Raw: wire, TTL: max(rec.TTL, minTTL),
			Method: fmt.Sprintf("mDNS A record %s = %s, announced by the device itself (%s section, mDNS TTL %s)",
				rec.Name, rec.Addr, rec.Section, rec.TTL),
		})
	}

	for _, rec := range resp.Records {
		switch rec.Type {
		case dnsmessage.TypePTR:
			l.service(key, rec, wire, emit, report)
		case dnsmessage.TypeSRV:
			service, ok := ServiceType(rec.Name)
			if !ok {
				continue
			}
			// The SRV names the host that actually serves the instance. Only
			// when that host is one the sender has claimed is the service
			// the sender's own.
			if !l.owns(key, rec.SRV.Target) {
				report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
					Message: fmt.Sprintf("%s announced %s served by %s, which it has not claimed as its own name; not crediting it with the service",
						key, rec.Name, rec.SRV.Target)})
				continue
			}
			emit(model.Observation{
				DeviceKey: key, Field: model.FieldService, Value: service,
				Confidence: Confidence, Raw: wire, TTL: max(rec.TTL, minTTL),
				Method: fmt.Sprintf("mDNS SRV record %s = %s:%d, and %s announced %s as its own name (mDNS TTL %s)",
					rec.Name, rec.SRV.Target, rec.SRV.Port, key, rec.SRV.Target, rec.TTL),
			})
		}
	}
}

// service emits what a PTR record proves about the sender.
//
// Only an answer to the meta-query does: it says "these are the types I
// offer", in the sender's own voice. A type pointing at an instance —
// `_airplay._tcp.local → Living Room._airplay._tcp.local` — says nothing
// about who offers it, and devices really do announce each other's
// instances, so that shape is logged and left to the SRV record, which names
// the serving host and can be checked.
func (l *Listener) service(key string, rec Record, wire []byte, emit engine.Emit, report engine.Report) {
	if rec.Name != MetaQuery {
		if service, ok := ServiceType(rec.Name); ok {
			report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
				Message: fmt.Sprintf("%s announced the instance %s of %s; waiting for an SRV record to say which host serves it", key, rec.PTR, service)})
		}
		return
	}
	name := trimSuffix(rec.PTR)
	if name == "" {
		return
	}
	emit(model.Observation{
		DeviceKey: key, Field: model.FieldService, Value: name,
		Confidence: Confidence, Raw: wire, TTL: max(rec.TTL, minTTL),
		Method: fmt.Sprintf("mDNS PTR under %s: the device lists %s among the service types it offers (mDNS TTL %s)", MetaQuery, name, rec.TTL),
	})
}

// ServiceType pulls the service type out of a DNS-SD name: "_airplay._tcp"
// from "Living Room._airplay._tcp.local". The type is the pair of labels
// where the second is the transport.
func ServiceType(name string) (string, bool) {
	if trimSuffix(name) == trimSuffix(MetaQuery) {
		return "", false
	}
	labels := strings.Split(trimDot(name), ".")
	for i := 0; i+1 < len(labels); i++ {
		if !strings.HasPrefix(labels[i], "_") {
			continue
		}
		if labels[i+1] == "_tcp" || labels[i+1] == "_udp" {
			return labels[i] + "." + labels[i+1], true
		}
	}
	return "", false
}

// trimSuffix drops the ".local" a DNS-SD name always ends with, which is the
// same on every row and so carries no information on screen.
func trimSuffix(name string) string {
	return strings.TrimSuffix(trimDot(name), ".local")
}
