package nbns

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

// Confidence is how far a NetBIOS name is trusted. Like mDNS it is the
// device's own answer, but the name it gives is a legacy one: fifteen bytes,
// upper-cased, and often truncated from the real hostname, so it sits below
// mDNS and above a PTR record somebody typed into a DHCP server years ago.
const Confidence = 0.8

// MACConfidence covers the adapter address a node reports for itself. It is
// as direct as an ARP reply, but it is a claim rather than something observed
// on the wire, so it stays just below one.
const MACConfidence = 0.9

const (
	defaultTimeout     = time.Second
	defaultConcurrency = 4
)

// Options tune the probe. Zero values are the defaults shown.
type Options struct {
	Port        int                                           // 137
	Timeout     time.Duration                                 // 1s
	Concurrency int                                           // 4
	Listen      func(context.Context) (net.PacketConn, error) // how to get a socket
	NewID       func() uint16                                 // transaction ids
}

func (o Options) withDefaults() Options {
	if o.Port == 0 {
		o.Port = Port
	}
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	if o.Concurrency <= 0 {
		o.Concurrency = defaultConcurrency
	}
	if o.Listen == nil {
		o.Listen = listen
	}
	if o.NewID == nil {
		o.NewID = randomID
	}
	return o
}

// listen opens an ordinary UDP socket. Windows sends these queries from port
// 137 itself, but a node answers whichever port asked, and binding 137 would
// need privileges shoal would rather not ask for.
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

// Enricher asks a host for its NetBIOS name table.
type Enricher struct {
	opts Options
}

// New creates the probe.
func New(opts Options) *Enricher { return &Enricher{opts: opts.withDefaults()} }

func (e *Enricher) Name() string            { return "nbns" }
func (e *Enricher) Triggers() []model.Field { return []model.Field{model.FieldIP} }
func (e *Enricher) Produces() model.Field   { return model.FieldHostname }
func (e *Enricher) Concurrency() int        { return e.opts.Concurrency }

// Enrich sends one node status request and reads the name table that comes
// back. Silence is ordinary: a machine that is not Windows and is not running
// Samba has no reason to answer, and many that do have port 137 firewalled.
func (e *Enricher) Enrich(ctx context.Context, d model.DeviceSnapshot, emit engine.Emit, report engine.Report) error {
	raw := d.Key
	if o, ok := d.ResolvedAt(model.FieldIP, time.Now()); ok {
		raw = o.Value
	}
	ip := net.ParseIP(raw)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("device %s has no IPv4 address for NetBIOS (%q)", d.Key, raw)
	}

	conn, err := e.opts.Listen(ctx)
	if err != nil {
		return fmt.Errorf("cannot open a socket for NetBIOS: %w", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(e.opts.Timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}

	txid := e.opts.NewID()
	query := Query(txid)
	dst := &net.UDPAddr{IP: ip, Port: e.opts.Port}
	report(engine.ProbeEvent{Kind: engine.KindSent, Target: d.Key,
		Message: fmt.Sprintf("node status request for \"*\" → %s (id 0x%04x, %d bytes)", dst, txid, len(query))})
	if _, err := conn.WriteTo(query, dst); err != nil {
		return fmt.Errorf("cannot send to %s: %w", dst, err)
	}

	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			report(engine.ProbeEvent{Kind: engine.KindInfo, Target: d.Key,
				Message: fmt.Sprintf("%s did not answer within %s; it is probably not running NetBIOS, or port %d is filtered", ip, e.opts.Timeout, e.opts.Port)})
			return nil
		}
		if e.handle(d.Key, txid, append([]byte(nil), buf[:n]...), from, emit, report) {
			return nil
		}
	}
}

// handle decodes one reply. It returns true when the reply answered the
// question shoal asked, so no further reading is needed.
func (e *Enricher) handle(key string, txid uint16, wire []byte, from net.Addr, emit engine.Emit, report engine.Report) bool {
	status, err := ParseNodeStatus(wire)
	if err != nil {
		report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
			Message: fmt.Sprintf("%s sent %d bytes shoal could not decode: %v", from, len(wire), err)})
		return false
	}
	if status.TxID != txid {
		report(engine.ProbeEvent{Kind: engine.KindError, Target: key,
			Message: fmt.Sprintf("discarding a reply from %s: id 0x%04x does not match query id 0x%04x", from, status.TxID, txid)})
		return false
	}
	report(engine.ProbeEvent{Kind: engine.KindReceived, Target: key,
		Message: fmt.Sprintf("%s: %s", from, status.Summary())})

	group := ""
	if workgroup, ok := status.Workgroup(); ok {
		group = fmt.Sprintf(", in the %s %s", workgroup.Name, workgroup.Service())
	}
	if name, ok := status.Workstation(); ok {
		emit(model.Observation{
			DeviceKey:  key,
			Field:      model.FieldHostname,
			Value:      name.Name,
			Confidence: Confidence,
			Raw:        wire,
			Method: fmt.Sprintf("NetBIOS node status: %s registered %q as its unique <%02x> %s name%s",
				from, name.Name, name.Suffix, name.Service(), group),
		})
	} else {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key,
			Message: fmt.Sprintf("%s answered but registered no unique workstation name", from)})
	}

	if status.MAC != nil {
		emit(model.Observation{
			DeviceKey:  key,
			Field:      model.FieldMAC,
			Value:      status.MAC.String(),
			Confidence: MACConfidence,
			Raw:        wire,
			Method:     fmt.Sprintf("NetBIOS node status: %s reports %s as its own adapter address", from, status.MAC),
		})
	}
	return true
}
