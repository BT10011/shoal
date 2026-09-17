package oui

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

const fixture = `# test registry, fetched 2026-09-17
001132,Synology Incorporated
3C22FB,"Apple, Inc."
b827eb,Raspberry Pi Foundation
`

func mustMAC(t *testing.T, s string) net.HardwareAddr {
	t.Helper()
	mac, err := net.ParseMAC(s)
	if err != nil {
		t.Fatal(err)
	}
	return mac
}

func TestParseFixture(t *testing.T) {
	reg, err := Parse(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if reg.Version != "test registry, fetched 2026-09-17" {
		t.Fatalf("Version = %q", reg.Version)
	}
	if reg.Len() != 3 {
		t.Fatalf("Len = %d", reg.Len())
	}
	cases := map[string]string{
		"00:11:32:7f:a2:c4": "Synology Incorporated",
		"3c:22:fb:9e:01:77": "Apple, Inc.",
		"B8:27:EB:00:00:01": "Raspberry Pi Foundation",
	}
	for mac, want := range cases {
		if got, ok := reg.Lookup(mustMAC(t, mac)); !ok || got != want {
			t.Errorf("%s: got %q ok=%v, want %q", mac, got, ok, want)
		}
	}
	if _, ok := reg.Lookup(mustMAC(t, "00:00:00:00:00:00")); ok {
		t.Error("unknown prefix reported found")
	}
	if _, ok := reg.Lookup(net.HardwareAddr{0x00}); ok {
		t.Error("short address reported found")
	}
}

func TestParseWithoutHeaderAndBadRows(t *testing.T) {
	reg, err := Parse(strings.NewReader("001132,Synology\n"))
	if err != nil || reg.Version != "" || reg.Len() != 1 {
		t.Fatalf("reg=%+v err=%v", reg, err)
	}
	if _, err := Parse(strings.NewReader("00113,Short\n")); err == nil {
		t.Fatal("expected error for a 5-digit prefix")
	}
	if _, err := Parse(strings.NewReader("00113G,Bad hex\n")); err == nil {
		t.Fatal("expected error for non-hex prefix")
	}
}

func TestEmbeddedRegistry(t *testing.T) {
	reg, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if reg.Len() < 30000 {
		t.Fatalf("embedded registry has only %d entries", reg.Len())
	}
	if !strings.Contains(reg.Version, "IEEE MA-L") || !strings.Contains(reg.Version, "fetched") {
		t.Fatalf("Version = %q", reg.Version)
	}
	for mac, want := range map[string]string{
		"00:11:32:7f:a2:c4": "Synology Incorporated",
		"b8:27:eb:6d:2f:90": "Raspberry Pi Foundation",
		"84:cc:a8:01:02:03": "Espressif Inc.",
	} {
		if got, _ := reg.Lookup(mustMAC(t, mac)); got != want {
			t.Errorf("%s: got %q, want %q", mac, got, want)
		}
	}
}

func TestAddressBits(t *testing.T) {
	if !LocallyAdministered(mustMAC(t, "da:3b:91:0c:44:e2")) {
		t.Error("da:… should be locally administered")
	}
	if LocallyAdministered(mustMAC(t, "00:11:32:7f:a2:c4")) {
		t.Error("00:11:32 should be universally administered")
	}
	if !Multicast(mustMAC(t, "01:00:5e:00:00:01")) || Multicast(mustMAC(t, "00:11:32:7f:a2:c4")) {
		t.Error("multicast bit wrong")
	}
	if got := Prefix(mustMAC(t, "b8:27:eb:6d:2f:90")); got != "B8:27:EB" {
		t.Errorf("Prefix = %q", got)
	}
}

type capture struct {
	obs    []model.Observation
	events []engine.ProbeEvent
}

func (c *capture) emit(o model.Observation)   { c.obs = append(c.obs, o) }
func (c *capture) report(e engine.ProbeEvent) { c.events = append(c.events, e) }

func snapshot(key, mac string) model.DeviceSnapshot {
	d := model.NewDevice(key, time.Now())
	if mac != "" {
		d.Add(model.Observation{DeviceKey: key, Field: model.FieldMAC, Value: mac, Source: "arp", Method: "m", Confidence: 1, At: time.Now()})
	}
	return d.Snapshot()
}

func TestEnrichEmitsVendorWithProvenance(t *testing.T) {
	reg, _ := Parse(strings.NewReader(fixture))
	en := New(reg)
	if en.Name() != "oui" || en.Triggers()[0] != model.FieldMAC || en.Concurrency() < 1 {
		t.Fatal("enricher metadata wrong")
	}
	c := &capture{}
	if err := en.Enrich(context.Background(), snapshot("00:11:32:7f:a2:c4", "00:11:32:7f:a2:c4"), c.emit, c.report); err != nil {
		t.Fatal(err)
	}
	if len(c.obs) != 1 {
		t.Fatalf("observations = %+v", c.obs)
	}
	o := c.obs[0]
	if o.Field != model.FieldVendor || o.Value != "Synology Incorporated" || o.Confidence != 0.9 {
		t.Fatalf("vendor observation = %+v", o)
	}
	for _, want := range []string{"IEEE MA-L", "00:11:32", "Synology Incorporated", "fetched 2026-09-17"} {
		if !strings.Contains(o.Method, want) {
			t.Errorf("Method %q missing %q", o.Method, want)
		}
	}
	if len(c.events) != 2 || !strings.Contains(c.events[0].Message, "lookup 00:11:32") || !strings.Contains(c.events[1].Message, "→ Synology") {
		t.Fatalf("events = %+v", c.events)
	}
}

func TestEnrichFlagsLocallyAdministered(t *testing.T) {
	reg, _ := Parse(strings.NewReader(fixture))
	c := &capture{}
	if err := New(reg).Enrich(context.Background(), snapshot("da:3b:91:0c:44:e2", ""), c.emit, c.report); err != nil {
		t.Fatal(err)
	}
	if len(c.obs) != 1 || c.obs[0].Field != model.FieldFlag || c.obs[0].Value != FlagLocallyAdministered {
		t.Fatalf("observations = %+v", c.obs)
	}
	if !strings.Contains(c.obs[0].Method, "0x02") || !strings.Contains(c.obs[0].Method, "software assigned") {
		t.Fatalf("Method should explain the bit: %q", c.obs[0].Method)
	}
	if len(c.events) != 1 || !strings.Contains(c.events[0].Message, "locally-administered") {
		t.Fatalf("events = %+v", c.events)
	}
}

func TestEnrichUnknownPrefixEmitsNothing(t *testing.T) {
	reg, _ := Parse(strings.NewReader(fixture))
	c := &capture{}
	if err := New(reg).Enrich(context.Background(), snapshot("00:00:00:aa:bb:cc", ""), c.emit, c.report); err != nil {
		t.Fatal(err)
	}
	if len(c.obs) != 0 {
		t.Fatalf("unexpected observations %+v", c.obs)
	}
	if len(c.events) != 2 || !strings.Contains(c.events[1].Message, "not in the registry") {
		t.Fatalf("events = %+v", c.events)
	}
}

func TestEnrichRejectsUnparseableKey(t *testing.T) {
	reg, _ := Parse(strings.NewReader(fixture))
	c := &capture{}
	if err := New(reg).Enrich(context.Background(), snapshot("10.0.0.1", ""), c.emit, c.report); err == nil {
		t.Fatal("expected error for an IP-keyed device with no MAC")
	}
}
