// Package oui turns MAC addresses into vendor names using an embedded copy
// of the IEEE MA-L registry, and flags addresses the registry cannot
// explain because software assigned them.
package oui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/BT10011/shoal/data"
	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

// Registry maps 24-bit OUI prefixes to organisation names.
type Registry struct {
	Version string
	entries map[uint32]string
}

// Parse reads "prefix,organisation" rows. A leading "# ..." line, if
// present, becomes the registry's Version.
func Parse(r io.Reader) (*Registry, error) {
	br := bufio.NewReader(r)
	reg := &Registry{entries: make(map[uint32]string)}
	if peek, err := br.Peek(1); err == nil && peek[0] == '#' {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		reg.Version = strings.TrimSpace(strings.TrimPrefix(line, "#"))
	}
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = 2
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		key, ok := prefixKey(rec[0])
		if !ok {
			return nil, fmt.Errorf("bad prefix %q", rec[0])
		}
		reg.entries[key] = rec[1]
	}
	return reg, nil
}

func prefixKey(hex6 string) (uint32, bool) {
	if len(hex6) != 6 {
		return 0, false
	}
	var key uint32
	for _, c := range strings.ToUpper(hex6) {
		var v uint32
		switch {
		case c >= '0' && c <= '9':
			v = uint32(c - '0')
		case c >= 'A' && c <= 'F':
			v = uint32(c-'A') + 10
		default:
			return 0, false
		}
		key = key<<4 | v
	}
	return key, true
}

// Len returns the number of assignments.
func (r *Registry) Len() int { return len(r.entries) }

// Lookup returns the organisation assigned the address's 24-bit prefix.
func (r *Registry) Lookup(mac net.HardwareAddr) (string, bool) {
	if len(mac) < 3 {
		return "", false
	}
	org, ok := r.entries[uint32(mac[0])<<16|uint32(mac[1])<<8|uint32(mac[2])]
	return org, ok
}

var embedded = sync.OnceValues(func() (*Registry, error) {
	return Parse(bytes.NewReader(data.OUI))
})

// Embedded returns the registry compiled into the binary.
func Embedded() (*Registry, error) { return embedded() }

// Prefix formats the first three octets as "00:11:32".
func Prefix(mac net.HardwareAddr) string {
	if len(mac) < 3 {
		return mac.String()
	}
	return strings.ToUpper(fmt.Sprintf("%02x:%02x:%02x", mac[0], mac[1], mac[2]))
}

// LocallyAdministered reports whether the U/L bit (0x02 of the first
// octet) is set, meaning software chose the address and no vendor owns it.
func LocallyAdministered(mac net.HardwareAddr) bool {
	return len(mac) > 0 && mac[0]&0x02 != 0
}

// Multicast reports whether the I/G bit (0x01 of the first octet) is set.
func Multicast(mac net.HardwareAddr) bool {
	return len(mac) > 0 && mac[0]&0x01 != 0
}

// FlagLocallyAdministered is the flag value emitted for software-assigned MACs.
const FlagLocallyAdministered = "locally-administered-mac"

// Enricher resolves a device's MAC to a vendor.
type Enricher struct {
	reg *Registry
}

// New creates an enricher over a registry.
func New(reg *Registry) *Enricher { return &Enricher{reg: reg} }

func (e *Enricher) Name() string            { return "oui" }
func (e *Enricher) Triggers() []model.Field { return []model.Field{model.FieldMAC} }
func (e *Enricher) Produces() model.Field   { return model.FieldVendor }
func (e *Enricher) Concurrency() int        { return 2 }

// Enrich looks up the device's MAC. It emits a vendor, or the
// locally-administered flag, and reports what it checked either way.
func (e *Enricher) Enrich(_ context.Context, d model.DeviceSnapshot, emit engine.Emit, report engine.Report) error {
	raw := d.Key
	if o, ok := d.ResolvedAt(model.FieldMAC, time.Now()); ok {
		raw = o.Value
	}
	mac, err := net.ParseMAC(raw)
	if err != nil {
		return fmt.Errorf("device %s has no parseable MAC (%q)", d.Key, raw)
	}
	prefix := Prefix(mac)

	if LocallyAdministered(mac) {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: d.Key,
			Message: fmt.Sprintf("%s has the locally-administered bit set; skipping registry lookup", prefix)})
		emit(model.Observation{DeviceKey: d.Key, Field: model.FieldFlag, Value: FlagLocallyAdministered, Confidence: 1,
			Method: fmt.Sprintf("first octet %02x has the U/L bit (0x02) set, so software assigned this address (e.g. a phone's private Wi-Fi address) and no IEEE vendor owns it", mac[0])})
		return nil
	}

	report(engine.ProbeEvent{Kind: engine.KindInfo, Target: d.Key,
		Message: fmt.Sprintf("lookup %s in embedded IEEE MA-L registry (%d assignments)", prefix, e.reg.Len())})
	org, ok := e.reg.Lookup(mac)
	if !ok {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: d.Key, Message: prefix + " is not in the registry"})
		return nil
	}
	report(engine.ProbeEvent{Kind: engine.KindInfo, Target: d.Key, Message: prefix + " → " + org})
	emit(model.Observation{DeviceKey: d.Key, Field: model.FieldVendor, Value: org, Confidence: 0.9,
		Method: fmt.Sprintf("IEEE MA-L registry assigns prefix %s to %s (embedded copy: %s)", prefix, org, e.reg.Version)})
	return nil
}
