package mdns

import (
	"context"
	"net"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
)

// listenConn replays captured datagrams and then ends the listener, standing
// in for a socket that has gone quiet.
type listenConn struct {
	queue   []packet
	onDrain func()
	closed  bool
}

func (c *listenConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if len(c.queue) == 0 {
		if c.onDrain != nil {
			c.onDrain()
		}
		return 0, nil, os.ErrDeadlineExceeded
	}
	next := c.queue[0]
	c.queue = c.queue[1:]
	return copy(p, next.wire), next.from, nil
}

func (c *listenConn) WriteTo(p []byte, _ net.Addr) (int, error) { return len(p), nil }
func (c *listenConn) Close() error                              { c.closed = true; return nil }
func (c *listenConn) LocalAddr() net.Addr                       { return udpAddr("0.0.0.0") }
func (c *listenConn) SetDeadline(time.Time) error               { return nil }
func (c *listenConn) SetReadDeadline(time.Time) error           { return nil }
func (c *listenConn) SetWriteDeadline(time.Time) error          { return nil }

// runListener feeds the given datagrams to a listener and returns what it made
// of them. The listener stops as soon as the datagrams run out.
func runListener(t *testing.T, packets ...packet) *recorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := &listenConn{queue: packets, onDrain: cancel}

	l := NewListener(ListenerOptions{
		Listen: func(context.Context, netif.Interface, *net.UDPAddr) (net.PacketConn, error) {
			return conn, nil
		},
	})
	var rec recorder
	if err := l.Run(ctx, netif.Interface{Name: "en0"}, rec.emit, rec.report); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if !conn.closed {
		t.Error("socket left open")
	}
	return &rec
}

func (r *recorder) values(f model.Field) []string {
	var out []string
	for _, o := range r.obs {
		if o.Field == f {
			out = append(out, o.Value)
		}
	}
	sort.Strings(out)
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

func TestListenerLearnsAHostnameFromAnAnnouncement(t *testing.T) {
	rec := runListener(t, packet{wire: loadHex(t, "announce-host.hex"), from: udpAddr(deviceIP)})

	if got := rec.values(model.FieldIP); len(got) != 1 || got[0] != deviceIP {
		t.Errorf("ip observations = %v, want the sender's address", got)
	}
	o, ok := rec.first(model.FieldHostname)
	if !ok {
		t.Fatalf("no hostname emitted from %+v", rec.obs)
	}
	if o.Value != "laptop.local" {
		t.Errorf("hostname = %q, want laptop.local", o.Value)
	}
	if o.DeviceKey != deviceIP {
		t.Errorf("DeviceKey = %q, want the sender's address; the store links it to a MAC", o.DeviceKey)
	}
	if o.Confidence != Confidence {
		t.Errorf("Confidence = %v, want %v", o.Confidence, Confidence)
	}
	if o.TTL != 10*time.Minute {
		t.Errorf("TTL = %s, want the record's 10m (already longer than the floor)", o.TTL)
	}
	if !strings.Contains(o.Method, "A record") || !strings.Contains(o.Method, "announced by the device itself") {
		t.Errorf("Method = %q, want it to explain the announcement", o.Method)
	}
	if got := rec.messages(engine.KindReceived); len(got) != 1 || !strings.Contains(got[0], "announced") {
		t.Errorf("received events = %v, want one describing the announcement", got)
	}
}

func TestListenerIgnoresAnAddressAnnouncedForAnotherHost(t *testing.T) {
	// The same packet, but arriving from a different sender: a responder
	// speaks for itself, so this A record is not evidence about the sender.
	rec := runListener(t, packet{wire: loadHex(t, "announce-host.hex"), from: udpAddr("192.168.1.57")})

	if _, ok := rec.first(model.FieldHostname); ok {
		t.Errorf("emitted a hostname from %+v, want none", rec.obs)
	}
	if got := rec.values(model.FieldIP); len(got) != 1 || got[0] != "192.168.1.57" {
		t.Errorf("ip observations = %v, want only the sender", got)
	}
}

func TestListenerLearnsServicesFromTheMetaQueryAnswer(t *testing.T) {
	rec := runListener(t, packet{wire: loadHex(t, "announce-services.hex"), from: udpAddr(deviceIP)})

	want := []string{
		"_airplay._tcp", "_companion-link._tcp", "_raop._tcp", "_rfb._tcp",
		"_sftp-ssh._tcp", "_smb._tcp", "_ssh._tcp",
	}
	got := rec.values(model.FieldService)
	if len(got) != len(want) {
		t.Fatalf("services = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("service %d = %q, want %q", i, got[i], want[i])
		}
	}
	o, _ := rec.first(model.FieldService)
	if o.DeviceKey != deviceIP {
		t.Errorf("DeviceKey = %q, want the sender's address", o.DeviceKey)
	}
	if !strings.Contains(o.Method, MetaQuery) {
		t.Errorf("Method = %q, want it to name the meta-query", o.Method)
	}
	if o.TTL != 75*time.Minute {
		t.Errorf("TTL = %s, want the announced 1h15m", o.TTL)
	}
}

func TestListenerNotesAQueryItOverhears(t *testing.T) {
	// A question, not an announcement: no records, but it still proves
	// somebody is at that address.
	rec := runListener(t, packet{wire: loadHex(t, "query-ptr.hex"), from: udpAddr("192.168.1.57")})

	if got := rec.values(model.FieldIP); len(got) != 1 {
		t.Errorf("ip observations = %v, want the asker's address", got)
	}
	if _, ok := rec.first(model.FieldHostname); ok {
		t.Error("a query should teach shoal no names")
	}
	if got := rec.messages(engine.KindReceived); len(got) != 1 || !strings.Contains(got[0], "asked about") {
		t.Errorf("received events = %v, want the question reported", got)
	}
}

func TestListenerReportsUndecodableTraffic(t *testing.T) {
	rec := runListener(t, packet{wire: []byte{0x00, 0x01, 0x02}, from: udpAddr(deviceIP)})
	if errs := rec.messages(engine.KindError); len(errs) != 1 {
		t.Errorf("error events = %v, want one", errs)
	}
	if len(rec.obs) != 0 {
		t.Errorf("emitted %+v, want nothing from a message it could not read", rec.obs)
	}
}

func TestListenerCountsWhatItHears(t *testing.T) {
	pkt := packet{wire: loadHex(t, "announce-host.hex"), from: udpAddr(deviceIP)}
	rec := runListener(t, pkt, pkt, pkt)

	var last engine.ProbeEvent
	for _, e := range rec.events {
		if e.Kind == engine.KindProgress {
			last = e
		}
	}
	if last.Done != 3 {
		t.Errorf("progress = %d, want 3 messages heard", last.Done)
	}
	if last.Total != 0 {
		t.Errorf("Total = %d, want 0: listening has no end to progress towards", last.Total)
	}
}

func TestServiceType(t *testing.T) {
	cases := map[string]string{
		"Living Room._airplay._tcp.local":   "_airplay._tcp",
		"_airplay._tcp.local":               "_airplay._tcp",
		"A1B2C3D4._asquic._udp.local":       "_asquic._udp",
		"laptop._companion-link._tcp.local": "_companion-link._tcp",
		"laptop.local":                      "",
		MetaQuery:                           "",
		"42.1.168.192.in-addr.arpa":         "",
	}
	for in, want := range cases {
		got, ok := ServiceType(in)
		if want == "" {
			if ok {
				t.Errorf("ServiceType(%q) = %q, want no service type", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("ServiceType(%q) = %q (%v), want %q", in, got, ok, want)
		}
	}
}

func TestListenerSurfacesSocketFailure(t *testing.T) {
	l := NewListener(ListenerOptions{
		Listen: func(context.Context, netif.Interface, *net.UDPAddr) (net.PacketConn, error) {
			return nil, os.ErrPermission
		},
	})
	var rec recorder
	if err := l.Run(context.Background(), netif.Interface{Name: "en0"}, rec.emit, rec.report); err == nil {
		t.Error("Run = nil error, want the socket failure")
	}
}

func TestListenerName(t *testing.T) {
	if got := NewListener(ListenerOptions{}).Name(); got != "mdns" {
		t.Errorf("Name = %q, want mdns", got)
	}
}

// Bonjour devices relay one another's records. These build the awkward
// shapes by hand, because a capture of them is a capture of somebody else's
// device announcing a third party's service.

func mustName(t *testing.T, s string) dnsmessage.Name {
	t.Helper()
	n, err := dnsmessage.NewName(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// announcement packs an authoritative mDNS response carrying the given answers.
func announcement(t *testing.T, answers ...dnsmessage.Resource) []byte {
	t.Helper()
	msg := dnsmessage.Message{
		Header:  dnsmessage.Header{Response: true, Authoritative: true},
		Answers: answers,
	}
	wire, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func ptrRecord(t *testing.T, owner, target string) dnsmessage.Resource {
	t.Helper()
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: mustName(t, owner), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET, TTL: 4500},
		Body:   &dnsmessage.PTRResource{PTR: mustName(t, target)},
	}
}

func srvRecord(t *testing.T, owner, target string, port uint16) dnsmessage.Resource {
	t.Helper()
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: mustName(t, owner), Type: dnsmessage.TypeSRV, Class: dnsmessage.ClassINET, TTL: 4500},
		Body:   &dnsmessage.SRVResource{Target: mustName(t, target), Port: port},
	}
}

func aRecord(t *testing.T, owner string, ip string) dnsmessage.Resource {
	t.Helper()
	var addr [4]byte
	copy(addr[:], net.ParseIP(ip).To4())
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: mustName(t, owner), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 4500},
		Body:   &dnsmessage.AResource{A: addr},
	}
}

func TestListenerDoesNotCreditAnInstanceItCannotTieToTheSender(t *testing.T) {
	// A phone announcing the laptop's companion-link instance. Observed on a
	// real network, and the reason this rule exists.
	wire := announcement(t, ptrRecord(t, "_companion-link._tcp.local.", "laptop._companion-link._tcp.local."))
	rec := runListener(t, packet{wire: wire, from: udpAddr("192.168.1.57")})

	if got := rec.values(model.FieldService); len(got) != 0 {
		t.Errorf("services = %v, want none: the sender never showed it serves that instance", got)
	}
	info := strings.Join(rec.messages(engine.KindInfo), " ")
	if !strings.Contains(info, "waiting for an SRV") {
		t.Errorf("info events = %q, want the instance noted but not credited", info)
	}
}

func TestListenerCreditsAServiceTiedToTheSenderByItsOwnRecords(t *testing.T) {
	wire := announcement(t,
		ptrRecord(t, "_asquic._udp.local.", "A1B2C3D4._asquic._udp.local."),
		srvRecord(t, "A1B2C3D4._asquic._udp.local.", "phone.local.", 60605),
		aRecord(t, "phone.local.", "192.168.1.57"),
	)
	rec := runListener(t, packet{wire: wire, from: udpAddr("192.168.1.57")})

	if got := rec.values(model.FieldService); len(got) != 1 || got[0] != "_asquic._udp" {
		t.Fatalf("services = %v, want [_asquic._udp]", got)
	}
	o, _ := rec.first(model.FieldService)
	if !strings.Contains(o.Method, "SRV") || !strings.Contains(o.Method, "as its own name") {
		t.Errorf("Method = %q, want it to show how the service was tied to the device", o.Method)
	}
	if host, ok := rec.first(model.FieldHostname); !ok || host.Value != "phone.local" {
		t.Errorf("hostname = %+v, want the announced name", host)
	}
}

func TestListenerRefusesASRVForAHostTheSenderHasNotClaimed(t *testing.T) {
	wire := announcement(t, srvRecord(t, "laptop._companion-link._tcp.local.", "laptop.local.", 49152))
	rec := runListener(t, packet{wire: wire, from: udpAddr("192.168.1.57")})

	if got := rec.values(model.FieldService); len(got) != 0 {
		t.Errorf("services = %v, want none: the sender is not the serving host", got)
	}
	info := strings.Join(rec.messages(engine.KindInfo), " ")
	if !strings.Contains(info, "not crediting") {
		t.Errorf("info events = %q, want the refusal explained", info)
	}
}

func TestListenerRemembersANameAcrossMessages(t *testing.T) {
	// The address arrives in one packet and the service in the next, which is
	// how a real responder spreads an announcement out.
	name := announcement(t, aRecord(t, "phone.local.", "192.168.1.57"))
	service := announcement(t, srvRecord(t, "A1B2C3D4._asquic._udp.local.", "phone.local.", 60605))
	rec := runListener(t,
		packet{wire: name, from: udpAddr("192.168.1.57")},
		packet{wire: service, from: udpAddr("192.168.1.57")},
	)

	if got := rec.values(model.FieldService); len(got) != 1 || got[0] != "_asquic._udp" {
		t.Errorf("services = %v, want the service credited once the name was known", got)
	}
}
