// Package engine runs probes. Discoverers find devices; enrichers are
// scheduled when the fields they care about appear or change. Everything a
// probe sends or receives is reported as a ProbeEvent so the UI can show
// what is happening under the hood.
package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/store"
)

// Emit hands an observation to the store.
type Emit func(model.Observation)

// Report publishes a probe event: what was sent, received, or how far along
// the probe is.
type Report func(ProbeEvent)

// Discoverer finds devices on an interface.
type Discoverer interface {
	Name() string
	Run(ctx context.Context, iface netif.Interface, emit Emit, report Report) error
}

// Enricher adds facts to a device once its trigger fields are known.
type Enricher interface {
	Name() string
	Triggers() []model.Field
	Concurrency() int
	Enrich(ctx context.Context, d model.DeviceSnapshot, emit Emit, report Report) error
}

// EventKind classifies a probe event.
type EventKind string

const (
	KindSent     EventKind = "sent"
	KindReceived EventKind = "received"
	KindProgress EventKind = "progress"
	KindError    EventKind = "error"
	KindInfo     EventKind = "info"
)

// ProbeEvent is a line in the "under the hood" log.
type ProbeEvent struct {
	Probe   string
	Kind    EventKind
	Target  string
	Message string
	Done    int
	Total   int
	At      time.Time
}

// ProbeState is the lifecycle state of a discoverer.
type ProbeState string

const (
	StateIdle      ProbeState = "idle"
	StateRunning   ProbeState = "running"
	StateDone      ProbeState = "done"
	StateFailed    ProbeState = "failed"
	StateCancelled ProbeState = "cancelled"
)

// DiscovererStatus is a snapshot of one discoverer's progress.
type DiscovererStatus struct {
	Name    string
	State   ProbeState
	Done    int
	Total   int
	Message string
	Err     string
}

// EnricherStatus is a snapshot of one enricher's queue.
type EnricherStatus struct {
	Name      string
	Running   int
	Queued    int
	Completed int
	Failed    int
}

// Status is a snapshot of the whole engine.
type Status struct {
	Discoverers []DiscovererStatus
	Enrichers   []EnricherStatus
}

// Listener receives every probe event on the goroutine that produced it.
// It must not block.
type Listener func(ProbeEvent)

// Engine owns the probes and their scheduling.
type Engine struct {
	store *store.Memory
	iface netif.Interface
	now   func() time.Time

	mu          sync.Mutex
	discoverers []*discoverer
	enrichers   []*enricher
	listeners   []Listener
	started     bool
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

// Option configures an Engine.
type Option func(*Engine)

// WithClock replaces the time source, for tests.
func WithClock(now func() time.Time) Option {
	return func(e *Engine) { e.now = now }
}

// New creates an engine over a store and subscribes to its events.
func New(st *store.Memory, iface netif.Interface, opts ...Option) *Engine {
	e := &Engine{store: st, iface: iface, now: time.Now}
	for _, opt := range opts {
		opt(e)
	}
	st.Subscribe(e.onStoreEvent)
	return e
}

// Subscribe adds a probe event listener.
func (e *Engine) Subscribe(l Listener) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.listeners = append(e.listeners, l)
}

// AddDiscoverer registers a discoverer. It must be called before Start.
func (e *Engine) AddDiscoverer(d Discoverer) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return errors.New("engine already started")
	}
	e.discoverers = append(e.discoverers, &discoverer{
		Discoverer: d,
		status:     DiscovererStatus{Name: d.Name(), State: StateIdle},
	})
	return nil
}

// AddEnricher registers an enricher. It must be called before Start.
func (e *Engine) AddEnricher(en Enricher) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return errors.New("engine already started")
	}
	workers := en.Concurrency()
	if workers < 1 {
		workers = 1
	}
	triggers := make(map[model.Field]bool, len(en.Triggers()))
	for _, f := range en.Triggers() {
		triggers[f] = true
	}
	e.enrichers = append(e.enrichers, &enricher{
		Enricher: en,
		workers:  workers,
		triggers: triggers,
		queued:   make(map[string]bool),
		wake:     make(chan struct{}, workers),
	})
	return nil
}

// Start launches every discoverer and the enricher worker pools. It returns
// immediately; Stop cancels and waits.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return errors.New("engine already started")
	}
	e.started = true
	ctx, e.cancel = context.WithCancel(ctx)

	for _, en := range e.enrichers {
		for i := 0; i < en.workers; i++ {
			e.wg.Add(1)
			go e.enrichWorker(ctx, en)
		}
	}
	for _, d := range e.discoverers {
		e.wg.Add(1)
		go e.runDiscoverer(ctx, d)
	}
	return nil
}

// Stop cancels all probes and waits for them to exit.
func (e *Engine) Stop() {
	e.mu.Lock()
	cancel := e.cancel
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	e.wg.Wait()
}

// Status returns a snapshot of every probe.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	var s Status
	for _, d := range e.discoverers {
		s.Discoverers = append(s.Discoverers, d.snapshot())
	}
	for _, en := range e.enrichers {
		s.Enrichers = append(s.Enrichers, en.snapshot())
	}
	return s
}

func (e *Engine) publish(ev ProbeEvent) {
	if ev.At.IsZero() {
		ev.At = e.now()
	}
	e.mu.Lock()
	listeners := append([]Listener(nil), e.listeners...)
	e.mu.Unlock()
	for _, l := range listeners {
		l(ev)
	}
}

func (e *Engine) emitFor(probe string) Emit {
	return func(o model.Observation) {
		if o.Source == "" {
			o.Source = probe
		}
		if o.At.IsZero() {
			o.At = e.now()
		}
		if err := e.store.Apply(o); err != nil {
			e.publish(ProbeEvent{Probe: probe, Kind: KindError, Target: o.DeviceKey, Message: "rejected observation: " + err.Error()})
		}
	}
}

func (e *Engine) onStoreEvent(ev store.Event) {
	if ev.Kind == store.EventConflict {
		values := ev.Device.Values(ev.Field, ev.At)
		e.publish(ProbeEvent{
			Probe:   "store",
			Kind:    KindInfo,
			Target:  ev.Key,
			Message: fmt.Sprintf("%s conflict: sources disagree (%s)", ev.Field, strings.Join(values, " vs ")),
			At:      ev.At,
		})
	}
	if !ev.Changed {
		return
	}
	e.mu.Lock()
	enrichers := e.enrichers
	e.mu.Unlock()
	for _, en := range enrichers {
		if en.triggers[ev.Field] {
			en.enqueue(ev.Key)
		}
	}
}

type discoverer struct {
	Discoverer
	mu     sync.Mutex
	status DiscovererStatus
}

func (d *discoverer) snapshot() DiscovererStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.status
}

func (d *discoverer) observe(ev ProbeEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch ev.Kind {
	case KindProgress:
		d.status.Done, d.status.Total = ev.Done, ev.Total
		if ev.Message != "" {
			d.status.Message = ev.Message
		}
	case KindInfo:
		d.status.Message = ev.Message
	}
}

func (d *discoverer) setState(s ProbeState, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status.State = s
	if err != nil {
		d.status.Err = err.Error()
	}
}

func (e *Engine) runDiscoverer(ctx context.Context, d *discoverer) {
	defer e.wg.Done()
	name := d.Name()
	d.setState(StateRunning, nil)
	report := func(ev ProbeEvent) {
		if ev.Probe == "" {
			ev.Probe = name
		}
		if ev.At.IsZero() {
			ev.At = e.now()
		}
		d.observe(ev)
		e.publish(ev)
	}
	err := d.Run(ctx, e.iface, e.emitFor(name), report)
	switch {
	case ctx.Err() != nil:
		d.setState(StateCancelled, nil)
	case err != nil:
		d.setState(StateFailed, err)
		e.publish(ProbeEvent{Probe: name, Kind: KindError, Message: err.Error()})
	default:
		d.setState(StateDone, nil)
	}
}

type enricher struct {
	Enricher
	workers  int
	triggers map[model.Field]bool
	wake     chan struct{}

	mu        sync.Mutex
	queue     []string
	queued    map[string]bool
	running   int
	completed int
	failed    int
}

func (en *enricher) snapshot() EnricherStatus {
	en.mu.Lock()
	defer en.mu.Unlock()
	return EnricherStatus{
		Name:      en.Name(),
		Running:   en.running,
		Queued:    len(en.queue),
		Completed: en.completed,
		Failed:    en.failed,
	}
}

func (en *enricher) enqueue(key string) {
	en.mu.Lock()
	if en.queued[key] {
		en.mu.Unlock()
		return
	}
	en.queued[key] = true
	en.queue = append(en.queue, key)
	en.mu.Unlock()
	select {
	case en.wake <- struct{}{}:
	default:
	}
}

func (en *enricher) pop() (string, bool) {
	en.mu.Lock()
	defer en.mu.Unlock()
	if len(en.queue) == 0 {
		return "", false
	}
	key := en.queue[0]
	en.queue = en.queue[1:]
	delete(en.queued, key)
	en.running++
	return key, true
}

func (en *enricher) finish(err error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	en.running--
	if err != nil {
		en.failed++
	} else {
		en.completed++
	}
}

func (e *Engine) enrichWorker(ctx context.Context, en *enricher) {
	defer e.wg.Done()
	name := en.Name()
	emit := e.emitFor(name)
	report := func(ev ProbeEvent) {
		if ev.Probe == "" {
			ev.Probe = name
		}
		e.publish(ev)
	}
	for {
		key, ok := en.pop()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-en.wake:
				continue
			}
		}
		snap, found := e.store.Get(key)
		if !found {
			en.finish(nil)
			continue
		}
		err := en.Enrich(ctx, snap, emit, report)
		if err != nil && ctx.Err() == nil {
			e.publish(ProbeEvent{Probe: name, Kind: KindError, Target: key, Message: err.Error()})
		}
		en.finish(err)
		if ctx.Err() != nil {
			return
		}
	}
}
