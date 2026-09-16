// Command shoal is a terminal LAN discovery tool that shows its work.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/probe/fake"
	"github.com/BT10011/shoal/internal/store"
	"github.com/BT10011/shoal/internal/ui"
)

const usage = `shoal — LAN discovery that shows its work

Usage:
  shoal --demo [--theme name]   TUI on scripted fake data; no root or network needed
  shoal iface [name]            Show the interface, subnet and gateway shoal would scan
  shoal probe <name>            Run one probe standalone and print what it sends, receives and learns

Only scan networks you own or are authorised to test.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "shoal:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return runTUI(args)
	}
	switch args[0] {
	case "iface":
		return runIface(args[1:])
	case "probe":
		return runProbe(args[1:])
	case "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runTUI(args []string) error {
	fs := flag.NewFlagSet("shoal", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, usage, "\nFlags:\n")
		fs.PrintDefaults()
	}
	demo := fs.Bool("demo", false, "use scripted fake probes instead of the network")
	theme := fs.String("theme", "catppuccin-mocha", "colour theme (a tideui built-in name)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*demo {
		return fmt.Errorf("live scanning arrives in Phase 1; run `shoal --demo` for now")
	}

	st := store.NewMemory()
	eng := engine.New(st, netif.Interface{})
	opts := fake.Options{}
	if err := eng.AddDiscoverer(fake.NewDiscoverer(opts)); err != nil {
		return err
	}
	for _, en := range fake.NewEnrichers(opts) {
		if err := eng.AddEnricher(en); err != nil {
			return err
		}
	}
	return ui.Run(context.Background(), ui.Options{Store: st, Engine: eng, Demo: true, Theme: *theme})
}

func runIface(args []string) error {
	var (
		i   netif.Interface
		err error
	)
	if len(args) > 0 {
		i, err = netif.ByName(args[0])
	} else {
		i, err = netif.Default()
	}
	if err != nil {
		return err
	}
	fmt.Print(i.Describe())
	return nil
}
