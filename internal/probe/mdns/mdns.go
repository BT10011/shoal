package mdns

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"

	"github.com/BT10011/shoal/internal/dnswire"
	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
)

// Group is the IPv4 multicast address every mDNS responder listens on, and
// 5353 is the port. The address is link-local: routers do not forward it, so
// an answer is proof the device is on this network segment.
const (
	Group = "224.0.0.251"
	Port  = 5353
)

// Confidence is how far an mDNS name is trusted. It is higher than reverse
// DNS because the device is answering for itself rather than repeating what
// an administrator once typed into a DHCP server.
const Confidence = 0.9

// minTTL holds a name on screen long enough to be read. Responders give
// reverse PTR records a TTL of about 10 seconds, expecting an interested
// querier to keep asking; shoal asks again only as a value nears expiry,
// and asking every few seconds per device would be impolite. The method
// string always reports the true TTL.
const minTTL = 5 * time.Minute

const (
	defaultWait        = time.Second
	defaultConcurrency = 4
)

// Options tune the probe. Zero values are the defaults shown.
type Options struct {
	Group *net.UDPAddr    // 224.0.0.251:5353
	Wait  time.Duration   // how long to listen after asking: 1s
	Iface netif.Interface // the interface to ask on; zero lets the system choose
	// Open gets the sockets for one question: OpenMulticast, falling back
	// to OpenOneShot when port 5353 cannot be shared.
	Open        func(ctx context.Context, iface netif.Interface, group *net.UDPAddr) (Asker, error)
	Fallback    func(ctx context.Context, iface netif.Interface, group *net.UDPAddr) (Asker, error) // OpenOneShot
	NewID       func() uint16                                                                       // IDs for one-shot questions: random
	Concurrency int                                                                                 // devices asked at once: 4
}

func (o Options) withDefaults() Options {
	if o.Group == nil {
		o.Group = &net.UDPAddr{IP: net.ParseIP(Group), Port: Port}
	}
	if o.Wait <= 0 {
		o.Wait = defaultWait
	}
	if o.Open == nil {
		o.Open = OpenMulticast
	}
	if o.Fallback == nil {
		o.Fallback = OpenOneShot
	}
	if o.NewID == nil {
		o.NewID = randomID
	}
	if o.Concurrency <= 0 {
		o.Concurrency = defaultConcurrency
	}
	return o
}

// Asker sends one question to the group and hands back what arrives.
type Asker interface {
	Send(query []byte, group *net.UDPAddr) error
	ReadFrom(p []byte) (int, net.Addr, error)
	SetReadDeadline(t time.Time) error
	Close() error
	// OneShot reports that questions leave from an ephemeral port, so
	// answers come back unicast and echo the query ID (RFC 6762 §6.7).
	OneShot() bool
}

// ErrPortHeld means another program holds port 5353 without letting it be
// shared, so a question cannot be asked from it.
var ErrPortHeld = errors.New("port 5353 is held by another program that does not share it")

// OpenMulticast prepares to ask the way every Bonjour and Avahi querier
// does: from port 5353. A question from that port is answered by multicast
// to the whole group (RFC 6762 §6), and a host firewall that allows mDNS at
// all allows that. An answer sent back by unicast, which is what a question
// from any other port gets, is usually dropped: the firewall's connection
// tracking saw a packet go to 224.0.0.251 and does not recognise a reply
// arriving from the device's own address.
//
// Answers are read on a socket bound to the group address, so it receives
// only multicast and never takes unicast traffic meant for the system's own
// responder, which shares the port. Each question is sent from a second
// socket bound to port 5353 that exists only for that one write.
func OpenMulticast(ctx context.Context, iface netif.Interface, group *net.UDPAddr) (Asker, error) {
	recv, err := ListenMulticast(ctx, iface, group)
	if err != nil {
		return nil, err
	}
	return &multicastAsker{recv: recv, iface: iface, ctx: ctx}, nil
}

type multicastAsker struct {
	recv  net.PacketConn
	iface netif.Interface
	ctx   context.Context
}

func (a *multicastAsker) OneShot() bool                            { return false }
func (a *multicastAsker) ReadFrom(p []byte) (int, net.Addr, error) { return a.recv.ReadFrom(p) }
func (a *multicastAsker) SetReadDeadline(t time.Time) error        { return a.recv.SetReadDeadline(t) }
func (a *multicastAsker) Close() error                             { return a.recv.Close() }

func (a *multicastAsker) Send(query []byte, group *net.UDPAddr) error {
	lc := net.ListenConfig{Control: shareablePort}
	conn, err := lc.ListenPacket(a.ctx, "udp4", fmt.Sprintf(":%d", group.Port))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPortHeld, err)
	}
	defer conn.Close()
	pc := ipv4.NewPacketConn(conn)
	if ni, err := interfaceFor(a.iface); err != nil {
		return err
	} else if ni != nil {
		// Several interfaces can carry multicast; ask on the one being scanned.
		if err := pc.SetMulticastInterface(ni); err != nil {
			return fmt.Errorf("cannot send multicast on %s: %w", a.iface.Name, err)
		}
	}
	// Keep our own question from reaching this host's sockets: the
	// listener would otherwise write it down as traffic from a device.
	_ = pc.SetMulticastLoopback(false)
	_ = pc.SetMulticastTTL(255) // RFC 6762 §11
	_, err = conn.WriteTo(query, group)
	return err
}

// OpenOneShot asks from an ephemeral port, the fallback when port 5353
// cannot be shared. Responders then answer by unicast, straight back to
// that port, which works unless a host firewall drops the reply.
func OpenOneShot(ctx context.Context, _ netif.Interface, _ *net.UDPAddr) (Asker, error) {
	var lc net.ListenConfig
	conn, err := lc.ListenPacket(ctx, "udp4", ":0")
	if err != nil {
		return nil, err
	}
	return oneShotAsker{conn}, nil
}

type oneShotAsker struct{ net.PacketConn }

func (a oneShotAsker) OneShot() bool { return true }
func (a oneShotAsker) Send(query []byte, group *net.UDPAddr) error {
	_, err := a.WriteTo(query, group)
	return err
}

func randomID() uint16 {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint16(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint16(b[:])
}

// Enricher asks a device, over multicast, what name it calls itself.
type Enricher struct {
	opts Options
}

// New creates the probe.
func New(opts Options) *Enricher { return &Enricher{opts: opts.withDefaults()} }

func (e *Enricher) Name() string            { return "mdns" }
func (e *Enricher) Triggers() []model.Field { return []model.Field{model.FieldIP} }
func (e *Enricher) Produces() model.Field   { return model.FieldHostname }
func (e *Enricher) Concurrency() int        { return e.opts.Concurrency }

// Enrich sends one reverse PTR question to the multicast group and listens
// for a short while. Silence is not an error: most devices that are not
// Apple, Linux-with-avahi or a modern printer simply do not speak mDNS.
func (e *Enricher) Enrich(ctx context.Context, d model.DeviceSnapshot, emit engine.Emit, report engine.Report) error {
	raw := d.Key
	if o, ok := d.ResolvedAt(model.FieldIP, time.Now()); ok {
		raw = o.Value
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return fmt.Errorf("device %s has no parseable IP (%q)", d.Key, raw)
	}
	name, err := dnswire.ReverseName(ip)
	if err != nil {
		return err
	}

	asker, err := e.opts.Open(ctx, e.opts.Iface, e.opts.Group)
	if err != nil {
		return fmt.Errorf("cannot open a socket for mDNS: %w", err)
	}
	defer func() { asker.Close() }()

	// A multicast question carries ID 0 (RFC 6762 §18.1): the answer is
	// matched by name, since everyone on the segment hears it.
	id := uint16(0)
	query, err := dnswire.Query(id, name, dnswire.QueryOptions{Type: dnsmessage.TypePTR})
	if err != nil {
		return err
	}
	err = asker.Send(query, e.opts.Group)
	if errors.Is(err, ErrPortHeld) && !asker.OneShot() {
		asker.Close()
		if asker, err = e.opts.Fallback(ctx, e.opts.Iface, e.opts.Group); err != nil {
			return fmt.Errorf("cannot open a socket for mDNS: %w", err)
		}
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: d.Key,
			Message: "port 5353 is held by a program that will not share it, so asking from an ephemeral port instead; the answer comes back by unicast, which a host firewall may drop"})
		id = e.opts.NewID()
		if query, err = dnswire.Query(id, name, dnswire.QueryOptions{Type: dnsmessage.TypePTR}); err != nil {
			return err
		}
		err = asker.Send(query, e.opts.Group)
	}
	if err != nil {
		return fmt.Errorf("cannot send to %s: %w", e.opts.Group, err)
	}
	how := fmt.Sprintf("from port %d, so the answer is multicast to the group (RFC 6762 §6) and every host on the segment hears both", Port)
	if asker.OneShot() {
		how = fmt.Sprintf("id 0x%04x, one-shot from an ephemeral port: the answer comes back unicast", id)
	}
	report(engine.ProbeEvent{Kind: engine.KindSent, Target: d.Key,
		Message: fmt.Sprintf("PTR? %s → %s (%d bytes, %s)", name, e.opts.Group, len(query), how)})

	deadline := time.Now().Add(e.opts.Wait)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := asker.SetReadDeadline(deadline); err != nil {
		return err
	}

	answered := false
	buf := make([]byte, 9000)
	for {
		n, from, err := asker.ReadFrom(buf)
		if err != nil {
			if !answered {
				msg := fmt.Sprintf("no mDNS answer for %s within %s; this device probably does not run a responder", ip, e.opts.Wait)
				if asker.OneShot() {
					msg += ", or a host firewall dropped a unicast reply"
				}
				report(engine.ProbeEvent{Kind: engine.KindInfo, Target: d.Key, Message: msg})
			}
			return nil
		}
		wire := append([]byte(nil), buf[:n]...)
		if e.handle(d.Key, ip, name, id, asker.OneShot(), wire, from, answered, emit, report) {
			answered = true
		}
		if time.Now().After(deadline) {
			return nil
		}
	}
}

// handle looks at one message and reports it when it answers the question.
// It returns true when it carried the name shoal asked for. A socket that
// hears the group hears every mDNS message on the segment, so anything
// without a record for this name is someone else's conversation and is
// left alone. Later answers are logged but not emitted: the first answer
// wins, and a second one is worth seeing rather than silently overwriting
// the first.
func (e *Enricher) handle(key string, ip net.IP, name string, id uint16, oneShot bool, wire []byte, from net.Addr, answered bool, emit engine.Emit, report engine.Report) bool {
	resp, err := ParseResponse(wire)
	if err != nil {
		if oneShot { // on the group socket, other people's malformed traffic is not our business
			report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
				Message: fmt.Sprintf("%s sent %d bytes shoal could not decode: %v", from, len(wire), err)})
		}
		return false
	}
	records := resp.PointedAt(name)
	if len(records) == 0 {
		if oneShot {
			report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
				Message: fmt.Sprintf("%s answered but carried no PTR for %s", from, name)})
		}
		return false
	}
	if oneShot && resp.ID != id {
		report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
			Message: fmt.Sprintf("discarding a reply from %s: id 0x%04x does not match query id 0x%04x", from, resp.ID, id)})
		return false
	}
	report(engine.ProbeEvent{Kind: engine.KindReceived, Target: key,
		Message: fmt.Sprintf("%s: %s", from, resp.Summary())})
	if answered {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
			Message: fmt.Sprintf("%s also claims %s is %s", from, ip, records[0].PTR)})
		return true
	}

	rec := records[0]
	emit(model.Observation{
		DeviceKey:  key,
		Field:      model.FieldHostname,
		Value:      rec.PTR,
		Confidence: Confidence,
		Raw:        wire,
		TTL:        max(rec.TTL, minTTL),
		Method:     fmt.Sprintf("mDNS PTR for %s, %s, mDNS TTL %s", name, responder(ip, from), rec.TTL),
	})
	return true
}

// responder explains who answered. Usually it is the device itself; a Bonjour
// Sleep Proxy answers on behalf of devices that have gone to sleep, and that
// is worth saying out loud rather than presenting as the device's own word.
func responder(ip net.IP, from net.Addr) string {
	host := from.String()
	if addr, ok := from.(*net.UDPAddr); ok {
		if addr.IP.Equal(ip) {
			return fmt.Sprintf("answered by %s itself over multicast", ip)
		}
		host = addr.IP.String()
	}
	return fmt.Sprintf("answered by %s on behalf of %s (a proxy responder, e.g. a Bonjour Sleep Proxy)", host, ip)
}
