// Package model holds the core data types: devices and the observations that
// describe them. It performs no I/O and never mutates state on its own; the
// store package is the only writer.
package model

import (
	"sort"
	"time"
)

// Field names a kind of fact about a device.
type Field string

const (
	FieldIP       Field = "ip"
	FieldMAC      Field = "mac"
	FieldHostname Field = "hostname"
	FieldVendor   Field = "vendor"
	FieldLatency  Field = "latency"
	FieldService  Field = "service"
	FieldPort     Field = "port"
	FieldType     Field = "device_type"
	FieldFlag     Field = "flag"
)

// MultiValued reports whether a field legitimately holds several values at
// once (services, ports, flags). Single-valued fields with more than one live
// value are in conflict.
func (f Field) MultiValued() bool {
	switch f {
	case FieldService, FieldPort, FieldFlag:
		return true
	}
	return false
}

// Observation is a single piece of evidence about a device, with provenance.
type Observation struct {
	DeviceKey  string
	Field      Field
	Value      string
	Source     string
	Method     string
	Confidence float32
	Raw        []byte
	At         time.Time
	TTL        time.Duration
}

// Expired reports whether the observation is past its TTL at now.
// A zero TTL never expires.
func (o Observation) Expired(now time.Time) bool {
	return o.TTL > 0 && !now.Before(o.At.Add(o.TTL))
}

// sourcePriority breaks ties between equally confident observations.
// Higher wins. Unknown sources rank lowest.
var sourcePriority = map[string]int{
	"arp":      100,
	"mdns":     90,
	"neigh":    80,
	"rdns":     70,
	"nbns":     60,
	"oui":      50,
	"icmp":     40,
	"ports":    30,
	"classify": 20,
	"store":    10,
}

// SourcePriority returns the tie-break rank of a source.
func SourcePriority(source string) int {
	return sourcePriority[source]
}

// better reports whether a should be displayed in preference to b:
// highest confidence, then source priority, then newest.
func better(a, b Observation) bool {
	if a.Confidence != b.Confidence {
		return a.Confidence > b.Confidence
	}
	if pa, pb := SourcePriority(a.Source), SourcePriority(b.Source); pa != pb {
		return pa > pb
	}
	return a.At.After(b.At)
}

// Device is everything known about one host, keyed by MAC for on-link hosts
// or by IP otherwise.
type Device struct {
	Key       string
	Facts     map[Field][]Observation
	FirstSeen time.Time
	LastSeen  time.Time
}

// NewDevice creates an empty device first seen at the given time.
func NewDevice(key string, at time.Time) *Device {
	return &Device{
		Key:       key,
		Facts:     make(map[Field][]Observation),
		FirstSeen: at,
		LastSeen:  at,
	}
}

// Add merges o into d and reports whether the set of values changed.
//
// For single-valued fields each source keeps only its latest opinion, so a new
// observation replaces any earlier one from the same source. For multi-valued
// fields an observation is identified by (source, value) and a repeat only
// refreshes its timestamp and metadata.
func (d *Device) Add(o Observation) (changed bool) {
	if o.At.After(d.LastSeen) {
		d.LastSeen = o.At
	}
	if d.FirstSeen.IsZero() || o.At.Before(d.FirstSeen) {
		d.FirstSeen = o.At
	}

	obs := d.Facts[o.Field]
	for i := range obs {
		if obs[i].Source != o.Source {
			continue
		}
		if o.Field.MultiValued() && obs[i].Value != o.Value {
			continue
		}
		changed = obs[i].Value != o.Value
		obs[i] = o
		return changed
	}
	d.Facts[o.Field] = append(obs, o)
	return true
}

// Live returns the unexpired observations for a field, best first.
func (d *Device) Live(f Field, now time.Time) []Observation {
	var live []Observation
	for _, o := range d.Facts[f] {
		if !o.Expired(now) {
			live = append(live, o)
		}
	}
	sort.SliceStable(live, func(i, j int) bool { return better(live[i], live[j]) })
	return live
}

// Resolved returns the display value for a field as of now.
func (d *Device) Resolved(f Field) (Observation, bool) {
	return d.ResolvedAt(f, time.Now())
}

// ResolvedAt returns the display value for a field as of the given time:
// unexpired only, then highest confidence, then source priority, then newest.
func (d *Device) ResolvedAt(f Field, now time.Time) (Observation, bool) {
	live := d.Live(f, now)
	if len(live) == 0 {
		return Observation{}, false
	}
	return live[0], true
}

// Values returns the distinct live values for a field, best first.
func (d *Device) Values(f Field, now time.Time) []string {
	seen := make(map[string]struct{})
	var values []string
	for _, o := range d.Live(f, now) {
		if _, dup := seen[o.Value]; dup {
			continue
		}
		seen[o.Value] = struct{}{}
		values = append(values, o.Value)
	}
	return values
}

// Conflicting reports whether a single-valued field has more than one distinct
// live value. Multi-valued fields never conflict.
func (d *Device) Conflicting(f Field, now time.Time) bool {
	if f.MultiValued() {
		return false
	}
	return len(d.Values(f, now)) > 1
}

// Fields lists the fields with at least one observation, sorted.
func (d *Device) Fields() []Field {
	fields := make([]Field, 0, len(d.Facts))
	for f := range d.Facts {
		fields = append(fields, f)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i] < fields[j] })
	return fields
}

// DeviceSnapshot is a deep copy of a Device that is safe to read from any
// goroutine. It is what the store hands to the UI and to enrichers.
type DeviceSnapshot struct {
	Device
}

// Snapshot returns a deep copy of the device.
func (d *Device) Snapshot() DeviceSnapshot {
	facts := make(map[Field][]Observation, len(d.Facts))
	for f, obs := range d.Facts {
		copied := make([]Observation, len(obs))
		for i, o := range obs {
			if o.Raw != nil {
				o.Raw = append([]byte(nil), o.Raw...)
			}
			copied[i] = o
		}
		facts[f] = copied
	}
	return DeviceSnapshot{Device{
		Key:       d.Key,
		Facts:     facts,
		FirstSeen: d.FirstSeen,
		LastSeen:  d.LastSeen,
	}}
}
