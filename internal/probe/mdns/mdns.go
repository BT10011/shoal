package mdns

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/BT10011/shoal/internal/dnswire"
	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
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
// querier to keep asking; shoal asks once per device, so the observation
// would otherwise expire almost immediately. The method string always
// reports the true TTL.
const minTTL = 5 * time.Minute

const (
	defaultWait        = time.Second
	defaultConcurrency = 4
)

// Options tune the probe. Zero values are the defaults shown.
type Options struct {
	Group       *net.UDPAddr                                  // 224.0.0.251:5353
	Wait        time.Duration                                 // how long to listen after asking: 1s
	Listen      func(context.Context) (net.PacketConn, error) // how to get a socket
	NewID       func() uint16                                 // query IDs: random
	Concurrency int                                           // devices asked at once: 4
}

func (o Options) withDefaults() Options {
	if o.Group == nil {
		o.Group = &net.UDPAddr{IP: net.ParseIP(Group), Port: Port}
	}
	if o.Wait <= 0 {
		o.Wait = defaultWait
	}
	if o.Listen == nil {
		o.Listen = listen
	}
	if o.NewID == nil {
		o.NewID = randomID
	}
	if o.Concurrency <= 0 {
		o.Concurrency = defaultConcurrency
	}
	return o
}

// listen opens an ordinary UDP socket on an ephemeral port. Asking from a
// port other than 5353 makes this a "one-shot" query: responders reply
// directly to us rather than multicasting the answer to the whole network
// (RFC 6762 §6.7), so no membership in the multicast group is needed.
func listen(ctx context.Context) (net.PacketConn, error) {
	var lc net.ListenConfig
	return lc.ListenPacket(ctx, "udp4", ":0")
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
	id := e.opts.NewID()
	query, err := dnswire.Query(id, name, dnswire.QueryOptions{Type: dnsmessage.TypePTR})
	if err != nil {
		return err
	}

	conn, err := e.opts.Listen(ctx)
	if err != nil {
		return fmt.Errorf("cannot open a socket for mDNS: %w", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(e.opts.Wait)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}

	report(engine.ProbeEvent{Kind: engine.KindSent, Target: d.Key,
		Message: fmt.Sprintf("PTR? %s → %s (id 0x%04x, %d bytes, one-shot; every host on the segment hears this)", name, e.opts.Group, id, len(query))})
	if _, err := conn.WriteTo(query, e.opts.Group); err != nil {
		return fmt.Errorf("cannot send to %s: %w", e.opts.Group, err)
	}

	answered := false
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			if !answered {
				report(engine.ProbeEvent{Kind: engine.KindInfo, Target: d.Key,
					Message: fmt.Sprintf("no mDNS answer for %s within %s; this device probably does not run a responder", ip, e.opts.Wait)})
			}
			return nil
		}
		wire := append([]byte(nil), buf[:n]...)
		if e.handle(d.Key, ip, name, id, wire, from, answered, emit, report) {
			answered = true
		}
		if time.Now().After(deadline) {
			return nil
		}
	}
}

// handle decodes one reply and reports it. It returns true when the reply
// carried the name shoal asked for. Later replies are logged but not emitted:
// the first answer wins, and a second one is worth seeing rather than
// silently overwriting the first.
func (e *Enricher) handle(key string, ip net.IP, name string, id uint16, wire []byte, from net.Addr, answered bool, emit engine.Emit, report engine.Report) bool {
	resp, err := ParseResponse(wire)
	if err != nil {
		report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
			Message: fmt.Sprintf("%s sent %d bytes shoal could not decode: %v", from, len(wire), err)})
		return false
	}
	if resp.ID != id {
		report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
			Message: fmt.Sprintf("discarding a reply from %s: id 0x%04x does not match query id 0x%04x", from, resp.ID, id)})
		return false
	}
	report(engine.ProbeEvent{Kind: engine.KindReceived, Target: key,
		Message: fmt.Sprintf("%s: %s", from, resp.Summary())})

	records := resp.PointedAt(name)
	if len(records) == 0 {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
			Message: fmt.Sprintf("%s answered but carried no PTR for %s", from, name)})
		return false
	}
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
