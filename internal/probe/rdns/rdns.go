package rdns

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/BT10011/shoal/internal/dnswire"
	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

// Exchange sends one query to one resolver and returns the reply and how long
// the round trip took. It is an option so tests can answer from fixtures.
type Exchange func(ctx context.Context, r Resolver, query []byte) (reply []byte, rtt time.Duration, err error)

// Confidence is how far a PTR answer is trusted. It sits below mDNS because
// reverse DNS says what the network's administrator wrote down, which may be
// stale, generic (dhcp-126.example.net) or simply never updated, while mDNS
// is the device answering for itself.
const Confidence = 0.7

// minTTL keeps a very short-lived record on screen long enough to read. DNS
// TTLs of 0 mean "do not cache at all", which would make the hostname column
// flicker; the method string always reports the true TTL.
const minTTL = time.Minute

const (
	defaultTimeout     = 2 * time.Second
	defaultConcurrency = 4
)

// Options tune the probe. Zero values are the defaults shown.
type Options struct {
	Resolvers   []Resolver    // servers to ask, in order: SystemResolvers(nil)
	Exchange    Exchange      // how to talk to them: one UDP query
	Timeout     time.Duration // per resolver: 2s
	Concurrency int           // devices looked up at once: 4
	NewID       func() uint16 // query IDs: random
}

func (o Options) withDefaults() Options {
	if o.Resolvers == nil {
		o.Resolvers = SystemResolvers(nil)
	}
	if o.Exchange == nil {
		o.Exchange = UDPExchange
	}
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	if o.Concurrency <= 0 {
		o.Concurrency = defaultConcurrency
	}
	if o.NewID == nil {
		o.NewID = randomID
	}
	return o
}

// randomID picks an unpredictable query ID. A guessable one would let an
// off-path answer be accepted in place of the resolver's.
func randomID() uint16 {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint16(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint16(b[:])
}

// Enricher looks up a device's name by its IP address.
type Enricher struct {
	opts Options
}

// New creates the probe.
func New(opts Options) *Enricher { return &Enricher{opts: opts.withDefaults()} }

func (e *Enricher) Name() string            { return "rdns" }
func (e *Enricher) Triggers() []model.Field { return []model.Field{model.FieldIP} }
func (e *Enricher) Concurrency() int        { return e.opts.Concurrency }

// Resolvers returns the servers this probe will ask, for display.
func (e *Enricher) Resolvers() []Resolver { return e.opts.Resolvers }

// Enrich asks each resolver in turn for the device's PTR record and stops at
// the first definite answer — a name, or an authoritative "no such name".
// A resolver that is silent, broken or refuses is reported and passed over.
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
	if len(e.opts.Resolvers) == 0 {
		return fmt.Errorf("no DNS resolver to ask: %s names none and no gateway was given", ResolvConf)
	}

	var failures []string
	for _, r := range e.opts.Resolvers {
		done, err := e.ask(ctx, d.Key, name, r, emit, report)
		if done {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		failures = append(failures, fmt.Sprintf("%s: %v", r.Addr, err))
	}
	return fmt.Errorf("no resolver answered for %s (%s)", name, joinLines(failures))
}

// ask runs one query against one resolver. It returns true when the answer
// was definite, so no further resolver needs asking.
func (e *Enricher) ask(ctx context.Context, key, name string, r Resolver, emit engine.Emit, report engine.Report) (bool, error) {
	id := e.opts.NewID()
	query, err := dnswire.Query(id, name, dnswire.QueryOptions{
		Type:             dnsmessage.TypePTR,
		RecursionDesired: true,
	})
	if err != nil {
		return false, err
	}
	report(engine.ProbeEvent{Kind: engine.KindSent, Target: key,
		Message: fmt.Sprintf("PTR? %s → %s (id 0x%04x, %d bytes, %s)", name, r.Addr, id, len(query), r.Origin)})

	ctx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()
	wire, rtt, err := e.opts.Exchange(ctx, r, query)
	if err != nil {
		report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
			Message: fmt.Sprintf("%s did not answer: %v", r.Addr, err)})
		return false, err
	}
	reply, err := ParseReply(wire)
	if err != nil {
		report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
			Message: fmt.Sprintf("%s sent %d bytes shoal could not decode: %v", r.Addr, len(wire), err)})
		return false, err
	}
	if reply.ID != id {
		err := fmt.Errorf("reply id 0x%04x does not match query id 0x%04x", reply.ID, id)
		report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
			Message: fmt.Sprintf("discarding an answer from %s: %v", r.Addr, err)})
		return false, err
	}
	report(engine.ProbeEvent{Kind: engine.KindReceived, Target: key,
		Message: fmt.Sprintf("%s in %s: %s", r.Addr, round(rtt), reply.Summary())})

	switch {
	case reply.RCode == dnsmessage.RCodeSuccess && len(reply.Records) > 0:
		rec := reply.Records[0]
		if len(reply.Records) > 1 {
			report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
				Message: fmt.Sprintf("%s has %d PTR records; showing the first, %s", name, len(reply.Records), rec.Name)})
		}
		emit(model.Observation{
			DeviceKey:  key,
			Field:      model.FieldHostname,
			Value:      rec.Name,
			Confidence: Confidence,
			Raw:        wire,
			TTL:        max(rec.TTL, minTTL),
			Method: fmt.Sprintf("PTR record for %s, answered by %s (%s) in %s, DNS TTL %s",
				name, r.Addr, r.Origin, round(rtt), rec.TTL),
		})
		return true, nil

	case reply.RCode == dnsmessage.RCodeSuccess:
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
			Message: fmt.Sprintf("%s knows %s but holds no PTR record for it, so this address has no name in DNS", r.Addr, name)})
		return true, nil

	case reply.RCode == dnsmessage.RCodeNameError:
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
			Message: fmt.Sprintf("%s says %s does not exist (NXDOMAIN); the address was never given a name in DNS", r.Addr, name)})
		return true, nil

	default:
		err := errors.New(reply.Status())
		report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
			Message: fmt.Sprintf("%s refused or failed the lookup: %s", r.Addr, reply.Status())})
		return false, err
	}
}

// UDPExchange sends the query as a single UDP datagram, the way a resolver
// expects, and reads one reply.
func UDPExchange(ctx context.Context, r Resolver, query []byte) ([]byte, time.Duration, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", r.Addr)
	if err != nil {
		return nil, 0, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, 0, err
		}
	}
	start := time.Now()
	if _, err := conn.Write(query); err != nil {
		return nil, time.Since(start), err
	}
	// 1232 bytes is the EDNS payload size the DNS Flag Day consensus
	// recommends; a PTR reply is far smaller, and a bigger one sets TC.
	buf := make([]byte, 1232)
	n, err := conn.Read(buf)
	rtt := time.Since(start)
	if err != nil {
		return nil, rtt, err
	}
	return buf[:n], rtt, nil
}

// round trims a round trip to a readable precision.
func round(d time.Duration) time.Duration { return d.Round(100 * time.Microsecond) }

func joinLines(s []string) string {
	out := ""
	for i, line := range s {
		if i > 0 {
			out += "; "
		}
		out += line
	}
	return out
}
