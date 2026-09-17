package rdns

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

// loadHex reads a hand-written packet fixture: hex bytes, "#" comments and
// whitespace for readability.
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
	deviceIP   = "192.168.1.20"
	deviceName = "20.1.168.192.in-addr.arpa."
	queryID    = 0x1a2b
)

func bytesEqual(a, b []byte) bool {
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

func TestParseReplyFixture(t *testing.T) {
	reply, err := ParseReply(loadHex(t, "reply.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if reply.ID != queryID {
		t.Errorf("ID = 0x%04x, want 0x%04x", reply.ID, queryID)
	}
	if reply.RCode != dnsmessage.RCodeSuccess {
		t.Errorf("RCode = %s, want NOERROR", reply.RCode)
	}
	if reply.Question != deviceName {
		t.Errorf("Question = %q, want %q", reply.Question, deviceName)
	}
	if len(reply.Records) != 1 {
		t.Fatalf("Records = %v, want one PTR", reply.Records)
	}
	if got := reply.Records[0].Name; got != "synology.lan" {
		t.Errorf("Records[0].Name = %q, want %q", got, "synology.lan")
	}
	if got := reply.Records[0].TTL; got != time.Hour {
		t.Errorf("Records[0].TTL = %s, want 1h", got)
	}
	if got := reply.Status(); got != "NOERROR" {
		t.Errorf("Status = %q, want NOERROR", got)
	}
	if got := reply.Summary(); !strings.Contains(got, "NOERROR") || !strings.Contains(got, "synology.lan") {
		t.Errorf("Summary = %q, want it to name the rcode and the answer", got)
	}
}

func TestParseNXDomainFixture(t *testing.T) {
	reply, err := ParseReply(loadHex(t, "nxdomain.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if got := reply.Status(); got != "NXDOMAIN" {
		t.Errorf("Status = %q, want NXDOMAIN", got)
	}
	if reply.RCode != dnsmessage.RCodeNameError {
		t.Errorf("RCode = %v, want NXDOMAIN", reply.RCode)
	}
	if len(reply.Records) != 0 {
		t.Errorf("Records = %v, want none", reply.Records)
	}
	if got := reply.Summary(); !strings.Contains(got, "no PTR answer") {
		t.Errorf("Summary = %q, want it to say there was no answer", got)
	}
}

func TestParseReplyRejectsGarbage(t *testing.T) {
	if _, err := ParseReply([]byte{0x1a, 0x2b, 0x81}); err == nil {
		t.Error("ParseReply(short) = nil error, want a decode error")
	}
}

func TestParseResolvConfFixture(t *testing.T) {
	f, err := os.Open("testdata/resolv.conf")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := ParseResolvConf(f)
	want := []string{"192.168.1.1:53", "1.1.1.1:53", "192.168.1.1:53"}
	if len(got) != len(want) {
		t.Fatalf("ParseResolvConf = %v, want %d resolvers", got, len(want))
	}
	for i, addr := range want {
		if got[i].Addr != addr {
			t.Errorf("resolver %d = %q, want %q", i, got[i].Addr, addr)
		}
		if !strings.Contains(got[i].Origin, ResolvConf) {
			t.Errorf("resolver %d origin = %q, want it to name %s", i, got[i].Origin, ResolvConf)
		}
	}
	if origin := got[1].Origin; !strings.Contains(origin, "line 5") {
		t.Errorf("second resolver origin = %q, want the line it came from", origin)
	}
	if n := len(dedupe(got)); n != 2 {
		t.Errorf("dedupe kept %d resolvers, want 2", n)
	}
}

func TestSystemResolversFallsBackToGateway(t *testing.T) {
	got := SystemResolvers(net.ParseIP("192.168.1.1"))
	if len(got) == 0 {
		t.Fatal("SystemResolvers = none, want at least the gateway")
	}
	// The host running the tests usually has a resolv.conf; the gateway is
	// only used when it names nothing.
	if _, err := os.Stat(ResolvConf); errors.Is(err, os.ErrNotExist) {
		if got[0].Addr != "192.168.1.1:53" {
			t.Errorf("resolver = %q, want the gateway", got[0].Addr)
		}
		if !strings.Contains(got[0].Origin, "gateway") {
			t.Errorf("origin = %q, want it to explain the gateway fallback", got[0].Origin)
		}
	}
}

// recorder collects what an enricher emits and reports.
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

func fixedID() uint16 { return queryID }

func TestEnrichEmitsHostnameFromPTR(t *testing.T) {
	reply := loadHex(t, "reply.hex")
	var asked []string
	e := New(Options{
		Resolvers: []Resolver{{Addr: "192.168.1.1:53", Origin: "nameserver on line 1 of /etc/resolv.conf"}},
		NewID:     fixedID,
		Exchange: func(_ context.Context, r Resolver, query []byte) ([]byte, time.Duration, error) {
			asked = append(asked, r.Addr)
			if want := loadHex(t, "query.hex"); !bytesEqual(query, want) {
				t.Errorf("query =\n%s\nwant\n%s", hex.Dump(query), hex.Dump(want))
			}
			return reply, 3200 * time.Microsecond, nil
		},
	})

	var rec recorder
	if err := e.Enrich(context.Background(), snapshot("00:11:32:7f:a2:c4", deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 {
		t.Fatalf("asked %v, want exactly one resolver", asked)
	}
	if len(rec.obs) != 1 {
		t.Fatalf("emitted %d observations, want 1: %+v", len(rec.obs), rec.obs)
	}
	o := rec.obs[0]
	if o.Field != model.FieldHostname || o.Value != "synology.lan" {
		t.Errorf("observation = %s %q, want hostname synology.lan", o.Field, o.Value)
	}
	if o.DeviceKey != "00:11:32:7f:a2:c4" {
		t.Errorf("DeviceKey = %q, want the device's key, not its IP", o.DeviceKey)
	}
	if o.Confidence != Confidence {
		t.Errorf("Confidence = %v, want %v", o.Confidence, Confidence)
	}
	if o.TTL != time.Hour {
		t.Errorf("TTL = %s, want the record's 1h", o.TTL)
	}
	if !bytesEqual(o.Raw, reply) {
		t.Error("Raw does not hold the reply bytes")
	}
	for _, want := range []string{deviceName, "192.168.1.1:53", "/etc/resolv.conf", "3.2ms", "1h0m0s"} {
		if !strings.Contains(o.Method, want) {
			t.Errorf("Method = %q, want it to mention %q", o.Method, want)
		}
	}
	if sent := rec.messages(engine.KindSent); len(sent) != 1 || !strings.Contains(sent[0], "PTR?") {
		t.Errorf("sent events = %v, want one naming the question", sent)
	}
	if got := rec.messages(engine.KindReceived); len(got) != 1 || !strings.Contains(got[0], "synology.lan") {
		t.Errorf("received events = %v, want one naming the answer", got)
	}
}

func TestEnrichClampsShortTTL(t *testing.T) {
	reply := loadHex(t, "reply-ttl0.hex")
	e := New(Options{
		Resolvers: []Resolver{{Addr: "192.168.1.1:53", Origin: "test"}},
		NewID:     fixedID,
		Exchange: func(context.Context, Resolver, []byte) ([]byte, time.Duration, error) {
			return reply, time.Millisecond, nil
		},
	})
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot("k", deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(rec.obs) != 1 {
		t.Fatalf("emitted %d observations, want 1", len(rec.obs))
	}
	if rec.obs[0].TTL != minTTL {
		t.Errorf("TTL = %s, want it clamped to %s", rec.obs[0].TTL, minTTL)
	}
	if !strings.Contains(rec.obs[0].Method, "DNS TTL 0s") {
		t.Errorf("Method = %q, want it to report the true TTL", rec.obs[0].Method)
	}
}

func TestEnrichTriesNextResolverWhenOneIsSilent(t *testing.T) {
	e := New(Options{
		Resolvers: []Resolver{
			{Addr: "10.0.0.53:53", Origin: "first"},
			{Addr: "192.168.1.1:53", Origin: "second"},
		},
		NewID: fixedID,
		Exchange: func(_ context.Context, r Resolver, _ []byte) ([]byte, time.Duration, error) {
			if r.Addr == "10.0.0.53:53" {
				return nil, 2 * time.Second, errors.New("i/o timeout")
			}
			return loadHex(t, "reply.hex"), time.Millisecond, nil
		},
	})
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot("k", deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(rec.obs) != 1 {
		t.Fatalf("emitted %d observations, want 1", len(rec.obs))
	}
	if !strings.Contains(rec.obs[0].Method, "192.168.1.1:53") {
		t.Errorf("Method = %q, want the resolver that actually answered", rec.obs[0].Method)
	}
	if errs := rec.messages(engine.KindError); len(errs) != 1 || !strings.Contains(errs[0], "10.0.0.53:53") {
		t.Errorf("error events = %v, want one naming the silent resolver", errs)
	}
}

func TestEnrichStopsAtNXDomain(t *testing.T) {
	var asked int
	e := New(Options{
		Resolvers: []Resolver{{Addr: "a:53", Origin: "first"}, {Addr: "b:53", Origin: "second"}},
		NewID:     func() uint16 { return 0x3c4d },
		Exchange: func(context.Context, Resolver, []byte) ([]byte, time.Duration, error) {
			asked++
			return loadHex(t, "nxdomain.hex"), time.Millisecond, nil
		},
	})
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot("k", "192.168.1.99"), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Errorf("asked %d resolvers, want 1: NXDOMAIN is a definite answer", asked)
	}
	if len(rec.obs) != 0 {
		t.Errorf("emitted %+v, want no hostname", rec.obs)
	}
	if info := rec.messages(engine.KindInfo); len(info) != 1 || !strings.Contains(info[0], "NXDOMAIN") {
		t.Errorf("info events = %v, want one explaining NXDOMAIN", info)
	}
}

func TestEnrichDiscardsMismatchedID(t *testing.T) {
	e := New(Options{
		Resolvers: []Resolver{{Addr: "a:53", Origin: "first"}},
		NewID:     func() uint16 { return 0x9999 }, // the fixture answers 0x1a2b
		Exchange: func(context.Context, Resolver, []byte) ([]byte, time.Duration, error) {
			return loadHex(t, "reply.hex"), time.Millisecond, nil
		},
	})
	var rec recorder
	err := e.Enrich(context.Background(), snapshot("k", deviceIP), rec.emit, rec.report)
	if err == nil {
		t.Fatal("Enrich = nil error, want the mismatch reported")
	}
	if len(rec.obs) != 0 {
		t.Errorf("emitted %+v, want nothing from an unmatched reply", rec.obs)
	}
	if errs := rec.messages(engine.KindError); len(errs) != 1 || !strings.Contains(errs[0], "discarding") {
		t.Errorf("error events = %v, want one saying the answer was discarded", errs)
	}
}

func TestEnrichRejectsDeviceWithoutIP(t *testing.T) {
	e := New(Options{Resolvers: []Resolver{{Addr: "a:53"}}, Exchange: func(context.Context, Resolver, []byte) ([]byte, time.Duration, error) {
		t.Error("exchange called for a device with no IP")
		return nil, 0, nil
	}})
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot("00:11:32:7f:a2:c4", "not-an-ip"), rec.emit, rec.report); err == nil {
		t.Error("Enrich = nil error, want a parse error")
	}
}

func TestEnrichWithoutResolvers(t *testing.T) {
	e := New(Options{Resolvers: []Resolver{}})
	var rec recorder
	err := e.Enrich(context.Background(), snapshot("k", deviceIP), rec.emit, rec.report)
	if err == nil || !strings.Contains(err.Error(), ResolvConf) {
		t.Errorf("Enrich = %v, want an error naming %s", err, ResolvConf)
	}
}

func TestTriggersAndName(t *testing.T) {
	e := New(Options{Resolvers: []Resolver{{Addr: "a:53"}}})
	if e.Name() != "rdns" {
		t.Errorf("Name = %q", e.Name())
	}
	if tr := e.Triggers(); len(tr) != 1 || tr[0] != model.FieldIP {
		t.Errorf("Triggers = %v, want [ip]", tr)
	}
	if e.Concurrency() != defaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", e.Concurrency(), defaultConcurrency)
	}
}
