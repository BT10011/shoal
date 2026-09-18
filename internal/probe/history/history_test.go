package history

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/store"
)

const (
	gw   = "00:00:5e:00:53:21"
	cam  = "00:01:4a:00:00:02"
	prn  = "00:1e:0b:00:00:03"
	proj = "00:26:ab:00:00:04"
	nu   = "f4:f5:d8:00:00:05"
	me   = "00:00:5e:00:53:26"
)

type rig struct {
	t       *testing.T
	st      *store.Memory
	hist    *store.History
	probe   *Probe
	settled atomic.Bool
	start   time.Time

	mu     sync.Mutex
	events []engine.ProbeEvent
}

func newRig(t *testing.T) *rig {
	t.Helper()
	h, err := store.OpenHistory("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	r := &rig{t: t, st: store.NewMemory(), hist: h, start: time.Now()}
	r.probe = New(Options{History: h, Store: r.st, Poll: 5 * time.Millisecond, Status: r.status})
	return r
}

func (r *rig) status() engine.Status {
	st := engine.Status{Scan: 1, ScanStarted: r.start}
	state := engine.StateRunning
	if r.settled.Load() {
		state = engine.StateDone
	}
	st.Discoverers = []engine.DiscovererStatus{{Name: "arp", State: state, Done: 254, Total: 254}}
	return st
}

func (r *rig) emit(o model.Observation) {
	if o.At.IsZero() {
		o.At = time.Now()
	}
	if err := r.st.Apply(o); err != nil {
		r.t.Errorf("rejected %+v: %v", o, err)
	}
}

func (r *rig) report(e engine.ProbeEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *rig) log() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		out = append(out, e.Message)
	}
	return strings.Join(out, "\n")
}

// seen applies what a sweep found today.
func (r *rig) seen(mac, ip string, extra ...model.Observation) {
	r.emit(model.Observation{DeviceKey: mac, Field: model.FieldMAC, Value: mac, Source: "arp", Method: "reply", Confidence: 1})
	r.emit(model.Observation{DeviceKey: mac, Field: model.FieldIP, Value: ip, Source: "arp", Method: "reply", Confidence: 1})
	for _, o := range extra {
		o.DeviceKey = mac
		r.emit(o)
	}
}

func (r *rig) run(ctx context.Context) chan error {
	_, subnet, _ := net.ParseCIDR("10.0.0.0/24")
	iface := netif.Interface{Name: "eth0", Subnet: subnet, Gateway: net.ParseIP("10.0.0.1")}
	done := make(chan error, 1)
	go func() { done <- r.probe.Run(ctx, iface, r.emit, r.report) }()
	return done
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func flags(st *store.Memory, key string) []string {
	d, _ := st.Get(key)
	return d.Values(model.FieldFlag, time.Now())
}

func TestSecondVisitRecallsComparesAndRecords(t *testing.T) {
	r := newRig(t)
	venue := store.NetworkID{Subnet: "10.0.0.0/24", GatewayMAC: gw}
	lastWeek := time.Now().Add(-7 * 24 * time.Hour)
	if _, _, err := r.hist.Visit(venue, "10.0.0.1", lastWeek); err != nil {
		t.Fatal(err)
	}
	if err := r.hist.Record(venue,
		store.Remembered{MAC: gw, IP: "10.0.0.1", FirstSeen: lastWeek, LastSeen: lastWeek, LastSource: "arp"},
		store.Remembered{MAC: cam, IP: "10.0.0.50", Hostname: "cam.local", Vendor: "Sony", FirstSeen: lastWeek.Add(-30 * 24 * time.Hour), LastSeen: lastWeek, LastSource: "arp"},
		store.Remembered{MAC: prn, IP: "10.0.0.60", Hostname: "HP-OLD", FirstSeen: lastWeek, LastSeen: lastWeek, LastSource: "nbns"},
		store.Remembered{MAC: proj, IP: "10.0.0.88", Hostname: "EPSON-PROJ.local", FirstSeen: lastWeek, LastSeen: lastWeek, LastSource: "arp"},
	); err != nil {
		t.Fatal(err)
	}

	// Today: the gateway, the camera on a new address, the printer under a
	// new name, a device never seen before, and this machine. No projector.
	r.seen(gw, "10.0.0.1")
	r.seen(me, "10.0.0.2", model.Observation{Field: model.FieldFlag, Value: "this-host", Source: "netif", Method: "self", Confidence: 1})
	r.seen(cam, "10.0.0.51")
	r.seen(prn, "10.0.0.60", model.Observation{Field: model.FieldHostname, Value: "HP-NEW.local", Source: "mdns", Method: "A", Confidence: 0.9})
	r.seen(nu, "10.0.0.70")

	ctx, cancel := context.WithCancel(context.Background())
	done := r.run(ctx)

	eventually(t, "the projector recalled as a row", func() bool {
		d, ok := r.st.Get(proj)
		ip, has := d.ResolvedAt(model.FieldIP, time.Now())
		return ok && has && ip.Value == "10.0.0.88" && ip.Source == model.SourceHistory
	})
	eventually(t, "flags raised", func() bool { return len(flags(r.st, nu)) == 1 && len(flags(r.st, prn)) == 1 })

	for key, want := range map[string]string{cam: FlagNewIP, prn: FlagRenamed, nu: FlagNew} {
		if got := strings.Join(flags(r.st, key), ","); got != want {
			t.Errorf("%s flags = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{gw, me, proj} {
		if got := flags(r.st, key); len(got) > 0 && !(key == me && len(got) == 1 && got[0] == "this-host") {
			t.Errorf("%s should carry no history flag: %v", key, got)
		}
	}
	d, _ := r.st.Get(cam)
	for _, o := range d.Facts[model.FieldFlag] {
		if o.Value == FlagNewIP && !strings.Contains(o.Method, "was at 10.0.0.50 when last heard on") {
			t.Errorf("the change should say what it was: %q", o.Method)
		}
	}
	if first, ok := d.ResolvedAt(model.FieldFirstSeen, time.Now()); !ok || !strings.Contains(first.Method, "first seen on this network") {
		t.Errorf("first seen should come back from history: %+v", first)
	}
	if d.Conflicting(model.FieldIP, time.Now()) {
		t.Error("yesterday's address is not in conflict with today's")
	}

	log := r.log()
	for _, want := range []string{"recognised 10.0.0.0/24 via " + gw + ": visit 2", "4 devices remembered from before",
		"new since the last visit: " + nu, "changed address: was 10.0.0.50", "renamed: was HP-OLD"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q\n%s", want, log)
		}
	}
	if strings.Contains(log, "missing:") {
		t.Error("nothing is missing until the scan has run its course")
	}

	r.settled.Store(true)
	eventually(t, "missing reported", func() bool { return strings.Contains(r.log(), "missing: "+proj) })
	time.Sleep(30 * time.Millisecond)
	if n := strings.Count(r.log(), "missing: "); n != 1 {
		t.Errorf("only the projector is missing, once: %d reports\n%s", n, r.log())
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	nw, known, err := r.hist.Recall(venue)
	if err != nil || nw.Visits != 2 {
		t.Fatalf("visits = %d %v", nw.Visits, err)
	}
	byMAC := map[string]store.Remembered{}
	for _, k := range known {
		byMAC[k.MAC] = k
	}
	if byMAC[cam].IP != "10.0.0.51" || byMAC[cam].Hostname != "cam.local" || byMAC[cam].Vendor != "Sony" {
		t.Errorf("camera should be saved on its new address, keeping its name: %+v", byMAC[cam])
	}
	if byMAC[prn].Hostname != "HP-NEW.local" || byMAC[nu].IP != "10.0.0.70" {
		t.Errorf("today's facts should be saved: %+v %+v", byMAC[prn], byMAC[nu])
	}
	if !byMAC[proj].LastSeen.Equal(lastWeek.Truncate(time.Millisecond)) || byMAC[proj].IP != "10.0.0.88" {
		t.Errorf("the missing projector keeps its memory untouched: %+v", byMAC[proj])
	}
	if !byMAC[cam].FirstSeen.Before(lastWeek) {
		t.Errorf("first seen must stay the earliest: %v", byMAC[cam].FirstSeen)
	}
}

func TestFirstVisitFlagsNothingNew(t *testing.T) {
	r := newRig(t)
	r.seen(gw, "10.0.0.1")
	r.seen(nu, "10.0.0.70")
	ctx, cancel := context.WithCancel(context.Background())
	done := r.run(ctx)
	eventually(t, "recorded", func() bool {
		_, known, _ := r.hist.Recall(store.NetworkID{Subnet: "10.0.0.0/24", GatewayMAC: gw})
		return len(known) == 2
	})
	cancel()
	<-done
	if f := flags(r.st, nu); len(f) != 0 {
		t.Errorf("on a first visit everything is new, so nothing is flagged: %v", f)
	}
	if !strings.Contains(r.log(), "first visit to 10.0.0.0/24 via "+gw) {
		t.Errorf("log:\n%s", r.log())
	}
}

func TestSilentGatewayRemembersNothing(t *testing.T) {
	r := newRig(t)
	r.seen(nu, "10.0.0.70")
	r.settled.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := r.run(ctx)
	eventually(t, "gave up", func() bool { return strings.Contains(r.log(), "the gateway 10.0.0.1 never answered") })
	cancel()
	<-done
	if nets, _ := r.hist.Networks(); len(nets) != 0 {
		t.Errorf("a network that cannot be recognised must not be recorded: %+v", nets)
	}
}

func TestSameName(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"OFFICE-NAS", "office-nas.local", true},
		{"nas.home", "nas", true},
		{"Stage-Laptop.local.", "stage-laptop.local", true},
		{"HP-OLD", "HP-NEW.local", false},
	} {
		if got := sameName(c.a, c.b); got != c.same {
			t.Errorf("sameName(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
