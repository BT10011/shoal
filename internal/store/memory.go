// Package store holds the authoritative device state. It is the only place
// that mutates devices: probes hand it observations, it applies them, detects
// conflicts and publishes events describing what changed.
package store

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/BT10011/shoal/internal/model"
)

// EventKind classifies a store event.
type EventKind string

const (
	EventDeviceAdded   EventKind = "device_added"
	EventDeviceUpdated EventKind = "device_updated"
	EventConflict      EventKind = "conflict"
)

// Event describes a change to the store. Device is a snapshot taken at the
// moment of the change, so consumers never need to lock.
type Event struct {
	Kind   EventKind
	Key    string
	Field  model.Field
	Device model.DeviceSnapshot
	At     time.Time
}

// Listener receives events. It is called outside the store's lock and may
// call back into the store, but it must not block for long.
type Listener func(Event)

// Memory is an in-memory store. It is safe for concurrent use, though the
// engine is expected to apply observations from a single goroutine.
type Memory struct {
	mu       sync.RWMutex
	devices  map[string]*model.Device
	ipOwners map[string]map[string]struct{}
	listener Listener
	now      func() time.Time
}

// Option configures a Memory store.
type Option func(*Memory)

// WithClock replaces the time source, for tests.
func WithClock(now func() time.Time) Option {
	return func(m *Memory) { m.now = now }
}

// WithListener sets the event listener.
func WithListener(l Listener) Option {
	return func(m *Memory) { m.listener = l }
}

// NewMemory creates an empty store.
func NewMemory(opts ...Option) *Memory {
	m := &Memory{
		devices:  make(map[string]*model.Device),
		ipOwners: make(map[string]map[string]struct{}),
		listener: func(Event) {},
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func validate(o model.Observation) error {
	switch {
	case o.DeviceKey == "":
		return errors.New("observation has no device key")
	case o.Field == "":
		return errors.New("observation has no field")
	case o.Value == "":
		return errors.New("observation has no value")
	case o.Source == "":
		return errors.New("observation has no source")
	case o.Method == "":
		return errors.New("observation has no method")
	case o.Confidence < 0 || o.Confidence > 1:
		return fmt.Errorf("observation confidence %v outside 0..1", o.Confidence)
	}
	return nil
}

// Apply merges an observation into the store and publishes the resulting
// events. Observations without provenance are rejected.
func (m *Memory) Apply(o model.Observation) error {
	if err := validate(o); err != nil {
		return err
	}
	if o.At.IsZero() {
		o.At = m.now()
	}

	m.mu.Lock()
	events := m.apply(o)
	m.mu.Unlock()

	for _, ev := range events {
		m.listener(ev)
	}
	return nil
}

func (m *Memory) apply(o model.Observation) []Event {
	var events []Event
	now := m.now()

	dev, exists := m.devices[o.DeviceKey]
	if !exists {
		dev = model.NewDevice(o.DeviceKey, o.At)
		m.devices[o.DeviceKey] = dev
	}
	changed := dev.Add(o)

	switch {
	case !exists:
		events = append(events, Event{Kind: EventDeviceAdded, Key: dev.Key, Field: o.Field, Device: dev.Snapshot(), At: now})
	default:
		events = append(events, Event{Kind: EventDeviceUpdated, Key: dev.Key, Field: o.Field, Device: dev.Snapshot(), At: now})
	}
	if changed && dev.Conflicting(o.Field, now) {
		events = append(events, Event{Kind: EventConflict, Key: dev.Key, Field: o.Field, Device: dev.Snapshot(), At: now})
	}

	if o.Field == model.FieldIP && changed {
		events = append(events, m.indexIP(o.Value, dev, now)...)
	}
	return events
}

// indexIP records that dev claims ip and flags every claimant when more than
// one device shares the address.
func (m *Memory) indexIP(ip string, dev *model.Device, now time.Time) []Event {
	owners := m.ipOwners[ip]
	if owners == nil {
		owners = make(map[string]struct{})
		m.ipOwners[ip] = owners
	}
	owners[dev.Key] = struct{}{}
	if len(owners) < 2 {
		return nil
	}

	keys := make([]string, 0, len(owners))
	for k := range owners {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var events []Event
	for _, key := range keys {
		others := make([]string, 0, len(keys)-1)
		for _, k := range keys {
			if k != key {
				others = append(others, k)
			}
		}
		claimant := m.devices[key]
		flag := model.Observation{
			DeviceKey:  key,
			Field:      model.FieldFlag,
			Value:      "duplicate-ip",
			Source:     "store",
			Method:     fmt.Sprintf("IP %s is also claimed by %v", ip, others),
			Confidence: 1,
			At:         now,
		}
		if claimant.Add(flag) {
			events = append(events, Event{Kind: EventDeviceUpdated, Key: key, Field: model.FieldFlag, Device: claimant.Snapshot(), At: now})
		}
	}
	return events
}

// Get returns a snapshot of one device.
func (m *Memory) Get(key string) (model.DeviceSnapshot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	dev, ok := m.devices[key]
	if !ok {
		return model.DeviceSnapshot{}, false
	}
	return dev.Snapshot(), true
}

// Devices returns snapshots of every device, sorted by key.
func (m *Memory) Devices() []model.DeviceSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.DeviceSnapshot, 0, len(m.devices))
	for _, dev := range m.devices {
		out = append(out, dev.Snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Len returns the number of known devices.
func (m *Memory) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.devices)
}
