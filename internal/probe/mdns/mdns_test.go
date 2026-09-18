package mdns

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/BT10011/shoal/internal/dnswire"
	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
)

func loadHex(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var clean strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		clean.WriteString(strings.ReplaceAll(line, " ", ""))
	}
	b, err := hex.DecodeString(clean.String())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const (
	deviceIP    = "192.168.1.42"
	deviceKey   = "3c:22:fb:9e:01:77"
	reverseName = "42.1.168.192.in-addr.arpa."
	captureID   = 0x4242
)

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseResponseFixture(t *testing.T) {
	resp, err := ParseResponse(loadHex(t, "reply-ptr.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != captureID {
		t.Errorf("ID = 0x%04x, want 0x%04x", resp.ID, captureID)
	}
	if !resp.Authoritative {
		t.Error("Authoritative = false; an mDNS responder always answers authoritatively")
	}
	if len(resp.Records) != 2 {
		t.Fatalf("Records = %d, want the answer and the additional: %+v", len(resp.Records), resp.Records)
	}

	answer := resp.Records[0]
	if answer.Section != SectionAnswer || answer.Type != dnsmessage.TypePTR {
		t.Errorf("first record = %s %s, want answer PTR", answer.Section, TypeName(answer.Type))
	}
	if answer.Name != "42.1.168.192.in-addr.arpa" {
		t.Errorf("owner = %q, want the reverse name without its trailing dot", answer.Name)
	}
	if answer.PTR != "laptop.local" {
		t.Errorf("PTR = %q, want laptop.local", answer.PTR)
	}
	if answer.TTL != 10*time.Second {
		t.Errorf("TTL = %s, want 10s", answer.TTL)
	}

	extra := resp.Records[1]
	if extra.Section != SectionAdditional || extra.Type != dnsmessage.TypeTXT {
		t.Errorf("second record = %s %s, want additional TXT", extra.Section, TypeName(extra.Type))
	}
	if got := extra.Value(); !strings.Contains(got, "model=Example1,1") {
		t.Errorf("TXT value = %q, want the device-info keys", got)
	}

	if got := resp.PointedAt(reverseName); len(got) != 1 || got[0].PTR != "laptop.local" {
		t.Errorf("PointedAt(%s) = %+v, want the one PTR", reverseName, got)
	}
	if got := resp.PointedAt("1.1.168.192.in-addr.arpa."); len(got) != 0 {
		t.Errorf("PointedAt(other name) = %+v, want none", got)
	}
	if got := resp.Summary(); !strings.Contains(got, "answer PTR") || !strings.Contains(got, "additional TXT") {
		t.Errorf("Summary = %q, want both sections named", got)
	}
}

func TestParseResponseRejectsGarbage(t *testing.T) {
	if _, err := ParseResponse([]byte{0x42, 0x42, 0x84}); err == nil {
		t.Error("ParseResponse(short) = nil error, want a decode error")
	}
}

// packet is one datagram a fake socket will hand back.
type packet struct {
	wire []byte
	from net.Addr
}

// fakeConn is a net.PacketConn that replays scripted replies and records what
// was sent. An empty queue reads as a timeout, like a silent network.
type fakeConn struct {
	replies []packet
	sent    [][]byte
	closed  bool
}

func udpAddr(ip string) *net.UDPAddr { return &net.UDPAddr{IP: net.ParseIP(ip), Port: Port} }

func (c *fakeConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if len(c.replies) == 0 {
		return 0, nil, os.ErrDeadlineExceeded
	}
	next := c.replies[0]
	c.replies = c.replies[1:]
	return copy(p, next.wire), next.from, nil
}

func (c *fakeConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.sent = append(c.sent, append([]byte(nil), p...))
	return len(p), nil
}

func (c *fakeConn) Close() error                     { c.closed = true; return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return udpAddr("0.0.0.0") }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

type recorder struct {
	obs    []model.Observation
	events []engine.ProbeEvent
}

func (r *recorder) emit(o model.Observation)   { r.obs = append(r.obs, o) }
func (r *recorder) report(e engine.ProbeEvent) { r.events = append(r.events, e) }

func (r *recorder) messages(k engine.EventKind) []string {
	var out []string
	for _, e := range r.events {
		if e.Kind == k {
			out = append(out, e.Message)
		}
	}
	return out
}

func snapshot(key, ip string) model.DeviceSnapshot {
	d := model.NewDevice(key, time.Now())
	d.Add(model.Observation{DeviceKey: key, Field: model.FieldIP, Value: ip, Source: "arp", Confidence: 1, At: time.Now()})
	return d.Snapshot()
}

// fakeAsker answers like OpenMulticast or OpenOneShot over a scripted socket.
type fakeAsker struct {
	*fakeConn
	oneShot bool
	sendErr error
}

func (a *fakeAsker) OneShot() bool { return a.oneShot }
func (a *fakeAsker) Send(q []byte, _ *net.UDPAddr) error {
	if a.sendErr != nil {
		return a.sendErr
	}
	_, err := a.WriteTo(q, nil)
	return err
}

// probeWith builds an enricher that asks from port 5353 over a socket that
// replays the given multicast traffic.
func probeWith(replies ...packet) (*Enricher, *fakeConn) {
	conn := &fakeConn{replies: replies}
	e := New(Options{
		Wait:  50 * time.Millisecond,
		NewID: func() uint16 { return captureID },
		Open: func(context.Context, netif.Interface, *net.UDPAddr) (Asker, error) {
			return &fakeAsker{fakeConn: conn}, nil
		},
		Fallback: func(context.Context, netif.Interface, *net.UDPAddr) (Asker, error) {
			return nil, errors.New("the fallback must not be used")
		},
	})
	return e, conn
}

// oneShotWith builds an enricher whose port-5353 socket cannot be had, so
// it falls back to asking from an ephemeral port.
func oneShotWith(replies ...packet) (*Enricher, *fakeConn) {
	conn := &fakeConn{replies: replies}
	e := New(Options{
		Wait:  50 * time.Millisecond,
		NewID: func() uint16 { return captureID },
		Open: func(context.Context, netif.Interface, *net.UDPAddr) (Asker, error) {
			return &fakeAsker{fakeConn: &fakeConn{}, sendErr: fmt.Errorf("%w: bind: address already in use", ErrPortHeld)}, nil
		},
		Fallback: func(context.Context, netif.Interface, *net.UDPAddr) (Asker, error) {
			return &fakeAsker{fakeConn: conn, oneShot: true}, nil
		},
	})
	return e, conn
}

// withID returns a copy of a DNS message with its ID replaced.
func withID(wire []byte, id uint16) []byte {
	out := append([]byte(nil), wire...)
	out[0], out[1] = byte(id>>8), byte(id)
	return out
}

func TestEnrichAsksFromPort5353AndMatchesByName(t *testing.T) {
	// Multicast answers carry ID 0 whatever the question's ID was.
	reply := withID(loadHex(t, "reply-ptr.hex"), 0)
	e, conn := probeWith(packet{wire: reply, from: udpAddr(deviceIP)})

	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(conn.sent) != 1 {
		t.Fatalf("sent %d queries, want 1", len(conn.sent))
	}
	if want := withID(loadHex(t, "query-ptr.hex"), 0); !equal(conn.sent[0], want) {
		t.Errorf("query =\n%s\nwant ID 0 (RFC 6762 §18.1)\n%s", hex.Dump(conn.sent[0]), hex.Dump(want))
	}
	if !conn.closed {
		t.Error("socket left open")
	}
	if len(rec.obs) != 1 {
		t.Fatalf("emitted %d observations, want 1: %+v", len(rec.obs), rec.obs)
	}
	o := rec.obs[0]
	if o.Field != model.FieldHostname || o.Value != "laptop.local" {
		t.Errorf("observation = %s %q, want hostname laptop.local", o.Field, o.Value)
	}
	if o.DeviceKey != deviceKey || o.Confidence != Confidence || o.TTL != minTTL {
		t.Errorf("observation = %+v", o)
	}
	if !equal(o.Raw, reply) {
		t.Error("Raw does not hold the reply bytes")
	}
	for _, want := range []string{reverseName, "answered by " + deviceIP + " itself", "mDNS TTL 10s"} {
		if !strings.Contains(o.Method, want) {
			t.Errorf("Method = %q, want it to mention %q", o.Method, want)
		}
	}
	if sent := rec.messages(engine.KindSent); len(sent) != 1 || !strings.Contains(sent[0], "from port 5353, so the answer is multicast") {
		t.Errorf("sent events = %v, want one saying the answer comes back by multicast", sent)
	}
	if got := rec.messages(engine.KindReceived); len(got) != 1 || !strings.Contains(got[0], "laptop.local") {
		t.Errorf("received events = %v, want one naming the answer", got)
	}
}

func TestEnrichIgnoresOtherConversationsOnTheGroup(t *testing.T) {
	other, err := dnswire.Query(0, "_airplay._tcp.local.", dnswire.QueryOptions{Type: dnsmessage.TypePTR})
	if err != nil {
		t.Fatal(err)
	}
	reply := withID(loadHex(t, "reply-ptr.hex"), 0)
	e, _ := probeWith(
		packet{wire: other, from: udpAddr("192.168.1.9")},           // someone else's question
		packet{wire: []byte{1, 2, 3}, from: udpAddr("192.168.1.8")}, // someone else's garbage
		packet{wire: reply, from: udpAddr(deviceIP)},
	)
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(rec.obs) != 1 {
		t.Fatalf("emitted %d observations, want the one answer", len(rec.obs))
	}
	if errs, info := rec.messages(engine.KindError), rec.messages(engine.KindInfo); len(errs) != 0 || len(info) != 0 {
		t.Errorf("traffic that is not an answer to us must pass silently: errors %v info %v", errs, info)
	}
}

func TestEnrichFallsBackWhenPort5353CannotBeShared(t *testing.T) {
	e, conn := oneShotWith(packet{wire: loadHex(t, "reply-ptr.hex"), from: udpAddr(deviceIP)})
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if want := loadHex(t, "query-ptr.hex"); len(conn.sent) != 1 || !equal(conn.sent[0], want) {
		t.Fatalf("a one-shot question carries a random ID the answer must echo: %v", conn.sent)
	}
	if len(rec.obs) != 1 || rec.obs[0].Value != "laptop.local" {
		t.Fatalf("observations = %+v", rec.obs)
	}
	info := strings.Join(rec.messages(engine.KindInfo), "\n")
	if !strings.Contains(info, "port 5353 is held by a program that will not share it") {
		t.Errorf("the fallback must be explained: %q", info)
	}
	if sent := rec.messages(engine.KindSent); len(sent) != 1 || !strings.Contains(sent[0], "one-shot from an ephemeral port") {
		t.Errorf("sent events = %v", sent)
	}
}

func TestEnrichSilenceIsNotAnError(t *testing.T) {
	e, _ := probeWith()
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatalf("Enrich = %v, want nil: most devices do not run a responder", err)
	}
	if len(rec.obs) != 0 {
		t.Errorf("emitted %+v, want nothing", rec.obs)
	}
	if info := rec.messages(engine.KindInfo); len(info) != 1 || !strings.Contains(info[0], "no mDNS answer") || strings.Contains(info[0], "firewall") {
		t.Errorf("info events = %v, want one explaining the silence", info)
	}
	e, _ = oneShotWith()
	rec = recorder{}
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if info := strings.Join(rec.messages(engine.KindInfo), "\n"); !strings.Contains(info, "or a host firewall dropped a unicast reply") {
		t.Errorf("one-shot silence should mention the firewall: %q", info)
	}
}

func TestEnrichDiscardsMismatchedIDOnAOneShotQuestion(t *testing.T) {
	e, _ := oneShotWith(packet{wire: loadHex(t, "reply-ptr.hex"), from: udpAddr(deviceIP)})
	e.opts.NewID = func() uint16 { return 0x9999 }

	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(rec.obs) != 0 {
		t.Errorf("emitted %+v, want nothing from an unmatched reply", rec.obs)
	}
	if errs := rec.messages(engine.KindError); len(errs) != 1 || !strings.Contains(errs[0], "discarding") {
		t.Errorf("error events = %v, want one saying the reply was discarded", errs)
	}
}

func TestEnrichNamesAProxyResponder(t *testing.T) {
	e, _ := probeWith(packet{wire: withID(loadHex(t, "reply-ptr.hex"), 0), from: udpAddr("192.168.1.5")})
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(rec.obs) != 1 {
		t.Fatalf("emitted %d observations, want 1", len(rec.obs))
	}
	method := rec.obs[0].Method
	if !strings.Contains(method, "192.168.1.5 on behalf of "+deviceIP) {
		t.Errorf("Method = %q, want it to say who answered for whom", method)
	}
}

func TestEnrichLogsASecondClaim(t *testing.T) {
	reply := withID(loadHex(t, "reply-ptr.hex"), 0)
	e, _ := probeWith(
		packet{wire: reply, from: udpAddr(deviceIP)},
		packet{wire: reply, from: udpAddr("192.168.1.5")},
	)
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(rec.obs) != 1 {
		t.Fatalf("emitted %d observations, want only the first answer", len(rec.obs))
	}
	if info := rec.messages(engine.KindInfo); len(info) != 1 || !strings.Contains(info[0], "also claims") {
		t.Errorf("info events = %v, want the second claim logged", info)
	}
}

func TestEnrichRejectsDeviceWithoutIP(t *testing.T) {
	e, _ := probeWith()
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, "not-an-ip"), rec.emit, rec.report); err == nil {
		t.Error("Enrich = nil error, want a parse error")
	}
}

func TestEnrichSurfacesSocketFailure(t *testing.T) {
	e := New(Options{Open: func(context.Context, netif.Interface, *net.UDPAddr) (Asker, error) {
		return nil, errors.New("permission denied")
	}})
	var rec recorder
	err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("Enrich = %v, want the socket error", err)
	}
}

func TestNameAndTriggers(t *testing.T) {
	e := New(Options{})
	if e.Name() != "mdns" {
		t.Errorf("Name = %q", e.Name())
	}
	if tr := e.Triggers(); len(tr) != 1 || tr[0] != model.FieldIP {
		t.Errorf("Triggers = %v, want [ip]", tr)
	}
	if e.Concurrency() != defaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", e.Concurrency(), defaultConcurrency)
	}
}
