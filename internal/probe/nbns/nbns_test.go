package nbns

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

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
	txid      = 0x1a2b
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

func TestWildcardEncoding(t *testing.T) {
	got := string(Encode(Wildcard()))
	want := "CK" + strings.Repeat("AA", 15)
	if got != want {
		t.Errorf("Encode(*) = %q, want %q", got, want)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	var name [16]byte
	copy(name[:], "NAS01          ")
	name[15] = 0x20
	back, err := Decode(Encode(name))
	if err != nil {
		t.Fatal(err)
	}
	if back != name {
		t.Errorf("round trip = %q, want %q", back, name)
	}
}

func TestDecodeRejectsBadInput(t *testing.T) {
	if _, err := Decode([]byte("CK")); err == nil {
		t.Error("Decode(short) = nil error, want an error")
	}
	if _, err := Decode([]byte(strings.Repeat("Z", 32))); err == nil {
		t.Error("Decode(out of range) = nil error, want an error: letters run A..P")
	}
}

func TestQueryMatchesFixture(t *testing.T) {
	got := Query(txid)
	if want := loadHex(t, "query.hex"); !equal(got, want) {
		t.Fatalf("Query =\n%s\nwant\n%s", hex.Dump(got), hex.Dump(want))
	}
}

func TestParseNodeStatusFixture(t *testing.T) {
	status, err := ParseNodeStatus(loadHex(t, "node-status.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if status.TxID != txid {
		t.Errorf("TxID = 0x%04x, want 0x%04x", status.TxID, txid)
	}
	if len(status.Names) != 4 {
		t.Fatalf("Names = %+v, want four", status.Names)
	}

	first := status.Names[0]
	if first.Name != "NAS01" {
		t.Errorf("name = %q, want NAS01 with its padding trimmed", first.Name)
	}
	if first.Group {
		t.Error("the workstation name should be unique, not a group name")
	}
	if got := first.Service(); got != "workstation" {
		t.Errorf("service = %q, want workstation", got)
	}
	if got := status.Names[1].Service(); got != "file server" {
		t.Errorf("suffix 0x20 = %q, want file server", got)
	}
	if !status.Names[2].Group {
		t.Error("WORKGROUP <00> should be a group name")
	}
	if got := status.Names[2].Service(); got != "domain or workgroup" {
		t.Errorf("group suffix 0x00 = %q, want domain or workgroup", got)
	}

	name, ok := status.Workstation()
	if !ok || name.Name != "NAS01" {
		t.Errorf("Workstation = %+v, want NAS01", name)
	}
	group, ok := status.Workgroup()
	if !ok || group.Name != "WORKGROUP" {
		t.Errorf("Workgroup = %+v, want WORKGROUP", group)
	}
	if status.MAC.String() != deviceKey {
		t.Errorf("MAC = %s, want %s", status.MAC, deviceKey)
	}
	if got := status.Summary(); !strings.Contains(got, "NAS01 <00>") || !strings.Contains(got, "adapter "+deviceKey) {
		t.Errorf("Summary = %q, want the names and the adapter address", got)
	}
}

func TestParseNodeStatusRejectsBadReplies(t *testing.T) {
	if _, err := ParseNodeStatus([]byte{0x1a, 0x2b}); err == nil {
		t.Error("ParseNodeStatus(short) = nil error, want an error")
	}
	noAnswer := loadHex(t, "node-status.hex")
	noAnswer[6], noAnswer[7] = 0, 0 // answer count
	if _, err := ParseNodeStatus(noAnswer); err == nil {
		t.Error("ParseNodeStatus(no answers) = nil error, want an error")
	}
	truncated := loadHex(t, "node-status.hex")
	if _, err := ParseNodeStatus(truncated[:60]); err == nil {
		t.Error("ParseNodeStatus(truncated) = nil error, want an error")
	}
}

// packet is one datagram a fake socket hands back.
type packet struct {
	wire []byte
	from net.Addr
}

type fakeConn struct {
	replies []packet
	sent    [][]byte
	closed  bool
}

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
func (c *fakeConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
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

func (r *recorder) first(f model.Field) (model.Observation, bool) {
	for _, o := range r.obs {
		if o.Field == f {
			return o, true
		}
	}
	return model.Observation{}, false
}

func snapshot(key, ip string) model.DeviceSnapshot {
	d := model.NewDevice(key, time.Now())
	d.Add(model.Observation{DeviceKey: key, Field: model.FieldIP, Value: ip, Source: "arp", Confidence: 1, At: time.Now()})
	return d.Snapshot()
}

func probeWith(replies ...packet) (*Enricher, *fakeConn) {
	conn := &fakeConn{replies: replies}
	e := New(Options{
		Timeout: 20 * time.Millisecond,
		NewID:   func() uint16 { return txid },
		Listen:  func(context.Context) (net.PacketConn, error) { return conn, nil },
	})
	return e, conn
}

func udpAddr(ip string) *net.UDPAddr { return &net.UDPAddr{IP: net.ParseIP(ip), Port: Port} }

func TestEnrichEmitsNameAndAdapterAddress(t *testing.T) {
	reply := loadHex(t, "node-status.hex")
	e, conn := probeWith(packet{wire: reply, from: udpAddr(deviceIP)})

	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(conn.sent) != 1 {
		t.Fatalf("sent %d queries, want 1", len(conn.sent))
	}
	if want := loadHex(t, "query.hex"); !equal(conn.sent[0], want) {
		t.Errorf("query =\n%s\nwant\n%s", hex.Dump(conn.sent[0]), hex.Dump(want))
	}
	if !conn.closed {
		t.Error("socket left open")
	}

	name, ok := rec.first(model.FieldHostname)
	if !ok {
		t.Fatalf("no hostname emitted from %+v", rec.obs)
	}
	if name.Value != "NAS01" {
		t.Errorf("hostname = %q, want NAS01", name.Value)
	}
	if name.Confidence != Confidence {
		t.Errorf("Confidence = %v, want %v", name.Confidence, Confidence)
	}
	if !equal(name.Raw, reply) {
		t.Error("Raw does not hold the reply bytes")
	}
	for _, want := range []string{"NAS01", "<00>", "workstation", "WORKGROUP"} {
		if !strings.Contains(name.Method, want) {
			t.Errorf("Method = %q, want it to mention %q", name.Method, want)
		}
	}

	mac, ok := rec.first(model.FieldMAC)
	if !ok {
		t.Fatalf("no MAC emitted from %+v", rec.obs)
	}
	if mac.Value != deviceKey {
		t.Errorf("MAC = %q, want %q", mac.Value, deviceKey)
	}
	if mac.Confidence != MACConfidence {
		t.Errorf("MAC confidence = %v, want %v", mac.Confidence, MACConfidence)
	}
	if !strings.Contains(mac.Method, "reports") {
		t.Errorf("Method = %q, want it clear this is the node's own claim", mac.Method)
	}

	if sent := rec.messages(engine.KindSent); len(sent) != 1 || !strings.Contains(sent[0], "node status request") {
		t.Errorf("sent events = %v, want one describing the request", sent)
	}
	if got := rec.messages(engine.KindReceived); len(got) != 1 || !strings.Contains(got[0], "file server") {
		t.Errorf("received events = %v, want one listing the name table", got)
	}
}

func TestEnrichSkipsAZeroAdapterAddress(t *testing.T) {
	// Samba answers with six zero bytes where the adapter address goes.
	reply := loadHex(t, "node-status.hex")
	for i := len(reply) - 52; i < len(reply)-46; i++ {
		reply[i] = 0
	}
	e, _ := probeWith(packet{wire: reply, from: udpAddr(deviceIP)})

	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if _, ok := rec.first(model.FieldMAC); ok {
		t.Errorf("emitted a MAC from %+v, want none: all zeroes is not an address", rec.obs)
	}
	if _, ok := rec.first(model.FieldHostname); !ok {
		t.Error("the name should still be emitted")
	}
}

func TestEnrichSilenceIsNotAnError(t *testing.T) {
	e, _ := probeWith()
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatalf("Enrich = %v, want nil: most devices do not run NetBIOS", err)
	}
	if len(rec.obs) != 0 {
		t.Errorf("emitted %+v, want nothing", rec.obs)
	}
	if info := rec.messages(engine.KindInfo); len(info) != 1 || !strings.Contains(info[0], "not running NetBIOS") {
		t.Errorf("info events = %v, want one explaining the silence", info)
	}
}

func TestEnrichDiscardsMismatchedID(t *testing.T) {
	e, _ := probeWith(packet{wire: loadHex(t, "node-status.hex"), from: udpAddr(deviceIP)})
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

func TestEnrichReportsUndecodableReply(t *testing.T) {
	e, _ := probeWith(packet{wire: []byte{0x1a, 0x2b, 0x84}, from: udpAddr(deviceIP)})
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if errs := rec.messages(engine.KindError); len(errs) == 0 {
		t.Error("want the undecodable reply reported")
	}
}

func TestEnrichRejectsNonIPv4(t *testing.T) {
	e, _ := probeWith()
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot(deviceKey, "fe80::1"), rec.emit, rec.report); err == nil {
		t.Error("Enrich = nil error, want a rejection: NetBIOS over TCP/IP is IPv4 only")
	}
}

func TestEnrichSurfacesSocketFailure(t *testing.T) {
	e := New(Options{Listen: func(context.Context) (net.PacketConn, error) {
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
	if e.Name() != "nbns" {
		t.Errorf("Name = %q", e.Name())
	}
	if tr := e.Triggers(); len(tr) != 1 || tr[0] != model.FieldIP {
		t.Errorf("Triggers = %v, want [ip]", tr)
	}
	if e.Concurrency() != defaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", e.Concurrency(), defaultConcurrency)
	}
	if e.opts.Port != Port {
		t.Errorf("Port = %d, want %d", e.opts.Port, Port)
	}
}
