package icmp

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	xicmp "golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
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
	deviceKey = "00:11:32:7f:a2:c4"
	deviceIP  = "192.168.1.20"
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

func TestEchoMatchesFixture(t *testing.T) {
	got, err := Echo(0x1234, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := loadHex(t, "echo-request.hex"); !equal(got, want) {
		t.Fatalf("Echo =\n%s\nwant\n%s", hex.Dump(got), hex.Dump(want))
	}
}

func TestParseReplyFixture(t *testing.T) {
	reply, err := ParseReply(loadHex(t, "echo-reply.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Echo {
		t.Error("Echo = false, want an echo reply")
	}
	if reply.ID != 0x1234 || reply.Seq != 1 {
		t.Errorf("id/seq = 0x%04x/%d, want 0x1234/1", reply.ID, reply.Seq)
	}
	if !reply.Mine {
		t.Error("Mine = false, want the payload recognised as shoal's")
	}
}

func TestParseReplyIgnoresAnotherProcessesPing(t *testing.T) {
	msg := xicmp.Message{Type: ipv4.ICMPTypeEchoReply, Body: &xicmp.Echo{ID: 7, Seq: 1, Data: []byte("ping")}}
	wire, err := msg.Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := ParseReply(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Echo {
		t.Error("Echo = false, want it recognised as an echo reply")
	}
	if reply.Mine {
		t.Error("Mine = true, want a foreign payload rejected")
	}
}

func TestParseReplyReadsUnreachable(t *testing.T) {
	msg := xicmp.Message{
		Type: ipv4.ICMPTypeDestinationUnreachable,
		Code: 1,
		Body: &xicmp.DstUnreach{Data: make([]byte, 28)},
	}
	wire, err := msg.Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := ParseReply(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Unreachable {
		t.Error("Unreachable = false, want the report recognised")
	}
}

func TestParseReplyRejectsGarbage(t *testing.T) {
	if _, err := ParseReply([]byte{0x08}); err == nil {
		t.Error("ParseReply(short) = nil error, want a decode error")
	}
}

// fakeConn answers echo requests according to respond, which may return
// nothing to model a host that ignores ICMP.
type fakeConn struct {
	respond func(req []byte) [][]byte
	queue   [][]byte
	sent    [][]byte
	closed  bool
}

func (c *fakeConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.sent = append(c.sent, append([]byte(nil), p...))
	if c.respond != nil {
		c.queue = append(c.queue, c.respond(p)...)
	}
	return len(p), nil
}

func (c *fakeConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if len(c.queue) == 0 {
		return 0, nil, os.ErrDeadlineExceeded
	}
	next := c.queue[0]
	c.queue = c.queue[1:]
	return copy(p, next), &net.UDPAddr{IP: net.ParseIP(deviceIP)}, nil
}

func (c *fakeConn) Close() error                     { c.closed = true; return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

// echoBack turns a request into the reply the host would send.
func echoBack(req []byte) [][]byte {
	reply := append([]byte(nil), req...)
	reply[0] = 0 // type 8 (echo) becomes type 0 (echo reply)
	return [][]byte{reply}
}

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

func probeWith(respond func([]byte) [][]byte) (*Enricher, *fakeConn) {
	conn := &fakeConn{respond: respond}
	e := New(Options{
		Count:    3,
		Interval: time.Millisecond,
		Timeout:  5 * time.Millisecond,
		Listen: func(context.Context) (net.PacketConn, Mode, error) {
			return conn, ModeDatagram, nil
		},
	})
	return e, conn
}

func TestEnrichMeasuresRoundTrips(t *testing.T) {
	e, conn := probeWith(echoBack)
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(conn.sent) != 3 {
		t.Fatalf("sent %d echo requests, want 3", len(conn.sent))
	}
	if !conn.closed {
		t.Error("socket left open")
	}
	if len(rec.obs) != 1 {
		t.Fatalf("emitted %d observations, want 1: %+v", len(rec.obs), rec.obs)
	}
	o := rec.obs[0]
	if o.Field != model.FieldLatency {
		t.Errorf("field = %s, want latency", o.Field)
	}
	if !strings.HasSuffix(o.Value, "ms") {
		t.Errorf("value = %q, want a value the RTT column can show", o.Value)
	}
	if o.DeviceKey != deviceKey {
		t.Errorf("DeviceKey = %q, want the device's key", o.DeviceKey)
	}
	for _, want := range []string{deviceIP, "best of 3", string(ModeDatagram)} {
		if !strings.Contains(o.Method, want) {
			t.Errorf("Method = %q, want it to mention %q", o.Method, want)
		}
	}
	if got := rec.messages(engine.KindSent); len(got) != 3 {
		t.Errorf("sent events = %v, want three", got)
	}
	if got := rec.messages(engine.KindReceived); len(got) != 3 {
		t.Errorf("received events = %v, want three", got)
	}
}

func TestEnrichSilenceIsNotAnError(t *testing.T) {
	e, _ := probeWith(nil)
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatalf("Enrich = %v, want nil: ignoring ICMP is a configuration, not a failure", err)
	}
	if len(rec.obs) != 0 {
		t.Errorf("emitted %+v, want nothing", rec.obs)
	}
	if info := rec.messages(engine.KindInfo); len(info) != 1 || !strings.Contains(info[0], "ignore ICMP") {
		t.Errorf("info events = %v, want one explaining the silence", info)
	}
}

func TestEnrichReportsUnreachable(t *testing.T) {
	unreachable := func([]byte) [][]byte {
		msg := xicmp.Message{Type: ipv4.ICMPTypeDestinationUnreachable, Code: 1, Body: &xicmp.DstUnreach{Data: make([]byte, 28)}}
		wire, err := msg.Marshal(nil)
		if err != nil {
			panic(err)
		}
		return [][]byte{wire}
	}
	e, _ := probeWith(unreachable)
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(rec.obs) != 0 {
		t.Errorf("emitted %+v, want no latency for an unreachable host", rec.obs)
	}
	info := rec.messages(engine.KindInfo)
	if len(info) == 0 || !strings.Contains(strings.Join(info, " "), "unreachable") {
		t.Errorf("info events = %v, want the unreachable report", info)
	}
}

func TestEnrichIgnoresAStrangersReply(t *testing.T) {
	stranger := func([]byte) [][]byte {
		msg := xicmp.Message{Type: ipv4.ICMPTypeEchoReply, Body: &xicmp.Echo{ID: 9, Seq: 9, Data: []byte("someone else")}}
		wire, err := msg.Marshal(nil)
		if err != nil {
			panic(err)
		}
		return [][]byte{wire}
	}
	e, _ := probeWith(stranger)
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(rec.obs) != 0 {
		t.Errorf("emitted %+v, want nothing from another process's ping", rec.obs)
	}
}

func TestEnrichSurfacesSocketFailure(t *testing.T) {
	e := New(Options{Listen: func(context.Context) (net.PacketConn, Mode, error) {
		return nil, "", errors.New("no ICMP socket available")
	}})
	var rec recorder
	err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report)
	if err == nil || !strings.Contains(err.Error(), "no ICMP socket") {
		t.Errorf("Enrich = %v, want the socket error", err)
	}
}

func TestEnrichRejectsDeviceWithoutIPv4(t *testing.T) {
	e, _ := probeWith(echoBack)
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, "fe80::1"), rec.emit, rec.report); err == nil {
		t.Error("Enrich = nil error, want a rejection: shoal pings IPv4 only")
	}
}

func TestModeAddressing(t *testing.T) {
	ip := net.ParseIP(deviceIP)
	if _, ok := ModeDatagram.addr(ip).(*net.UDPAddr); !ok {
		t.Error("datagram mode should address the host like UDP")
	}
	if _, ok := ModeRaw.addr(ip).(*net.IPAddr); !ok {
		t.Error("raw mode should address the host by IP")
	}
}

func TestFormatRTT(t *testing.T) {
	cases := map[time.Duration]string{
		2400 * time.Microsecond: "2.4ms",
		time.Millisecond:        "1.0ms",
		15 * time.Millisecond:   "15.0ms",
	}
	for in, want := range cases {
		if got := FormatRTT(in); got != want {
			t.Errorf("FormatRTT(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestNameAndTriggers(t *testing.T) {
	e := New(Options{})
	if e.Name() != "icmp" {
		t.Errorf("Name = %q", e.Name())
	}
	if tr := e.Triggers(); len(tr) != 1 || tr[0] != model.FieldIP {
		t.Errorf("Triggers = %v, want [ip]", tr)
	}
	if e.Concurrency() != defaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", e.Concurrency(), defaultConcurrency)
	}
}
