package av

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

type capture struct {
	obs    []model.Observation
	events []engine.ProbeEvent
}

func (c *capture) emit(o model.Observation)   { c.obs = append(c.obs, o) }
func (c *capture) report(e engine.ProbeEvent) { c.events = append(c.events, e) }

func device(facts ...model.Observation) model.DeviceSnapshot {
	d := model.NewDevice("k", time.Now())
	for _, o := range facts {
		o.DeviceKey, o.At, o.Method, o.Confidence = "k", time.Now(), "m", 0.9
		d.Add(o)
	}
	return d.Snapshot()
}

func service(v string) model.Observation {
	return model.Observation{Field: model.FieldService, Value: v, Source: "mdns"}
}

func vendor(v string) model.Observation {
	return model.Observation{Field: model.FieldVendor, Value: v, Source: "oui"}
}

func run(t *testing.T, d model.DeviceSnapshot) *capture {
	t.Helper()
	c := &capture{}
	if err := New().Enrich(context.Background(), d, c.emit, c.report); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDanteFromItsServicesAndItsModule(t *testing.T) {
	c := run(t, device(service("_netaudio-arc._udp"), service("_netaudio-cmc._udp"), service("_http._tcp"), vendor("Audinate Pty L")))
	if len(c.obs) != 1 || c.obs[0].Value != FlagDante || c.obs[0].Field != model.FieldFlag || c.obs[0].Confidence != 1 {
		t.Fatalf("observations = %+v", c.obs)
	}
	m := c.obs[0].Method
	for _, want := range []string{"a Dante audio-over-IP device", "announces _netaudio-arc._udp over mDNS (learned by mdns)", "_netaudio-cmc._udp", "made by Audinate Pty L, which makes only Dante modules"} {
		if !strings.Contains(m, want) {
			t.Errorf("method missing %q: %q", want, m)
		}
	}
	if strings.Contains(m, "_http._tcp") {
		t.Errorf("unrelated services are not evidence: %q", m)
	}
	if len(c.events) != 1 || c.events[0].Target != "k" {
		t.Errorf("events = %+v", c.events)
	}
}

func TestAudinateModuleAloneIsEnough(t *testing.T) {
	if c := run(t, device(vendor("Audinate Pty L"))); len(c.obs) != 1 || c.obs[0].Value != FlagDante {
		t.Fatalf("an Audinate interface is a Dante interface: %+v", c.obs)
	}
}

func TestNDIFromItsService(t *testing.T) {
	c := run(t, device(service("_ndi._tcp"), service("_rtsp._tcp"), vendor("Sony Corporation")))
	if len(c.obs) != 1 || c.obs[0].Value != FlagNDI || !strings.Contains(c.obs[0].Method, "announces _ndi._tcp") {
		t.Fatalf("observations = %+v", c.obs)
	}
}

func TestNoGuessingFromOtherVendorsOrServices(t *testing.T) {
	for _, d := range []model.DeviceSnapshot{
		device(vendor("Shure Incorporated"), service("_http._tcp")),
		device(vendor("Sony Corporation"), service("_rtsp._tcp")),
		device(service("_netaudio"), service("_ndi._udp")),
		device(),
	} {
		if c := run(t, d); len(c.obs) != 0 {
			t.Errorf("flagged without self-declared evidence: %+v", c.obs)
		}
	}
}

func TestShape(t *testing.T) {
	e := New()
	if e.Name() != "av" || e.Produces() != model.FieldFlag || e.Concurrency() != 1 {
		t.Fatal("shape")
	}
	if tr := e.Triggers(); len(tr) != 2 || tr[0] != model.FieldService || tr[1] != model.FieldVendor {
		t.Fatalf("triggers = %v", tr)
	}
}
