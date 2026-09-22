package mdns

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
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
	return runListenerWith(t, ListenerOptions{}, packets...)
}

// testSubnet is the network the fake interface is on, so that an address
// from anywhere else is recognisably another network's.
const testSubnet = "192.168.1.0/24"

func runListenerWith(t *testing.T, opts ListenerOptions, packets ...packet) *recorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := &listenConn{queue: packets, onDrain: cancel}

	opts.Listen = func(context.Context, netif.Interface, *net.UDPAddr) (net.PacketConn, error) {
		return conn, nil
	}
	l := NewListener(opts)
	_, subnet, err := net.ParseCIDR(testSubnet)
	if err != nil {
		t.Fatal(err)
	}
	var rec recorder
	if err := l.Run(ctx, netif.Interface{Name: "en0", Subnet: subnet}, rec.emit, rec.report); err != nil {
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
	if o.Source != "mdns" {
		t.Errorf("Source = %q: the listener's facts are mDNS records, credited to mdns", o.Source)
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
	rec := runListener(t,
		packet{wire: loadHex(t, "announce-host.hex"), from: udpAddr(deviceIP)}, // names its own address
		packet{wire: loadHex(t, "announce-services.hex"), from: udpAddr(deviceIP)},
	)

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

func TestListenerHoldsAListUntilTheSenderNamesItself(t *testing.T) {
	// The list first, then the device answering for its own reverse name.
	rec := runListener(t,
		packet{wire: loadHex(t, "announce-services.hex"), from: udpAddr(deviceIP)},
		packet{wire: loadHex(t, "reply-ptr.hex"), from: udpAddr(deviceIP)},
	)
	if got := rec.values(model.FieldService); len(got) != 7 {
		t.Fatalf("held services should be credited once the device names itself: %v", got)
	}
	o, _ := rec.first(model.FieldService)
	if !strings.Contains(o.Method, "credited once it had named its own address") {
		t.Errorf("Method = %q", o.Method)
	}
	info := strings.Join(rec.messages(engine.KindInfo), "\n")
	for _, want := range []string{"holding it 2s in case", "has named its own address, so the 7 service types it listed earlier are its own"} {
		if !strings.Contains(info, want) {
			t.Errorf("log missing %q\n%s", want, info)
		}
	}
}

func TestListenerReadsAServiceInstanceNameContainingADot(t *testing.T) {
	// The NDI case. A DNS-SD instance name is one label of arbitrary text,
	// and NDI builds it from a Mac's hostname, so it ends up holding a dot.
	// x/net's parser rejects that and fails the whole message, which is why
	// shoal decodes names itself.
	rec := runListener(t, packet{wire: loadHex(t, "reply-ndi.hex"), from: udpAddr(deviceIP)})

	if got := rec.values(model.FieldService); len(got) != 1 || got[0] != "_ndi._tcp" {
		t.Fatalf("services = %v, want [_ndi._tcp]", got)
	}
	if errs := rec.messages(engine.KindError); len(errs) != 0 {
		t.Errorf("the message should decode cleanly: %v", errs)
	}
	// The address record shares the message, and used to be lost with it.
	if h, ok := rec.first(model.FieldHostname); !ok || h.Value != "stage-mbp.local" {
		t.Errorf("hostname = %+v, want the A record in the same message", h)
	}
	o, _ := rec.first(model.FieldService)
	if !strings.Contains(o.Method, "STAGE-MBP.LOCAL (Scan Converter)") {
		t.Errorf("Method = %q, want the instance name shown as the device spells it", o.Method)
	}
}

func TestParseResponseKeepsADotInsideALabel(t *testing.T) {
	resp, err := ParseResponse(loadHex(t, "reply-ndi.hex"))
	if err != nil {
		t.Fatalf("ParseResponse = %v, want the message to decode", err)
	}
	if len(resp.Records) != 4 {
		t.Fatalf("records = %d, want all four", len(resp.Records))
	}
	want := "STAGE-MBP.LOCAL (Scan Converter)._ndi._tcp.local"
	if got := resp.Records[0].PTR; got != want {
		t.Errorf("PTR = %q, want %q", got, want)
	}
	if got, ok := ServiceType(want); !ok || got != "_ndi._tcp" {
		t.Errorf("ServiceType(%q) = %q (%v), want _ndi._tcp", want, got, ok)
	}
	if got := resp.Records[1].SRV; got.Target != "stage-mbp.local" || got.Port != 5961 {
		t.Errorf("SRV = %+v, want stage-mbp.local:5961", got)
	}
	if got := resp.Records[3].Addr.String(); got != deviceIP {
		t.Errorf("A = %s, want %s: the record that used to be lost with the message", got, deviceIP)
	}
}

func TestParseResponseRefusesAMessageItCannotTrust(t *testing.T) {
	good := loadHex(t, "reply-ndi.hex")
	for name, wire := range map[string][]byte{
		"truncated mid-record": good[:len(good)-5],
		"header only":          good[:12],
		"empty":                nil,
		"pointer past the end": {0, 0, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0xc0, 0xff},
		"pointer to itself":    {0, 0, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0xc0, 0x0c},
	} {
		if _, err := ParseResponse(wire); err == nil {
			t.Errorf("%s: ParseResponse = nil error, want a refusal", name)
		}
	}
}

func TestListenerCreditsAListEvenWhenTheSenderNeverNamesItself(t *testing.T) {
	// The common case, and the one that used to be lost: a responder
	// answering "which services are here?" sends PTR records and nothing
	// else. There is no address record to wait for, so waiting for one
	// meant almost no service was ever credited.
	rec := runListenerWith(t, ListenerOptions{Settle: time.Nanosecond},
		packet{wire: loadHex(t, "announce-services.hex"), from: udpAddr(deviceIP)},
	)
	if got := rec.values(model.FieldService); len(got) != 7 {
		t.Fatalf("services = %v, want the 7 types the device listed", got)
	}
	o, _ := rec.first(model.FieldService)
	if !strings.Contains(o.Method, MetaQuery) {
		t.Errorf("Method = %q, want it to name the meta-query", o.Method)
	}
	if !strings.Contains(o.Method, "no sign that this sender was relaying") {
		t.Errorf("Method = %q, want it to say why the list was taken as the sender's own", o.Method)
	}
}

func TestListenerCreditsNothingToARelay(t *testing.T) {
	// A router repeating another VLAN's answers. It gives itself away by
	// carrying an address record for a host on another network, which no
	// device that speaks only for itself ever does.
	foreign := announcement(t, aRecord(t, "faraway.local.", "192.0.2.77"))
	rec := runListener(t,
		packet{wire: loadHex(t, "announce-services.hex"), from: udpAddr("192.168.1.1")},
		packet{wire: foreign, from: udpAddr("192.168.1.1")},
		packet{wire: loadHex(t, "announce-services.hex"), from: udpAddr("192.168.1.1")},
	)
	if got := rec.values(model.FieldService); len(got) != 0 {
		t.Fatalf("a relay must not be credited with what it relays: %v", got)
	}
	if _, ok := rec.first(model.FieldHostname); ok {
		t.Error("a relayed address record names somebody else, so it is no hostname for the sender")
	}
	info := strings.Join(rec.messages(engine.KindInfo), "\n")
	for _, want := range []string{
		"is relaying another network's mDNS",
		"192.0.2.77, an address on another network",
		"7 service types it had listed are dropped",
		"not credited: 192.168.1.1 was relaying",
	} {
		if !strings.Contains(info, want) {
			t.Errorf("log missing %q\n%s", want, info)
		}
	}
}

func TestListenerTreatsASecondAddressOnThisSubnetAsItsOwnBusiness(t *testing.T) {
	// Only an address on another network marks a relay: a device that also
	// answers for a neighbour on this subnet is not forwarding a VLAN.
	wire := announcement(t,
		aRecord(t, "laptop.local.", deviceIP),
		aRecord(t, "neighbour.local.", "192.168.1.58"),
	)
	rec := runListener(t,
		packet{wire: wire, from: udpAddr(deviceIP)},
		packet{wire: loadHex(t, "announce-services.hex"), from: udpAddr(deviceIP)},
	)
	if got := rec.values(model.FieldService); len(got) != 7 {
		t.Fatalf("services = %v, want the list credited", got)
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
	if got := NewListener(ListenerOptions{}).Name(); got != "dns-sd" {
		t.Errorf("Name = %q, want dns-sd, apart from the mdns enricher", got)
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
	if !strings.Contains(info, "holding the service until it announces an address for that name") {
		t.Errorf("info events = %q, want the refusal explained", info)
	}
}

func TestListenerCreditsAServiceWhoseAddressArrivesAfterTheSRV(t *testing.T) {
	// Responders split long answers across messages, and nothing says the
	// address record comes first. A Dante or NDI device announcing its
	// service before its address used to lose the service outright.
	srvFirst := announcement(t,
		ptrRecord(t, "_netaudio-cmc._udp.local.", "mixer._netaudio-cmc._udp.local."),
		srvRecord(t, "mixer._netaudio-cmc._udp.local.", "mixer.local.", 8800),
	)
	addrLater := announcement(t, aRecord(t, "mixer.local.", deviceIP))

	rec := runListener(t,
		packet{wire: srvFirst, from: udpAddr(deviceIP)},
		packet{wire: addrLater, from: udpAddr(deviceIP)},
	)
	if got := rec.values(model.FieldService); len(got) != 1 || got[0] != "_netaudio-cmc._udp" {
		t.Fatalf("services = %v, want [_netaudio-cmc._udp] once the address arrived", got)
	}
	o, _ := rec.first(model.FieldService)
	if !strings.Contains(o.Method, "has since announced mixer.local as its own name") {
		t.Errorf("Method = %q, want it to say the address settled it later", o.Method)
	}
}

func TestListenerNeverCreditsAHeldSRVToARelay(t *testing.T) {
	// The order must not become a way round the relay rule: a sender that
	// turns out to be forwarding another network keeps nothing.
	srvFirst := announcement(t,
		srvRecord(t, "mixer._netaudio-cmc._udp.local.", "mixer.local.", 8800),
	)
	foreign := announcement(t, aRecord(t, "mixer.local.", "192.0.2.77"))
	rec := runListener(t,
		packet{wire: srvFirst, from: udpAddr("192.168.1.1")},
		packet{wire: foreign, from: udpAddr("192.168.1.1")},
	)
	if got := rec.values(model.FieldService); len(got) != 0 {
		t.Fatalf("services = %v, want none from a relay", got)
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

// quietConn is a socket on which nothing arrives: each read waits a little,
// like a real read deadline, so the listener's clock moves.
type quietConn struct{ listenConn }

func (c *quietConn) ReadFrom([]byte) (int, net.Addr, error) {
	time.Sleep(20 * time.Millisecond)
	return 0, nil, os.ErrDeadlineExceeded
}

func TestListenerBrowsesTwiceASecondApart(t *testing.T) {
	var mu sync.Mutex
	var sent [][]byte
	var at []time.Time
	send := func(_ context.Context, _ netif.Interface, _ *net.UDPAddr, q []byte) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, q)
		at = append(at, time.Now())
		return nil
	}
	l := NewListener(ListenerOptions{
		Listen: func(context.Context, netif.Interface, *net.UDPAddr) (net.PacketConn, error) { return &quietConn{}, nil },
		Browse: DefaultBrowse,
		Send:   send,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 1400*time.Millisecond)
	defer cancel()
	var rec recorder
	if err := l.Run(ctx, netif.Interface{Name: "en0"}, rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatalf("sent %d browse questions, want 2", len(sent))
	}
	if gap := at[1].Sub(at[0]); gap < browseAgain {
		t.Errorf("the repeat came %s after the first; RFC 6762 asks for at least a second", gap)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(sent[0]); err != nil {
		t.Fatal(err)
	}
	if msg.Header.ID != 0 || len(msg.Questions) != 4 {
		t.Fatalf("header %+v, %d questions", msg.Header, len(msg.Questions))
	}
	var names []string
	for _, q := range msg.Questions {
		if q.Type != dnsmessage.TypePTR || q.Class != dnsmessage.ClassINET {
			t.Errorf("question %+v should be a plain PTR, answered by multicast", q)
		}
		names = append(names, q.Name.String())
	}
	if got := strings.Join(names, " "); got != "_services._dns-sd._udp.local. _netaudio-cmc._udp.local. _netaudio-arc._udp.local. _ndi._tcp.local." {
		t.Errorf("questions = %s", got)
	}
	if s := rec.messages(engine.KindSent); len(s) != 2 || !strings.Contains(s[0], "browse 1 of 2") || !strings.Contains(s[1], "browse 2 of 2") {
		t.Errorf("sent events = %v", s)
	}
	if info := rec.messages(engine.KindInfo); len(info) == 0 || !strings.HasPrefix(info[0], "listening on") || !strings.Contains(info[0], "including Dante and NDI") {
		t.Errorf("the opening message should still read as listening and say it asks: %v", info)
	}
}

func TestListenerListensWhenItCannotAsk(t *testing.T) {
	calls := 0
	l := NewListener(ListenerOptions{
		Listen: func(context.Context, netif.Interface, *net.UDPAddr) (net.PacketConn, error) { return &quietConn{}, nil },
		Browse: DefaultBrowse,
		Send: func(context.Context, netif.Interface, *net.UDPAddr, []byte) error {
			calls++
			return fmt.Errorf("%w: address in use", ErrPortHeld)
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	var rec recorder
	if err := l.Run(ctx, netif.Interface{Name: "en0"}, rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("after a refusal it should stop asking: %d attempts", calls)
	}
	if info := strings.Join(rec.messages(engine.KindInfo), "\n"); !strings.Contains(info, "listening only, so services appear when something else browses") {
		t.Errorf("the refusal should be explained: %q", info)
	}
}

// browseOpts asks the questions the TUI asks, with no settle wait, so a
// test sees what a scan would credit.
func browseOpts() ListenerOptions {
	return ListenerOptions{Browse: DefaultBrowse, Settle: time.Nanosecond,
		Send:     func(context.Context, netif.Interface, *net.UDPAddr, []byte) error { return nil },
		AskLocal: func(context.Context, []string, time.Duration) ([][]byte, error) { return nil, nil }}
}

func TestListenerCreditsADanteDeviceThatAnswersTheBrowse(t *testing.T) {
	// A Dante device answering the browse shoal sent for _netaudio-arc._udp.
	// Embedded responders often answer with the PTR alone: no SRV naming
	// the serving host, no address record. shoal asked this exact question,
	// and an answer to it is the device saying "I offer this".
	wire := announcement(t, ptrRecord(t, "_netaudio-arc._udp.local.", "stage-box._netaudio-arc._udp.local."))
	rec := runListenerWith(t, browseOpts(), packet{wire: wire, from: udpAddr(deviceIP)})

	got := rec.values(model.FieldService)
	if len(got) != 1 || got[0] != "_netaudio-arc._udp" {
		t.Fatalf("services = %v, want [_netaudio-arc._udp] from the answer to our own browse", got)
	}
	o, _ := rec.first(model.FieldService)
	if !strings.Contains(o.Method, "_netaudio-arc._udp") || !strings.Contains(o.Method, "browse") {
		t.Errorf("Method = %q, want it to say the device answered the browse for that type", o.Method)
	}
}

func TestListenerCreditsAnNDISourceThatAnswersTheBrowse(t *testing.T) {
	// NDI builds its source name from the machine's hostname, which on a
	// Mac ends ".LOCAL", so the instance is a single label with a dot in it.
	wire := announcement(t, ptrRecord(t, "_ndi._tcp.local.",
		"STAGE-MBP.LOCAL (Scan Converter)._ndi._tcp.local."))
	rec := runListenerWith(t, browseOpts(), packet{wire: wire, from: udpAddr(deviceIP)})

	if got := rec.values(model.FieldService); len(got) != 1 || got[0] != "_ndi._tcp" {
		t.Fatalf("services = %v, want [_ndi._tcp]", got)
	}
}

func TestListenerStillWaitsForAnSRVForATypeNobodyAskedAbout(t *testing.T) {
	// The rule that instance PTRs prove nothing stands for every type shoal
	// did not ask for: a phone really does announce a laptop's instance.
	wire := announcement(t, ptrRecord(t, "_companion-link._tcp.local.", "laptop._companion-link._tcp.local."))
	rec := runListenerWith(t, browseOpts(), packet{wire: wire, from: udpAddr(deviceIP)})

	if got := rec.values(model.FieldService); len(got) != 0 {
		t.Fatalf("services = %v, want none: nobody asked about that type", got)
	}
	if info := strings.Join(rec.messages(engine.KindInfo), " "); !strings.Contains(info, "waiting for an SRV") {
		t.Errorf("info = %q, want the instance noted but not credited", info)
	}
}

func TestListenerCreditsNoBrowseAnswerToARelay(t *testing.T) {
	// Answering a browse is no better evidence than listing a type: a
	// repeater answers for the devices behind it, and must not be credited.
	foreign := announcement(t, aRecord(t, "faraway.local.", "192.0.2.77"))
	answer := announcement(t, ptrRecord(t, "_ndi._tcp.local.", "camera._ndi._tcp.local."))
	rec := runListenerWith(t, browseOpts(),
		packet{wire: foreign, from: udpAddr("192.168.1.1")},
		packet{wire: answer, from: udpAddr("192.168.1.1")},
	)
	if got := rec.values(model.FieldService); len(got) != 0 {
		t.Fatalf("services = %v, want none credited to a relay", got)
	}
}

func TestListenerDoesNotMistakeAMultiHomedDeviceForARelay(t *testing.T) {
	// A device with a second interface — a Dante secondary, a VPN, a
	// management port — announces an address of its own on another network.
	// That is not a repeater carrying somebody else's records, and marking
	// it one used to erase every service it offered.
	wire := announcement(t,
		aRecord(t, "stage-box.local.", deviceIP),
		aRecord(t, "stage-box.local.", "172.16.10.9"),
		ptrRecord(t, MetaQuery+".", "_netaudio-arc._udp.local."),
	)
	rec := runListenerWith(t, browseOpts(), packet{wire: wire, from: udpAddr(deviceIP)})

	if got := rec.values(model.FieldService); len(got) != 1 || got[0] != "_netaudio-arc._udp" {
		t.Fatalf("services = %v, want the device's own list kept", got)
	}
	if info := strings.Join(rec.messages(engine.KindInfo), " "); strings.Contains(info, "is relaying another network's mDNS") {
		t.Errorf("a device naming its own second address is not a relay: %q", info)
	}
}

func TestListenerLeavesItsOwnHostOnLoopbackAlone(t *testing.T) {
	// The system responder announces on the loopback interface too. Those
	// datagrams are this machine talking to itself: writing them down makes
	// a device called 127.0.0.1 that no scan can explain. This machine is
	// asked directly instead, and credited to the scanning address.
	wire := announcement(t, aRecord(t, "self.local.", "127.0.0.1"))
	rec := runListenerWith(t, browseOpts(), packet{wire: wire, from: udpAddr("127.0.0.1")})

	if got := rec.values(model.FieldIP); len(got) != 0 {
		t.Fatalf("ip = %v, want nothing: loopback is not a device on the network", got)
	}
}

func TestListenerDoesNotCallADanteFlowRecordARelay(t *testing.T) {
	// Dante registers a record per multicast flow, named for the reversed
	// flow address, whose address is the transmitter on the Dante network.
	// That is a device saying where its audio comes from, not a repeater
	// carrying another network's hosts, and reading it as one cost the
	// device every service it offered.
	// Owner: the reversed flow address 239.255.0.10. Value: the transmitter,
	// on the Dante network rather than the one being scanned.
	wire := announcement(t,
		aRecord(t, "10.0.255.239.in-addr.local.", "172.16.10.9"),
		ptrRecord(t, MetaQuery+".", "_netaudio-arc._udp.local."),
	)
	rec := runListenerWith(t, browseOpts(), packet{wire: wire, from: udpAddr(deviceIP)})

	if got := rec.values(model.FieldService); len(got) != 1 || got[0] != "_netaudio-arc._udp" {
		t.Fatalf("services = %v, want the Dante type kept", got)
	}
	if info := strings.Join(rec.messages(engine.KindInfo), " "); strings.Contains(info, "is relaying another network's mDNS") {
		t.Errorf("a flow record is not another host's address record: %q", info)
	}
}

func TestListenerStillCatchesARepeaterCarryingAnotherHost(t *testing.T) {
	// The case the rule exists for, and the shape a real reflector has: a
	// router answering with a host on a VLAN the scan cannot reach.
	wire := announcement(t,
		aRecord(t, "office-printer.local.", "172.16.10.13"),
		ptrRecord(t, MetaQuery+".", "_netaudio-arc._udp.local."),
	)
	rec := runListenerWith(t, browseOpts(), packet{wire: wire, from: udpAddr("192.168.1.1")})

	if got := rec.values(model.FieldService); len(got) != 0 {
		t.Fatalf("services = %v, want none credited to a repeater", got)
	}
	if info := strings.Join(rec.messages(engine.KindInfo), " "); !strings.Contains(info, "relaying another network's mDNS") {
		t.Errorf("info = %q, want the repeater named", info)
	}
}

func TestListenMulticastBindsTheGroupNotEveryAddress(t *testing.T) {
	// Go substitutes the wildcard address for a multicast one, which puts
	// the listener in the system responder's port-sharing group and lets it
	// take unicast mDNS meant for that responder — including shoal's own
	// question to it, so the machine shoal runs on reported no services.
	group := &net.UDPAddr{IP: net.ParseIP(Group), Port: Port}
	conn, err := bindGroup(group)
	if err != nil {
		t.Skipf("cannot bind %s here: %v", group, err)
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || !addr.IP.Equal(group.IP) {
		t.Errorf("bound to %s, want %s: only multicast may arrive on this socket", conn.LocalAddr(), group)
	}
}

func TestListenerAccusesNobodyOfRelayingWithNoSubnetKnown(t *testing.T) {
	// Without an interface there is nothing to judge an address against.
	// Calling every address foreign meant a run that could not name the
	// interface credited no services at all.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wire := announcement(t,
		aRecord(t, "neighbour.local.", "172.16.10.9"),
		ptrRecord(t, MetaQuery+".", "_ndi._tcp.local."),
	)
	conn := &listenConn{queue: []packet{{wire: wire, from: udpAddr(deviceIP)}}, onDrain: cancel}
	opts := browseOpts()
	opts.Listen = func(context.Context, netif.Interface, *net.UDPAddr) (net.PacketConn, error) { return conn, nil }
	var rec recorder
	if err := NewListener(opts).Run(ctx, netif.Interface{Name: "en0"}, rec.emit, rec.report); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if got := rec.values(model.FieldService); len(got) != 1 || got[0] != "_ndi._tcp" {
		t.Fatalf("services = %v, want [_ndi._tcp] credited with no subnet to judge against", got)
	}
}
