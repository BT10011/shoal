package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/probe/fake"
	"github.com/BT10011/shoal/internal/store"
)

const probeUsage = `Usage: shoal probe <name> [flags]

Probes:
  fake    Scripted demo probes; no network access (see docs/protocols/fake.md)
`

func runProbe(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, probeUsage)
		return fmt.Errorf("no probe given")
	}
	switch args[0] {
	case "fake":
		return runProbeFake(args[1:])
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
	return runStandalone(netif.Interface{}, []engine.Discoverer{fake.NewDiscoverer(opts)}, fake.NewEnrichers(opts), os.Stdout)
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
