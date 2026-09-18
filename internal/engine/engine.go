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
	"sync/atomic"
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

// Producer is implemented by an enricher that exists to fill one field,
// such as a name or a round trip. A lookup then counts as answered only
// when that field came back, so the status can say how many devices a
// probe actually told us something about, not just how many it asked.
// An enricher without it counts any observation as an answer.
type Producer interface {
	Produces() model.Field
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

// EnricherStatus is a snapshot of one enricher's queue. The counts cover
// the current scan: a rescan starts them again from zero.
type EnricherStatus struct {
	Name      string
	Produces  model.Field // what an answer means; empty for "anything"
	Running   int
	Queued    int
	Completed int // lookups finished without error
	Failed    int
	Answered  int // lookups that produced the Produces field
}

// Asked is every lookup this scan has started: finished or still waiting.
func (s EnricherStatus) Asked() int { return s.Running + s.Completed + s.Failed }

// Status is a snapshot of the whole engine.
type Status struct {
	// Scan counts runs of the discoverers: 1 for the first, one more for
	// every rescan, 0 before Start. ScanStarted is when the current one began.
	Scan        int
	ScanStarted time.Time
	Discoverers []DiscovererStatus
	Enrichers   []EnricherStatus
}

// Sweeping reports whether a discoverer that counts towards a total is
// still running: the part of a scan that has an end.
func (s Status) Sweeping() bool {
	for _, d := range s.Discoverers {
		if d.State == StateRunning && d.Total > 0 {
			return true
		}
	}
	return false
}

// Settled reports whether the scan has run its course: every discoverer
// with a total has finished (not failed or cancelled, which leave addresses
// unasked) and no enricher has work left. Listeners, which have no total,
// do not hold it up. A scan that has not yet reported a total is not
// settled either, since nothing is known about how far it got.
func (s Status) Settled() bool {
	swept := false
	for _, d := range s.Discoverers {
		if d.Total == 0 {
			continue
		}
		if d.State != StateDone {
			return false
		}
		swept = true
	}
	if !swept {
		return false
	}
	for _, e := range s.Enrichers {
		if e.Running > 0 || e.Queued > 0 {
			return false
		}
	}
	return true
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
	ctx         context.Context // the engine's lifetime; enrichers live this long
	cancel      context.CancelFunc
	wg          sync.WaitGroup

	scan       *scan // the current or most recent scan; nil before Start
	rescanning bool  // a rescan is waiting for the previous scan to let go
}

// scan is one run of every discoverer. Enrichers outlive scans: their worker
// pools keep running and answer whatever each scan turns up.
type scan struct {
	n       int
	started time.Time
	cancel  context.CancelFunc
	wg      sync.WaitGroup // discoverers still running under this scan
	stopped bool           // Cancel has been called on it
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
	var produces model.Field
	if p, ok := en.(Producer); ok {
		produces = p.Produces()
	}
	e.enrichers = append(e.enrichers, &enricher{
		Enricher: en,
		workers:  workers,
		triggers: triggers,
		produces: produces,
		queued:   make(map[string]bool),
		wake:     make(chan struct{}, workers),
	})
	return nil
}

// Start launches the enricher worker pools and the first scan. It returns
// immediately; Stop cancels and waits.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return errors.New("engine already started")
	}
	e.started = true
	e.ctx, e.cancel = context.WithCancel(ctx)

	for _, en := range e.enrichers {
		for i := 0; i < en.workers; i++ {
			e.wg.Add(1)
			go e.enrichWorker(e.ctx, en)
		}
	}
	events := e.startScan()
	e.mu.Unlock()
	for _, ev := range events {
		e.publish(ev)
	}
	return nil
}

// startScan launches every discoverer under a fresh per-scan context and,
// on a rescan, asks the enrichers to look at every known device again so
// names and round trips are refreshed too, not just the address list. It
// must be called with e.mu held; the events it returns are published by the
// caller once the lock is released.
func (e *Engine) startScan() []ProbeEvent {
	n := 1
	if e.scan != nil {
		n = e.scan.n + 1
	}
	ctx, cancel := context.WithCancel(e.ctx)
	s := &scan{n: n, started: e.now(), cancel: cancel}
	e.scan = s
	for _, d := range e.discoverers {
		d.begin()
		s.wg.Add(1)
		e.wg.Add(1)
		go e.runDiscoverer(ctx, s, d)
	}
	if n == 1 {
		return nil
	}

	// Counts describe one scan, so they can be read against the devices on
	// screen; a lookup from the last scan still finishing lands in this one.
	for _, en := range e.enrichers {
		en.resetCounts()
	}
	requeued := 0
	for _, dev := range e.store.Devices() {
		for _, en := range e.enrichers {
			for f := range en.triggers {
				if len(dev.Live(f, s.started)) > 0 {
					en.enqueue(dev.Key)
					requeued++
					break
				}
			}
		}
	}
	return []ProbeEvent{{
		Probe: "scan", Kind: KindInfo, At: s.started,
		Message: fmt.Sprintf("scan %d: running every discoverer again and re-asking the enrichers about %d known devices (%d lookups)", n, e.store.Len(), requeued),
	}}
}

// Rescan stops the current scan, waits for its discoverers to release their
// sockets, then starts a new one. It returns at once; the new scan shows up
// in Status when it begins. Devices are never forgotten: a rescan adds fresh
// observations next to the old ones, which is how a device that has gone
// quiet becomes visible.
func (e *Engine) Rescan() {
	e.mu.Lock()
	if !e.started || e.ctx.Err() != nil || e.rescanning {
		e.mu.Unlock()
		return
	}
	e.rescanning = true
	old := e.scan
	e.wg.Add(1) // under the lock, so Stop's Wait always covers this goroutine
	e.mu.Unlock()

	old.cancel()
	go func() {
		defer e.wg.Done()
		old.wg.Wait()
		e.mu.Lock()
		e.rescanning = false
		var events []ProbeEvent
		if e.ctx.Err() == nil {
			events = e.startScan()
		}
		e.mu.Unlock()
		for _, ev := range events {
			e.publish(ev)
		}
	}()
}

// Cancel stops the current scan: discoverers are cancelled and the enricher
// queues emptied. A lookup already waiting on a reply finishes on its own
// timeout. Rescan starts afresh afterwards. Once a scan has been stopped, or
// has nothing left running, Cancel does nothing, so the key can be pressed
// freely.
func (e *Engine) Cancel() {
	e.mu.Lock()
	if !e.started || e.scan == nil || e.scan.stopped || !e.busyLocked() {
		e.mu.Unlock()
		return
	}
	s := e.scan
	s.stopped = true
	dropped := 0
	for _, en := range e.enrichers {
		dropped += en.clear()
	}
	e.mu.Unlock()
	s.cancel()
	e.publish(ProbeEvent{Probe: "scan", Kind: KindInfo,
		Message: fmt.Sprintf("scan %d stopped: discoverers cancelled, %d queued lookups dropped", s.n, dropped)})
}

// busyLocked reports whether any discoverer is still running or any
// enricher has work in hand. It must be called with e.mu held.
func (e *Engine) busyLocked() bool {
	for _, d := range e.discoverers {
		if d.snapshot().State == StateRunning {
			return true
		}
	}
	for _, en := range e.enrichers {
		if s := en.snapshot(); s.Running > 0 || s.Queued > 0 {
			return true
		}
	}
	return false
}

// Stop cancels all probes and waits for them to exit.
func (e *Engine) Stop() {
	e.mu.Lock()
	cancel := e.cancel
	if cancel != nil {
		cancel()
	}
	e.mu.Unlock()
	e.wg.Wait()
}

// Status returns a snapshot of every probe.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	var s Status
	if e.scan != nil {
		s.Scan, s.ScanStarted = e.scan.n, e.scan.started
	}
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

// begin resets the status for a new scan. Total is kept as a hint: it lets
// the progress bar start at 0/254 rather than blank, and tells the UI this
// probe is a sweep with an end, until the first progress event replaces it.
func (d *discoverer) begin() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status.State = StateRunning
	d.status.Done = 0
	d.status.Err = ""
	d.status.Message = ""
}

func (e *Engine) runDiscoverer(ctx context.Context, s *scan, d *discoverer) {
	defer e.wg.Done()
	defer s.wg.Done()
	name := d.Name()
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
	produces model.Field
	wake     chan struct{}

	mu        sync.Mutex
	queue     []string
	queued    map[string]bool
	running   int
	completed int
	failed    int
	answered  int
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
		Answered:  en.answered,
		Produces:  en.produces,
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

// clear empties the queue and reports how many lookups were dropped.
func (en *enricher) clear() int {
	en.mu.Lock()
	defer en.mu.Unlock()
	n := len(en.queue)
	en.queue = nil
	en.queued = make(map[string]bool)
	return n
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

func (en *enricher) finish(err error, answered bool) {
	en.mu.Lock()
	defer en.mu.Unlock()
	en.running--
	if err != nil {
		en.failed++
	} else {
		en.completed++
	}
	if answered {
		en.answered++
	}
}

func (en *enricher) resetCounts() {
	en.mu.Lock()
	defer en.mu.Unlock()
	en.completed, en.failed, en.answered = 0, 0, 0
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
			en.finish(nil, false)
			continue
		}
		var answered atomic.Bool
		counted := func(o model.Observation) {
			if en.produces == "" || o.Field == en.produces {
				answered.Store(true)
			}
			emit(o)
		}
		err := en.Enrich(ctx, snap, counted, report)
		if err != nil && ctx.Err() == nil {
			e.publish(ProbeEvent{Probe: name, Kind: KindError, Target: key, Message: err.Error()})
		}
		en.finish(err, answered.Load())
		if ctx.Err() != nil {
			return
		}
	}
}
