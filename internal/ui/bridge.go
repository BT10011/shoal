package ui

import (
	"context"
	"reflect"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/store"
)

// Batch is the only message the engine side sends to the UI. The bridge
// coalesces store and probe events and sends at most one Batch per tick.
type Batch struct {
	Devices []model.DeviceSnapshot // nil when nothing changed since the last batch
	Events  []engine.ProbeEvent
	Status  engine.Status
	At      time.Time
}

// maxPending bounds how many probe events accumulate between ticks if the
// UI stalls; the oldest are dropped, since the log only keeps a tail anyway.
const maxPending = 5000

type bridge struct {
	store  *store.Memory
	engine *engine.Engine
	send   func(tea.Msg)
	every  time.Duration

	mu         sync.Mutex
	dirty      bool
	pending    []engine.ProbeEvent
	lastStatus engine.Status
}

func newBridge(st *store.Memory, eng *engine.Engine, send func(tea.Msg), every time.Duration) *bridge {
	b := &bridge{store: st, engine: eng, send: send, every: every}
	st.Subscribe(b.onStore)
	eng.Subscribe(b.onProbe)
	return b
}

func (b *bridge) onStore(store.Event) {
	b.mu.Lock()
	b.dirty = true
	b.mu.Unlock()
}

func (b *bridge) onProbe(ev engine.ProbeEvent) {
	b.mu.Lock()
	b.pending = append(b.pending, ev)
	if len(b.pending) > maxPending {
		b.pending = b.pending[len(b.pending)-maxPending:]
	}
	b.mu.Unlock()
}

func (b *bridge) run(ctx context.Context) {
	t := time.NewTicker(b.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if batch, ok := b.flush(); ok {
				b.send(batch)
			}
		}
	}
}

// flush builds a Batch if anything changed since the previous flush.
func (b *bridge) flush() (Batch, bool) {
	b.mu.Lock()
	dirty, events := b.dirty, b.pending
	b.dirty, b.pending = false, nil
	b.mu.Unlock()

	status := b.engine.Status()
	statusChanged := !reflect.DeepEqual(status, b.lastStatus)
	if !dirty && len(events) == 0 && !statusChanged {
		return Batch{}, false
	}
	b.lastStatus = status

	batch := Batch{Events: events, Status: status, At: time.Now()}
	if dirty {
		batch.Devices = b.store.Devices()
	}
	return batch, true
}
