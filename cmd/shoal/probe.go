package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/probe/arp"
	"github.com/BT10011/shoal/internal/probe/fake"
	"github.com/BT10011/shoal/internal/probe/neigh"
	"github.com/BT10011/shoal/internal/probe/oui"
	"github.com/BT10011/shoal/internal/store"
)

const probeUsage = `Usage: shoal probe <name> [flags]

Probes:
  arp [iface]     Active ARP sweep of the interface's subnet (see docs/protocols/arp.md)
  neigh [iface]   Kernel neighbour cache, no privileges needed (see docs/protocols/neigh.md)
  fake            Scripted demo probes; no network access (see docs/protocols/fake.md)
  oui <mac>       Vendor lookup in the embedded IEEE registry (see docs/protocols/oui.md)
`

func runProbe(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, probeUsage)
		return fmt.Errorf("no probe given")
	}
	switch args[0] {
	case "arp":
		return runProbeARP(args[1:])
	case "neigh":
		return runProbeNeigh(args[1:])
	case "fake":
		return runProbeFake(args[1:])
	case "oui":
		return runProbeOUI(args[1:])
	default:
		fmt.Fprint(os.Stderr, probeUsage)
		return fmt.Errorf("unknown probe %q", args[0])
	}
}

func runProbeFake(args []string) error {
	fs := flag.NewFlagSet("shoal probe fake", flag.ContinueOnError)
	interval := fs.Duration("interval", 0, "pause between simulated ARP requests (default 15ms)")
	latency := fs.Duration("latency", 0, "base round trip for simulated lookups (default 150ms)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := fake.Options{Interval: *interval, Latency: *latency}
	ouiEnricher, err := realOUI()
	if err != nil {
		return err
	}
	enrichers := append([]engine.Enricher{ouiEnricher}, fake.NewEnrichers(opts)...)
	return runStandalone(netif.Interface{}, []engine.Discoverer{fake.NewDiscoverer(opts)}, enrichers, os.Stdout)
}

func runProbeARP(args []string) error {
	fs := flag.NewFlagSet("shoal probe arp", flag.ContinueOnError)
	rate := fs.Int("rate", 0, "who-has requests per second (default 100)")
	noRetry := fs.Bool("no-retry", false, "do not re-ask addresses that stayed silent")
	if err := fs.Parse(args); err != nil {
		return err
	}
	iface, err := pickInterface(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Print(iface.Describe(), "\n")
	sweep := arp.New(arp.Options{Rate: *rate, NoRetry: *noRetry})
	return runStandalone(iface, []engine.Discoverer{sweep}, nil, os.Stdout)
}

func runProbeNeigh(args []string) error {
	fs := flag.NewFlagSet("shoal probe neigh", flag.ContinueOnError)
	rate := fs.Int("rate", 0, "nudge datagrams per second (default 200)")
	noNudge := fs.Bool("no-nudge", false, "send nothing; only read what the kernel already knew")
	if err := fs.Parse(args); err != nil {
		return err
	}
	iface, err := pickInterface(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Print(iface.Describe(), "\nneighbour cache source: ", neigh.TableSource, "\n\n")
	probe := neigh.New(neigh.Options{Rate: *rate, NoNudge: *noNudge})
	return runStandalone(iface, []engine.Discoverer{probe}, nil, os.Stdout)
}

// pickInterface resolves an interface by name, or the one carrying the
// default route when the name is empty.
func pickInterface(name string) (netif.Interface, error) {
	if name == "" {
		return netif.Default()
	}
	return netif.ByName(name)
}

func runProbeOUI(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: shoal probe oui <mac>")
	}
	mac, err := net.ParseMAC(args[0])
	if err != nil {
		return err
	}
	ouiEnricher, err := realOUI()
	if err != nil {
		return err
	}
	seed := seedDiscoverer{key: mac.String(), field: model.FieldMAC, value: mac.String()}
	return runStandalone(netif.Interface{}, []engine.Discoverer{seed}, []engine.Enricher{ouiEnricher}, os.Stdout)
}

func realOUI() (engine.Enricher, error) {
	reg, err := oui.Embedded()
	if err != nil {
		return nil, fmt.Errorf("embedded OUI registry: %w", err)
	}
	return oui.New(reg), nil
}

// seedDiscoverer emits one user-supplied fact so a single enricher can be
// exercised on its own from the command line.
type seedDiscoverer struct {
	key   string
	field model.Field
	value string
}

func (s seedDiscoverer) Name() string { return "seed" }

func (s seedDiscoverer) Run(_ context.Context, _ netif.Interface, emit engine.Emit, report engine.Report) error {
	report(engine.ProbeEvent{Kind: engine.KindInfo, Target: s.key, Message: fmt.Sprintf("%s=%s supplied on the command line", s.field, s.value)})
	emit(model.Observation{DeviceKey: s.key, Field: s.field, Value: s.value, Confidence: 1, Method: "supplied on the command line"})
	return nil
}

// runStandalone drives probes to completion, printing every event and
// observation as it happens, then a summary of what was learned.
func runStandalone(iface netif.Interface, discoverers []engine.Discoverer, enrichers []engine.Enricher, w io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	st := store.NewMemory()
	st.Subscribe(func(ev store.Event) {
		if !ev.Changed {
			return
		}
		o, ok := ev.Device.ResolvedAt(ev.Field, ev.At)
		if !ok {
			return
		}
		fmt.Fprintf(w, "%s %-8s %-9s %-18s %s=%q  <- %s [%s conf %.1f]\n",
			ev.At.Format("15:04:05.000"), "store", ev.Kind, ev.Key, ev.Field, o.Value, o.Method, o.Source, o.Confidence)
	})

	e := engine.New(st, iface)
	e.Subscribe(func(ev engine.ProbeEvent) {
		if ev.Kind == engine.KindProgress {
			return
		}
		fmt.Fprintf(w, "%s %-8s %-9s %-18s %s\n", ev.At.Format("15:04:05.000"), ev.Probe, ev.Kind, ev.Target, ev.Message)
	})
	for _, d := range discoverers {
		if err := e.AddDiscoverer(d); err != nil {
			return err
		}
	}
	for _, en := range enrichers {
		if err := e.AddEnricher(en); err != nil {
			return err
		}
	}
	if err := e.Start(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for !finished(e.Status()) {
		select {
		case <-ctx.Done():
			fmt.Fprintln(w, "\ninterrupted")
			e.Stop()
			return printSummary(w, st)
		case <-ticker.C:
		}
	}
	e.Stop()
	return printSummary(w, st)
}

func finished(s engine.Status) bool {
	for _, d := range s.Discoverers {
		if d.State == engine.StateIdle || d.State == engine.StateRunning {
			return false
		}
	}
	for _, en := range s.Enrichers {
		if en.Running > 0 || en.Queued > 0 {
			return false
		}
	}
	return true
}

func printSummary(w io.Writer, st *store.Memory) error {
	now := time.Now()
	devices := st.Devices()
	fmt.Fprintf(w, "\n%d devices\n", len(devices))
	for _, d := range devices {
		fmt.Fprintf(w, "\n%s\n", d.Key)
		for _, f := range d.Fields() {
			for _, o := range d.Live(f, now) {
				fmt.Fprintf(w, "  %-12s %-28s <- %s [%s conf %.1f]\n", f, o.Value, o.Method, o.Source, o.Confidence)
			}
		}
	}
	return nil
}
