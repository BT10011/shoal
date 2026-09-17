// Package icmp measures how long a device takes to answer an echo request —
// the round trip every "ping" reports. It is the one probe that measures the
// network rather than asking a question about it, so an answer also proves
// the address is reachable at layer 3, not merely present at layer 2.
package icmp

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	xicmp "golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

// Payload is sent in every echo request. Unprivileged sockets let the kernel
// choose the ICMP id, and it rewrites the id on the way out, so shoal
// recognises its own replies by this payload and the sequence number instead.
const Payload = "shoal probe"

// protocolICMP is ICMP's IP protocol number, which the parser needs in order
// to know which message set it is decoding.
const protocolICMP = 1

// Mode says which kind of socket carried the probe. It changes nothing about
// the packets, but it decides whether privileges were needed, so it is worth
// showing.
type Mode string

const (
	// ModeDatagram is an unprivileged ICMP socket: the kernel owns the id and
	// only passes back replies to echoes this process sent.
	ModeDatagram Mode = "unprivileged datagram socket"
	// ModeRaw is a raw socket, which needs root or cap_net_raw.
	ModeRaw Mode = "raw socket"
)

// Short is a status-bar-sized name for the socket kind.
func (m Mode) Short() string {
	if m == ModeRaw {
		return "raw"
	}
	return "unprivileged"
}

// addr returns the destination in the form the socket expects. A datagram
// ICMP socket is addressed like UDP; a raw one is addressed by IP.
func (m Mode) addr(ip net.IP) net.Addr {
	if m == ModeRaw {
		return &net.IPAddr{IP: ip}
	}
	return &net.UDPAddr{IP: ip}
}

const (
	defaultCount       = 3
	defaultInterval    = 100 * time.Millisecond
	defaultTimeout     = time.Second
	defaultConcurrency = 8
)

// Options tune the probe. Zero values are the defaults shown.
type Options struct {
	Count       int                                                 // echo requests per device: 3
	Interval    time.Duration                                       // pause between them: 100ms
	Timeout     time.Duration                                       // wait for the last reply: 1s
	Concurrency int                                                 // devices pinged at once: 8
	Listen      func(context.Context) (net.PacketConn, Mode, error) // how to get a socket
}

func (o Options) withDefaults() Options {
	if o.Count <= 0 {
		o.Count = defaultCount
	}
	if o.Interval <= 0 {
		o.Interval = defaultInterval
	}
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	if o.Concurrency <= 0 {
		o.Concurrency = defaultConcurrency
	}
	if o.Listen == nil {
		o.Listen = Listen
	}
	return o
}

// Listen opens an ICMP socket, preferring the unprivileged kind. macOS allows
// datagram ICMP to every process; Linux allows it only to group ids inside
// net.ipv4.ping_group_range, and falls back to a raw socket, which needs
// cap_net_raw.
func Listen(ctx context.Context) (net.PacketConn, Mode, error) {
	conn, dgramErr := xicmp.ListenPacket("udp4", "0.0.0.0")
	if dgramErr == nil {
		return conn, ModeDatagram, nil
	}
	conn, rawErr := xicmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if rawErr == nil {
		return conn, ModeRaw, nil
	}
	return nil, "", fmt.Errorf("no ICMP socket available: unprivileged (%v); raw (%v). On Linux, either widen net.ipv4.ping_group_range or grant cap_net_raw with `make setcap`", dgramErr, rawErr)
}

// CheckAccess reports whether this process can send echo requests at all, and
// how. Callers use it once at startup rather than discovering the failure
// once per device.
func CheckAccess() (Mode, error) {
	conn, mode, err := Listen(context.Background())
	if err != nil {
		return "", err
	}
	conn.Close()
	return mode, nil
}

// Enricher measures the round trip to a device.
type Enricher struct {
	opts Options
}

// New creates the probe.
func New(opts Options) *Enricher { return &Enricher{opts: opts.withDefaults()} }

func (e *Enricher) Name() string            { return "icmp" }
func (e *Enricher) Triggers() []model.Field { return []model.Field{model.FieldIP} }
func (e *Enricher) Concurrency() int        { return e.opts.Concurrency }

// Enrich sends a few echo requests and reports the round trips. A host that
// never answers is not an error: plenty of devices are configured to ignore
// ICMP, and saying so is more useful than failing.
func (e *Enricher) Enrich(ctx context.Context, d model.DeviceSnapshot, emit engine.Emit, report engine.Report) error {
	raw := d.Key
	if o, ok := d.ResolvedAt(model.FieldIP, time.Now()); ok {
		raw = o.Value
	}
	ip := net.ParseIP(raw)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("device %s has no IPv4 address to ping (%q)", d.Key, raw)
	}

	conn, mode, err := e.opts.Listen(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	rtts, err := e.roundTrips(ctx, conn, mode, d.Key, ip, report)
	if err != nil {
		return err
	}
	if len(rtts) == 0 {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: d.Key,
			Message: fmt.Sprintf("%s answered none of %d echo requests; it may be configured to ignore ICMP", ip, e.opts.Count)})
		return nil
	}

	best := rtts[0]
	for _, rtt := range rtts {
		best = min(best, rtt)
	}
	emit(model.Observation{
		DeviceKey:  d.Key,
		Field:      model.FieldLatency,
		Value:      FormatRTT(best),
		Confidence: 1,
		Method: fmt.Sprintf("ICMP echo reply from %s, best of %d (%s), over an %s",
			ip, e.opts.Count, listRTTs(rtts), mode),
	})
	return nil
}

// roundTrips sends the echo requests and collects whatever comes back before
// the deadline. Requests are spaced out so a single lost packet does not hide
// a device that is simply busy.
func (e *Enricher) roundTrips(ctx context.Context, conn net.PacketConn, mode Mode, key string, ip net.IP, report engine.Report) ([]time.Duration, error) {
	dst := mode.addr(ip)
	sent := make(map[int]time.Time, e.opts.Count)
	var rtts []time.Duration

	deadline := time.Now().Add(time.Duration(e.opts.Count-1)*e.opts.Interval + e.opts.Timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}

	for seq := 1; seq <= e.opts.Count; seq++ {
		wire, err := Echo(uint16(seq), uint16(seq))
		if err != nil {
			return nil, err
		}
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return nil, err
		}
		if _, err := conn.WriteTo(wire, dst); err != nil {
			return nil, fmt.Errorf("cannot send an echo request to %s: %w", ip, err)
		}
		sent[seq] = time.Now()
		report(engine.ProbeEvent{Kind: engine.KindSent, Target: key,
			Message: fmt.Sprintf("echo request seq=%d → %s (%d bytes, %s)", seq, ip, len(wire), mode)})

		// Collect replies during the gap before the next request, and after
		// the last one until the deadline.
		last := seq == e.opts.Count
		until := time.Now().Add(e.opts.Interval)
		if last {
			until = deadline
		}
		got, err := e.collect(conn, key, ip, sent, until, last, report)
		if err != nil {
			return nil, err
		}
		rtts = append(rtts, got...)
	}
	return rtts, nil
}

// collect reads replies until the given time, matching them to requests by
// sequence number and payload. After the last request it returns as soon as
// every request has been answered, rather than waiting out a timeout that
// nothing is left to arrive in.
func (e *Enricher) collect(conn net.PacketConn, key string, ip net.IP, sent map[int]time.Time, until time.Time, last bool, report engine.Report) ([]time.Duration, error) {
	var rtts []time.Duration
	buf := make([]byte, 1500)
	for time.Now().Before(until) {
		if err := conn.SetReadDeadline(until); err != nil {
			return rtts, err
		}
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return rtts, nil // a timeout, which is how the loop ends
		}
		reply, err := ParseReply(buf[:n])
		if err != nil {
			report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
				Message: fmt.Sprintf("%s sent %d bytes shoal could not decode: %v", host(from), n, err)})
			continue
		}
		if reply.Unreachable {
			report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
				Message: fmt.Sprintf("%s reports %s unreachable", host(from), ip)})
			continue
		}
		if !reply.Echo || !reply.Mine {
			continue // somebody else's ping, on a shared raw socket
		}
		at, ok := sent[reply.Seq]
		if !ok {
			report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
				Message: fmt.Sprintf("discarding an echo reply from %s: seq=%d was never sent", host(from), reply.Seq)})
			continue
		}
		delete(sent, reply.Seq)
		rtt := time.Since(at)
		rtts = append(rtts, rtt)
		report(engine.ProbeEvent{Kind: engine.KindReceived, Target: key,
			Message: fmt.Sprintf("echo reply seq=%d from %s in %s", reply.Seq, host(from), FormatRTT(rtt))})
		if last && len(sent) == 0 {
			return rtts, nil
		}
	}
	return rtts, nil
}

// host strips the transport port from a source address. An unprivileged ICMP
// socket reports its peer as a UDP address, and the port is always zero and
// always meaningless.
func host(addr net.Addr) string {
	switch a := addr.(type) {
	case *net.UDPAddr:
		return a.IP.String()
	case *net.IPAddr:
		return a.IP.String()
	default:
		return addr.String()
	}
}

// Echo builds an ICMP echo request carrying shoal's payload.
func Echo(id, seq uint16) ([]byte, error) {
	msg := xicmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &xicmp.Echo{ID: int(id), Seq: int(seq), Data: []byte(Payload)},
	}
	return msg.Marshal(nil)
}

// Reply is a decoded ICMP message, reduced to what the probe acts on.
type Reply struct {
	Echo        bool // an echo reply
	Unreachable bool // a destination-unreachable report
	ID, Seq     int
	Mine        bool // carries shoal's payload
}

// ParseReply decodes an ICMP message.
func ParseReply(wire []byte) (Reply, error) {
	msg, err := xicmp.ParseMessage(protocolICMP, wire)
	if err != nil {
		return Reply{}, err
	}
	switch body := msg.Body.(type) {
	case *xicmp.Echo:
		return Reply{
			Echo: msg.Type == ipv4.ICMPTypeEchoReply,
			ID:   body.ID,
			Seq:  body.Seq,
			Mine: string(body.Data) == Payload,
		}, nil
	case *xicmp.DstUnreach:
		return Reply{Unreachable: true}, nil
	default:
		return Reply{}, nil
	}
}

// FormatRTT renders a round trip the way the table shows it.
func FormatRTT(d time.Duration) string {
	return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
}

func listRTTs(rtts []time.Duration) string {
	parts := make([]string, len(rtts))
	for i, rtt := range rtts {
		parts[i] = FormatRTT(rtt)
	}
	return strings.Join(parts, ", ")
}
