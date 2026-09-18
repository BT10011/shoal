// Package history is the probe that remembers. It sends nothing: it
// recognises the network by its gateway's MAC, puts back on screen what an
// earlier visit found, compares what this visit finds against it, and
// writes the sightings down for next time.
//
// Everything it recalls reaches the table as observations credited to
// model.SourceHistory, with the time and probe of the original sighting in
// the method, so a remembered value is as traceable as a fresh one.
package history

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/store"
)

// Flags the probe raises. Like every flag they are never retracted: they
// say what changed between the last visit and this one.
const (
	FlagNew     = "new-device"
	FlagNewIP   = "ip-changed"
	FlagRenamed = "name-changed"
)

// Confidence is how far a remembered value is trusted: below anything a
// probe learns today, so the present always wins.
const Confidence = 0.5

// Options wire the probe to the store it reads and the file it writes.
type Options struct {
	History *store.History       // where visits are remembered
	Store   *store.Memory        // read, never written: facts go out through emit
	Status  func() engine.Status // to tell when a scan has run its course
	Poll    time.Duration        // how often to compare: 500ms
	Now     func() time.Time
}

const (
	defaultPoll = 500 * time.Millisecond
	// resaveAfter bounds how often an unchanged device's last-seen time is
	// written, so a long scan does not rewrite the file twice a second.
	resaveAfter = 30 * time.Second
)

// Probe is the history discoverer. Its state outlives a single Run, which
// the engine restarts on every rescan: the network is recognised once per
// visit, and the baseline stays what was known before the visit began.
type Probe struct {
	opts Options

	id       *store.NetworkID
	prev     store.Network
	known    map[string]store.Remembered // by MAC, as it stood before this visit
	raised   map[string]bool             // mac|flag|detail already emitted
	saved    map[string]saved            // what was last written, by MAC
	missing  map[int]bool                // scans whose missing devices were reported
	newCount int
	// lastMissing is how many remembered devices the last settled scan
	// did not hear.
	lastMissing int
}

type saved struct {
	ip, hostname, vendor string
	lastSeen             time.Time
}

// New creates the probe.
func New(opts Options) *Probe {
	if opts.Poll <= 0 {
		opts.Poll = defaultPoll
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Probe{opts: opts, raised: make(map[string]bool), saved: make(map[string]saved), missing: make(map[int]bool)}
}

func (p *Probe) Name() string { return "history" }

// Run recognises the network, then compares and records until cancelled.
func (p *Probe) Run(ctx context.Context, iface netif.Interface, emit engine.Emit, report engine.Report) error {
	if iface.Subnet == nil {
		return fmt.Errorf("interface %q has no subnet, so there is no network to remember", iface.Name)
	}
	status := func(msg string) { report(engine.ProbeEvent{Kind: engine.KindProgress, Message: msg}) }

	if p.id == nil {
		if err := p.recognise(ctx, iface, emit, report, status); err != nil || p.id == nil {
			return err
		}
	}

	t := time.NewTicker(p.opts.Poll)
	defer t.Stop()
	for {
		p.compare(emit, report)
		p.record(report, false)
		p.reportMissing(report)
		status(p.summary())
		select {
		case <-ctx.Done():
			p.record(report, true) // what the last moments learned is worth keeping
			report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("saved %d devices for %s", len(p.saved), p.id)})
			return nil
		case <-t.C:
		}
	}
}

// recognise waits to hear the gateway, whose MAC names the network, then
// opens the visit and puts what was remembered back on screen.
func (p *Probe) recognise(ctx context.Context, iface netif.Interface, emit engine.Emit, report engine.Report, status func(string)) error {
	id := store.NetworkID{Subnet: iface.Subnet.String()}
	gatewayIP := ""
	if iface.Gateway != nil {
		gatewayIP = iface.Gateway.String()
		status(fmt.Sprintf("waiting to hear the gateway %s, whose MAC identifies this network", gatewayIP))
		t := time.NewTicker(p.opts.Poll)
		defer t.Stop()
		for {
			if mac := p.macOf(iface.Gateway); mac != "" {
				id.GatewayMAC = mac
				break
			}
			if p.opts.Status != nil && p.opts.Status().Settled() {
				report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("the gateway %s never answered, so this network cannot be told from others on %s; nothing is remembered this visit", gatewayIP, id.Subnet)})
				status("gateway silent: not remembering this visit")
				<-ctx.Done()
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
			}
		}
	}

	now := p.opts.Now()
	prev, known, err := p.opts.History.Visit(id, gatewayIP, now)
	if err != nil {
		return err
	}
	p.id, p.prev = &id, prev
	p.known = make(map[string]store.Remembered, len(known))
	for _, r := range known {
		p.known[r.MAC] = r
	}

	where := p.opts.History.Path()
	if where == "" {
		where = "memory only"
	}
	if prev.Visits == 0 {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("first visit to %s: everything found is new here, and is remembered in %s for next time", id, where)})
	} else {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("recognised %s: visit %d, last on %s; %d devices remembered from before", id, prev.Visits+1, stamp(prev.LastVisit), len(known))})
	}
	for _, r := range known {
		p.recall(r, emit)
	}
	return nil
}

// recall puts one remembered device back on screen as history facts, dated
// when it was last heard. It shows as a row at once; if it answers this
// visit its fresh facts take over, and if not it is plainly missing.
func (p *Probe) recall(r store.Remembered, emit engine.Emit) {
	heard := fmt.Sprintf("remembered from an earlier visit: last heard on this network on %s", stamp(r.LastSeen))
	if r.LastSource != "" {
		heard += " by " + r.LastSource
	}
	fact := func(f model.Field, value, method string) {
		if value == "" {
			return
		}
		emit(model.Observation{DeviceKey: r.MAC, Field: f, Value: value, Source: model.SourceHistory,
			Method: method, Confidence: Confidence, At: r.LastSeen})
	}
	fact(model.FieldMAC, r.MAC, heard)
	fact(model.FieldIP, r.IP, heard+", at this address")
	fact(model.FieldHostname, r.Hostname, heard+", by this name")
	fact(model.FieldVendor, r.Vendor, heard+"; vendor as looked up then")
	fact(model.FieldFirstSeen, r.FirstSeen.UTC().Format(time.RFC3339),
		fmt.Sprintf("remembered: first seen on this network on %s", stamp(r.FirstSeen)))
}

// current is what probes say about a device today, ignoring memories.
func current(d model.DeviceSnapshot, f model.Field, now time.Time) (model.Observation, bool) {
	for _, o := range d.Live(f, now) {
		if !model.Historical(o.Source) {
			return o, true
		}
	}
	return model.Observation{}, false
}

// lastHeard is the newest direct contact this visit, and which probe it was.
func lastHeard(d model.DeviceSnapshot) (time.Time, time.Time, string, bool) {
	var first, last time.Time
	source, ok := "", false
	for _, obs := range d.Facts {
		for _, o := range obs {
			if model.Historical(o.Source) || !model.Direct(o.Source) {
				continue
			}
			if !ok || o.At.Before(first) {
				first = o.At
			}
			if !ok || o.At.After(last) {
				last, source = o.At, o.Source
			}
			ok = true
		}
	}
	return first, last, source, ok
}

func (p *Probe) macOf(ip net.IP) string {
	now := p.opts.Now()
	for _, d := range p.opts.Store.Devices() {
		o, ok := current(d, model.FieldIP, now)
		if !ok || !net.ParseIP(o.Value).Equal(ip) {
			continue
		}
		if mac, ok := current(d, model.FieldMAC, now); ok {
			return mac.Value
		}
	}
	return ""
}

func self(d model.DeviceSnapshot, now time.Time) bool {
	for _, v := range d.Values(model.FieldFlag, now) {
		if v == "this-host" {
			return true
		}
	}
	return false
}

// compare raises a flag the first time a device turns out to be new since
// the last visit, on a new address, or under a new name.
func (p *Probe) compare(emit engine.Emit, report engine.Report) {
	now := p.opts.Now()
	// A device often answers to several names, from different probes that
	// answer at different moments. Judging a rename before they have all
	// had their say would flag a NAS as renamed because DNS answered before
	// mDNS did, so names are compared once the scan has run its course.
	namesSettled := p.opts.Status == nil || p.opts.Status().Settled()
	for _, d := range p.opts.Store.Devices() {
		mac, ok := current(d, model.FieldMAC, now)
		if !ok || self(d, now) {
			continue
		}
		before, known := p.known[mac.Value]
		ip, _ := current(d, model.FieldIP, now)
		name, _ := current(d, model.FieldHostname, now)
		label := describe(mac.Value, ip.Value, name.Value)

		switch {
		case !known && p.prev.Visits > 0:
			p.raise(d.Key, FlagNew, "", emit, report,
				fmt.Sprintf("not seen on this network on any of %d earlier visits; first heard now by %s", p.prev.Visits, mac.Source),
				fmt.Sprintf("new since the last visit: %s", label))
		case known && ip.Value != "" && before.IP != "" && ip.Value != before.IP:
			p.raise(d.Key, FlagNewIP, ip.Value, emit, report,
				fmt.Sprintf("was at %s when last heard on %s; now at %s (%s)", before.IP, stamp(before.LastSeen), ip.Value, ip.Source),
				fmt.Sprintf("%s changed address: was %s on %s", label, before.IP, stamp(before.LastSeen)))
		}
		if known && namesSettled && name.Value != "" && before.Hostname != "" && !anyName(d, before.Hostname, now) {
			p.raise(d.Key, FlagRenamed, name.Value, emit, report,
				fmt.Sprintf("was named %s when last heard on %s; now %s (%s)", before.Hostname, stamp(before.LastSeen), name.Value, name.Source),
				fmt.Sprintf("%s renamed: was %s on %s", label, before.Hostname, stamp(before.LastSeen)))
		}
	}
}

func (p *Probe) raise(key, flag, detail string, emit engine.Emit, report engine.Report, method, message string) {
	k := key + "|" + flag + "|" + detail
	if p.raised[k] {
		return
	}
	p.raised[k] = true
	if flag == FlagNew {
		p.newCount++
	}
	emit(model.Observation{DeviceKey: key, Field: model.FieldFlag, Value: flag, Source: "history", Method: method, Confidence: 1})
	report(engine.ProbeEvent{Kind: engine.KindInfo, Target: key, Message: message})
}

// anyName reports whether any name a probe gives the device today is the
// remembered one: a device still answering to its old name under one
// protocol has not been renamed.
func anyName(d model.DeviceSnapshot, remembered string, now time.Time) bool {
	for _, o := range d.Live(model.FieldHostname, now) {
		if !model.Historical(o.Source) && sameName(o.Value, remembered) {
			return true
		}
	}
	return false
}

// sameName compares names the way a person would: a device called
// OFFICE-NAS by NetBIOS and OFFICE-NAS.local by mDNS has not been renamed, so
// only the first label counts, and case does not.
func sameName(a, b string) bool {
	host := func(s string) string {
		s = strings.ToLower(strings.TrimSuffix(s, "."))
		if i := strings.IndexByte(s, '.'); i >= 0 {
			s = s[:i]
		}
		return s
	}
	return host(a) == host(b)
}

// record writes down every device heard this visit whose facts changed,
// or whose last sighting is getting stale in the file.
func (p *Probe) record(report engine.Report, all bool) {
	now := p.opts.Now()
	var batch []store.Remembered
	for _, d := range p.opts.Store.Devices() {
		mac, ok := current(d, model.FieldMAC, now)
		if !ok {
			continue
		}
		first, last, source, heard := lastHeard(d)
		if !heard {
			continue // a memory that has not answered: nothing new to write
		}
		r := store.Remembered{MAC: mac.Value, FirstSeen: first, LastSeen: last, LastSource: source}
		if o, ok := current(d, model.FieldIP, now); ok {
			r.IP = o.Value
		}
		if o, ok := current(d, model.FieldHostname, now); ok {
			r.Hostname = o.Value
		}
		if o, ok := current(d, model.FieldVendor, now); ok {
			r.Vendor = o.Value
		}
		was, done := p.saved[r.MAC]
		changed := !done || was.ip != r.IP || was.hostname != r.Hostname || was.vendor != r.Vendor
		stale := done && last.Sub(was.lastSeen) >= resaveAfter
		if !changed && !stale && !(all && last.After(was.lastSeen)) {
			continue
		}
		batch = append(batch, r)
		p.saved[r.MAC] = saved{ip: r.IP, hostname: r.Hostname, vendor: r.Vendor, lastSeen: last}
	}
	if len(batch) == 0 {
		return
	}
	if err := p.opts.History.Record(*p.id, batch...); err != nil {
		report(engine.ProbeEvent{Kind: engine.KindError, Message: err.Error()})
	}
}

// reportMissing names, once per scan, every device remembered from before
// that this scan has run its course without hearing.
func (p *Probe) reportMissing(report engine.Report) {
	if p.opts.Status == nil {
		return
	}
	st := p.opts.Status()
	if !st.Settled() || p.missing[st.Scan] {
		return
	}
	p.missing[st.Scan] = true
	byMAC := make(map[string]model.DeviceSnapshot)
	for _, d := range p.opts.Store.Devices() {
		byMAC[d.Key] = d
	}
	var gone []store.Remembered
	for mac, r := range p.known {
		d, ok := byMAC[mac]
		if ok {
			if _, last, _, heard := lastHeard(d); heard && !last.Before(st.ScanStarted) {
				continue
			}
		}
		gone = append(gone, r)
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].MAC < gone[j].MAC })
	for _, r := range gone {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: r.MAC,
			Message: fmt.Sprintf("missing: %s did not answer scan %d; last heard on %s", describe(r.MAC, r.IP, r.Hostname), st.Scan, stamp(r.LastSeen))})
	}
	p.lastMissing = len(gone)
}

func (p *Probe) summary() string {
	if p.id == nil {
		return "waiting"
	}
	if p.prev.Visits == 0 {
		return fmt.Sprintf("first visit to this network · %d remembered", len(p.saved))
	}
	s := fmt.Sprintf("visit %d · %d known · %d new", p.prev.Visits+1, len(p.known), p.newCount)
	if len(p.missing) > 0 {
		s += fmt.Sprintf(" · %d missing", p.lastMissing)
	}
	return s
}

func describe(mac, ip, name string) string {
	parts := []string{mac}
	if ip != "" {
		parts = append(parts, "at "+ip)
	}
	if name != "" {
		parts = append(parts, "("+name+")")
	}
	return strings.Join(parts, " ")
}

// stamp is a time as it reads in the log: the date always, since a visit
// can be months ago.
func stamp(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }
