package ui

import (
	"fmt"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

// freshness says how recently a probe heard from a device, relative to the
// current scan. It is judged on direct contact only (see model.Direct): a
// resolver or the vendor table answering says nothing about whether the
// device is still there.
type freshness int

const (
	fresh        freshness = iota // heard from since the current scan began
	stale                         // not heard from yet this scan, and the scan is still going
	notAnswering                  // the scan has run its course without hearing from it
	unheard                       // nothing has ever exchanged packets with it
)

func classify(d model.DeviceSnapshot, st engine.Status) freshness {
	at, _, ok := d.LastContact()
	switch {
	case !ok:
		return unheard
	case st.Scan == 0 || !at.Before(st.ScanStarted):
		return fresh
	case st.Settled():
		return notAnswering
	default:
		return stale
	}
}

// mark is the one-cell glyph shown at the left of a table row.
func (f freshness) mark(plain bool) string {
	switch f {
	case stale:
		return "?"
	case notAnswering:
		if plain {
			return "x"
		}
		return "✗"
	case unheard:
		return "-"
	}
	return " "
}

// explain puts the classification into words for the details pane, naming
// the probe and the time so the reader can check the reasoning.
func (f freshness) explain(d model.DeviceSnapshot, st engine.Status, now time.Time) string {
	at, source, _ := d.LastContact()
	switch f {
	case fresh:
		if st.Scan == 0 {
			return fmt.Sprintf("heard from directly %s via %s", ago(now, at), source)
		}
		return fmt.Sprintf("heard from directly %s via %s, since scan %d began", ago(now, at), source, st.Scan)
	case stale:
		return fmt.Sprintf("not heard from since scan %d began; last contact %s via %s, and the scan is still running", st.Scan, ago(now, at), source)
	case notAnswering:
		return fmt.Sprintf("did not answer scan %d, which has finished; last contact %s via %s", st.Scan, ago(now, at), source)
	default:
		return "no probe has exchanged packets with this device; everything known about it came from a third party"
	}
}
