package mdns

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"

	"github.com/BT10011/shoal/internal/dnswire"
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
	// Browse lists service types to ask for once per scan; nil sends nothing
	// and only listens. DefaultBrowse is what the TUI asks.
	Browse []string
	// Send puts a question on the wire: SendFrom5353.
	Send func(ctx context.Context, iface netif.Interface, group *net.UDPAddr, query []byte) error
}

// DefaultBrowse is the question shoal asks the network at the start of each
// scan: every service type on offer (RFC 6763 §9), and Dante's and NDI's
// own types, since not every embedded responder answers the enumeration.
// Everything that answers is credited by the same rules as an announcement.
var DefaultBrowse = []string{MetaQuery, "_netaudio-arc._udp.local", "_ndi._tcp.local"}

// browseAgain is when the question is repeated: RFC 6762 §5.2 asks that
// the first two queries of a browse be at least a second apart, since a
// responder may miss the first.
const browseAgain = time.Second

// BrowseQuery builds one mDNS message asking for PTR records of each name,
// ID 0 as multicast questions carry.
func BrowseQuery(names ...string) ([]byte, error) {
	msg := dnsmessage.Message{}
	for _, n := range names {
		name, err := dnsmessage.NewName(strings.TrimSuffix(n, ".") + ".")
		if err != nil {
			return nil, fmt.Errorf("%q is not a usable DNS name: %w", n, err)
		}
		msg.Questions = append(msg.Questions, dnsmessage.Question{Name: name, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET})
	}
	return msg.Pack()
}

func (o ListenerOptions) withDefaults() ListenerOptions {
	if o.Group == nil {
		o.Group = &net.UDPAddr{IP: net.ParseIP(Group), Port: Port}
	}
	if o.Listen == nil {
		o.Listen = ListenMulticast
	}
	if o.Send == nil {
		o.Send = SendFrom5353
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

	// held keeps the service types a sender listed before it had named its
	// own address, to be credited once it does. A router running an mDNS
	// repeater or reflector answers "which services are here?" for devices
	// on other networks, and its answers never name the router's own
	// address, so what it relays is never credited to it.
	held map[string]map[string]heldService
}

type heldService struct {
	rec  Record
	wire []byte
}

// NewListener creates the probe.
func NewListener(opts ListenerOptions) *Listener {
	return &Listener{opts: opts.withDefaults(), own: make(map[string]map[string]bool), held: make(map[string]map[string]heldService)}
}

// speaksForItself reports whether a sender has named its own address, by
// an address record or by answering for its own reverse name.
func (l *Listener) speaksForItself(key string) bool { return len(l.own[key]) > 0 }

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

// ListenerName is how the listener appears in the hood pane and the log,
// kept apart from the mdns enricher that asks questions: one hears the
// announcements devices make (DNS Service Discovery, RFC 6763, the traffic
// behind Bonjour and Avahi), the other asks each device its name.
const ListenerName = "dns-sd"

// Source is what the listener's facts are credited to. They are mDNS
// records like the enricher's answers, so they share its name and with it
// its priority, its weight as direct contact and its renewal.
const Source = "mdns"

func (l *Listener) Name() string { return ListenerName }

// ListenMulticast joins the mDNS group on an interface. With no interface
// named, the system's default for multicast is used.
//
// Port 5353 is already held by the system responder — mDNSResponder on macOS,
// avahi-daemon on most Linux systems — so the socket asks for SO_REUSEADDR
// and SO_REUSEPORT before binding. Those are what let several processes share
// a multicast port, and they are why shoal can watch the conversation without
// disturbing the responder that is having it. Binding the group address,
// rather than any address, means only multicast arrives here: unicast
// traffic for the port stays with the responder it was meant for.
func ListenMulticast(ctx context.Context, iface netif.Interface, group *net.UDPAddr) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: shareablePort}
	conn, err := lc.ListenPacket(ctx, "udp4", fmt.Sprintf("%s:%d", group.IP, group.Port))
	if err != nil {
		return nil, fmt.Errorf("cannot bind %s: %w", group, err)
	}
	ni, err := interfaceFor(iface)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := ipv4.NewPacketConn(conn).JoinGroup(ni, group); err != nil {
		conn.Close()
		where := "the default interface"
		if ni != nil {
			where = iface.Name
		}
		return nil, fmt.Errorf("cannot join %s on %s: %w", group.IP, where, err)
	}
	return conn, nil
}

// shareablePort sets the options that let a socket share port 5353 with the
// system's own responder. BSD and macOS need SO_REUSEPORT on every socket
// bound to the port; Linux accepts SO_REUSEADDR on both, whoever owns the
// other one.
func shareablePort(_, _ string, c syscall.RawConn) error {
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
}

// interfaceFor resolves the scanning interface, or nil to let the system
// choose when none is named.
func interfaceFor(iface netif.Interface) (*net.Interface, error) {
	if iface.Name == "" {
		return nil, nil
	}
	ni, err := net.InterfaceByName(iface.Name)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", iface.Name, err)
	}
	return ni, nil
}

// Run listens until the context is cancelled.
func (l *Listener) Run(ctx context.Context, iface netif.Interface, emit engine.Emit, report engine.Report) error {
	conn, err := l.opts.Listen(ctx, iface, l.opts.Group)
	if err != nil {
		return err
	}
	defer conn.Close()

	var query []byte
	if len(l.opts.Browse) > 0 {
		if query, err = BrowseQuery(l.opts.Browse...); err != nil {
			return err
		}
		report(engine.ProbeEvent{Kind: engine.KindInfo,
			Message: fmt.Sprintf("listening on %s via %s, sharing the port with the system responder; asking once which services are on offer, including Dante and NDI, then only listening", l.opts.Group, iface.Name)})
	} else {
		report(engine.ProbeEvent{Kind: engine.KindInfo,
			Message: fmt.Sprintf("listening on %s via %s, sharing the port with the system responder; nothing is sent", l.opts.Group, iface.Name)})
	}
	asked := 0
	nextAsk := time.Now()

	heard := 0
	buf := make([]byte, 9000)
	for {
		if err := ctx.Err(); err != nil {
			l.reportUncredited(report)
			report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("stopped listening after %d messages", heard)})
			return nil
		}
		if query != nil && asked < 2 && !time.Now().Before(nextAsk) {
			asked++
			nextAsk = time.Now().Add(browseAgain)
			if err := l.opts.Send(ctx, iface, l.opts.Group, query); err != nil {
				query = nil
				report(engine.ProbeEvent{Kind: engine.KindInfo, Target: l.opts.Group.String(),
					Message: fmt.Sprintf("could not ask which services are on offer (%v); listening only, so services appear when something else browses", err)})
			} else {
				report(engine.ProbeEvent{Kind: engine.KindSent, Target: l.opts.Group.String(),
					Message: fmt.Sprintf("PTR? %s → %s (%d bytes, browse %d of 2, from port %d: every responder offering these answers by multicast)", strings.Join(l.opts.Browse, ", "), l.opts.Group, len(query), asked, Port)})
			}
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
		Source:    Source,
		DeviceKey: key, Field: model.FieldIP, Value: key, Confidence: 0.9,
		Method: fmt.Sprintf("sent multicast DNS from this address to %s, overheard without asking", l.opts.Group),
	})

	// Addresses first: what a device says about its own name decides which
	// services can be credited to it below. Answering for its own reverse
	// name, which shoal's mdns probe asks every device, counts as well.
	ownReverse, _ := dnswire.ReverseName(addr.IP)
	for _, rec := range resp.Records {
		if rec.Type == dnsmessage.TypePTR && ownReverse != "" && trimDot(rec.Name) == trimDot(ownReverse) {
			l.claims(key, rec.PTR)
		}
	}
	for _, rec := range resp.Records {
		if rec.Type != dnsmessage.TypeA || !rec.Addr.Equal(addr.IP) {
			continue
		}
		l.claims(key, rec.Name)
		emit(model.Observation{
			Source:    Source,
			DeviceKey: key, Field: model.FieldHostname, Value: rec.Name,
			Confidence: Confidence, Raw: wire, TTL: max(rec.TTL, minTTL),
			Method: fmt.Sprintf("mDNS A record %s = %s, announced by the device itself (%s section, mDNS TTL %s)",
				rec.Name, rec.Addr, rec.Section, rec.TTL),
		})
	}

	if l.speaksForItself(key) {
		l.release(key, emit, report)
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
				Source:    Source,
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
	if !l.speaksForItself(key) {
		l.hold(key, name, rec, wire, report)
		return
	}
	emit(model.Observation{
		Source:    Source,
		DeviceKey: key, Field: model.FieldService, Value: name,
		Confidence: Confidence, Raw: wire, TTL: max(rec.TTL, minTTL),
		Method: fmt.Sprintf("mDNS PTR under %s: the device lists %s among the service types it offers (mDNS TTL %s)", MetaQuery, name, rec.TTL),
	})
}

// hold keeps a listed service type until its sender names its own address.
func (l *Listener) hold(key, name string, rec Record, wire []byte, report engine.Report) {
	if l.held[key] == nil {
		l.held[key] = make(map[string]heldService)
	}
	if _, already := l.held[key][name]; already {
		return
	}
	l.held[key][name] = heldService{rec: rec, wire: wire}
	report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
		Message: fmt.Sprintf("%s lists %s among the service types on offer, but has not named its own address; holding it until it does, since a router relaying another network's mDNS lists types it does not offer itself", key, name)})
}

// release credits what a sender listed before it named its own address.
func (l *Listener) release(key string, emit engine.Emit, report engine.Report) {
	held := l.held[key]
	if len(held) == 0 {
		return
	}
	delete(l.held, key)
	names := make([]string, 0, len(held))
	for name := range held {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h := held[name]
		emit(model.Observation{
			DeviceKey: key, Field: model.FieldService, Value: name, Source: Source,
			Confidence: Confidence, Raw: h.wire, TTL: max(h.rec.TTL, minTTL),
			Method: fmt.Sprintf("mDNS PTR under %s: the device lists %s among the service types it offers, credited once it had named its own address (mDNS TTL %s)", MetaQuery, name, h.rec.TTL),
		})
	}
	what := fmt.Sprintf("the %d service types it listed earlier are its own", len(names))
	if len(names) == 1 {
		what = "the service type it listed earlier is its own"
	}
	report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
		Message: fmt.Sprintf("%s has named its own address, so %s: %s", key, what, strings.Join(names, ", "))})
}

// reportUncredited says, when listening stops, which senders listed
// services but never spoke for themselves: most likely relays.
func (l *Listener) reportUncredited(report engine.Report) {
	keys := make([]string, 0, len(l.held))
	for key := range l.held {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		names := make([]string, 0, len(l.held[key]))
		for name := range l.held[key] {
			names = append(names, name)
		}
		sort.Strings(names)
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
			Message: fmt.Sprintf("not credited: %s listed %s but never named its own address while shoal listened. A router relaying another network's mDNS (a repeater or reflector) behaves like this, and its list belongs to the devices behind it", key, strings.Join(names, ", "))})
	}
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
