// Package av points out Dante and NDI devices. It sends nothing: it reads
// what a device has said about itself, the service types it announces over
// mDNS, and names the device for what it offers, with that evidence in the
// method. It does not guess from a vendor, with one exception that is not
// a guess: Audinate makes only Dante modules, so an Audinate network
// interface is a Dante interface.
package av

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

// Flags this probe raises, and so the badges the table shows.
const (
	FlagDante = "dante-device"
	FlagNDI   = "ndi-device"
)

// DantePrefix begins every Dante service type: _netaudio-arc._udp for
// audio routing, _netaudio-cmc._udp for control and monitoring, and more.
const DantePrefix = "_netaudio-"

// dantePrefixes are the beginnings of every service type Dante registers.
// Besides the _netaudio- family, a device announces _dante-safe._udp when
// it has fallen back to safe mode and _dante-upgr._udp while it is being
// upgraded — states a device is very much still a Dante device in, and the
// ones an engineer is most likely to be hunting for.
var dantePrefixes = []string{DantePrefix, "_dante-"}

// isDante reports whether a service type is one Dante registers.
func isDante(service string) bool {
	for _, p := range dantePrefixes {
		if strings.HasPrefix(service, p) {
			return true
		}
	}
	return false
}

// NDIService is the type NDI devices announce so they can be found.
const NDIService = "_ndi._tcp"

// Enricher reads a device's services and vendor.
type Enricher struct{}

// New creates the probe.
func New() *Enricher { return &Enricher{} }

func (e *Enricher) Name() string { return "av" }
func (e *Enricher) Triggers() []model.Field {
	return []model.Field{model.FieldService, model.FieldVendor}
}
func (e *Enricher) Produces() model.Field { return model.FieldFlag }
func (e *Enricher) Concurrency() int      { return 1 }

// Enrich flags a device when what it has said about itself shows it is a
// Dante or NDI device.
func (e *Enricher) Enrich(_ context.Context, d model.DeviceSnapshot, emit engine.Emit, report engine.Report) error {
	now := time.Now()
	var dante, ndi []string
	for _, o := range d.Live(model.FieldService, now) {
		switch {
		case isDante(o.Value):
			dante = append(dante, fmt.Sprintf("announces %s over mDNS (learned by %s)", o.Value, o.Source))
		case o.Value == NDIService:
			ndi = append(ndi, fmt.Sprintf("announces %s over mDNS (learned by %s)", o.Value, o.Source))
		}
	}
	if v, ok := d.ResolvedAt(model.FieldVendor, now); ok && strings.Contains(strings.ToLower(v.Value), "audinate") {
		dante = append(dante, fmt.Sprintf("its network interface is made by %s, which makes only Dante modules (learned by %s)", v.Value, v.Source))
	}
	if len(dante) > 0 {
		e.flag(d.Key, FlagDante, "a Dante audio-over-IP device", dante, emit, report)
	}
	if len(ndi) > 0 {
		e.flag(d.Key, FlagNDI, "an NDI video-over-IP device", ndi, emit, report)
	}
	return nil
}

func (e *Enricher) flag(key, flag, what string, evidence []string, emit engine.Emit, report engine.Report) {
	method := fmt.Sprintf("%s: it %s", what, strings.Join(evidence, "; and "))
	emit(model.Observation{DeviceKey: key, Field: model.FieldFlag, Value: flag, Confidence: 1, Method: method})
	report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key, Message: fmt.Sprintf("%s is %s", key, method)})
}
