// Command shoal is a terminal LAN discovery tool that shows its work.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/probe/arp"
	"github.com/BT10011/shoal/internal/probe/fake"
	"github.com/BT10011/shoal/internal/probe/icmp"
	"github.com/BT10011/shoal/internal/probe/mdns"
	"github.com/BT10011/shoal/internal/probe/neigh"
	"github.com/BT10011/shoal/internal/probe/rdns"
	"github.com/BT10011/shoal/internal/store"
	"github.com/BT10011/shoal/internal/ui"
)

const usage = `shoal — LAN discovery that shows its work

Usage:
  shoal [flags]           Scan the local subnet and watch it fill in
  shoal --demo            Scripted fake data; no privileges or network needed
  shoal iface [name]      Show the interface, subnet and gateway shoal would scan
  shoal probe <name>      Run one probe standalone and print what it sends, receives and learns

Flags:
  --iface NAME            Interface to scan (default: the one carrying the default route)
  --unprivileged          Skip raw ARP; read the kernel neighbour table instead
  --rate N                Probe packets per second (default 100)
  --max-hosts N           Largest subnet to sweep, in addresses (default 1022, a /22)
  --theme NAME            Colour theme, e.g. nord, dracula, gruvbox-dark, vt100
  --demo                  Scripted fake data

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
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	demo := fs.Bool("demo", false, "use scripted fake probes instead of the network")
	ifaceName := fs.String("iface", "", "interface to scan")
	unprivileged := fs.Bool("unprivileged", false, "read the kernel neighbour table instead of sending ARP")
	rate := fs.Int("rate", 0, "probe packets per second")
	maxHosts := fs.Int("max-hosts", 0, "largest subnet to sweep, in addresses")
	theme := fs.String("theme", "catppuccin-mocha", "colour theme (a tideui built-in name)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *demo {
		return runDemo(*theme)
	}

	iface, err := pickInterface(*ifaceName)
	if err != nil {
		return err
	}
	st := store.NewMemory()
	eng := engine.New(st, iface)
	discoverer, mode := chooseDiscoverer(iface, *unprivileged, *rate, *maxHosts)
	if err := eng.AddDiscoverer(discoverer); err != nil {
		return err
	}
	ouiEnricher, err := realOUI()
	if err != nil {
		return err
	}
	if err := eng.AddEnricher(ouiEnricher); err != nil {
		return err
	}
	// Neither name probe needs privileges, so both run whichever discoverer
	// was chosen above. They answer for different halves of a network: DNS
	// knows what the router was told, mDNS knows what each device calls
	// itself.
	if err := eng.AddEnricher(rdns.New(rdns.Options{Resolvers: rdns.SystemResolvers(iface.Gateway)})); err != nil {
		return err
	}
	if err := eng.AddEnricher(mdns.New(mdns.Options{})); err != nil {
		return err
	}
	// The listener sends nothing; it writes down the announcements already
	// crossing the network, and keeps running after the sweep finishes.
	if err := eng.AddDiscoverer(mdns.NewListener(mdns.ListenerOptions{})); err != nil {
		return err
	}
	// ICMP is the one enricher that can be refused outright, so ask once
	// here rather than failing per device, and say so in the status bar.
	if icmpMode, err := icmp.CheckAccess(); err != nil {
		mode += " · no ICMP"
	} else {
		if err := eng.AddEnricher(icmp.New(icmp.Options{})); err != nil {
			return err
		}
		mode += " · icmp " + icmpMode.Short()
	}
	return ui.Run(context.Background(), ui.Options{Store: st, Engine: eng, Iface: iface, Mode: mode, Theme: *theme})
}

// chooseDiscoverer prefers a real ARP sweep and falls back to the kernel
// neighbour table when raw packet access is refused. The chosen probe
// explains itself in the event log, so the caller only needs the label.
func chooseDiscoverer(iface netif.Interface, unprivileged bool, rate, maxHosts int) (engine.Discoverer, string) {
	fallback := neigh.New(neigh.Options{Rate: rate, MaxHosts: maxHosts})
	if unprivileged {
		return fallback, "neigh · unprivileged"
	}
	switch err := arp.CheckAccess(iface); {
	case err == nil:
		return arp.New(arp.Options{Rate: rate, MaxHosts: maxHosts}), "arp sweep"
	case errors.Is(err, arp.ErrPermission):
		return fallback, "neigh · no raw packet access"
	default:
		return fallback, "neigh · " + err.Error()
	}
}

func runDemo(theme string) error {
	st := store.NewMemory()
	eng := engine.New(st, netif.Interface{})
	opts := fake.Options{}
	if err := eng.AddDiscoverer(fake.NewDiscoverer(opts)); err != nil {
		return err
	}
	ouiEnricher, err := realOUI()
	if err != nil {
		return err
	}
	for _, en := range append([]engine.Enricher{ouiEnricher}, fake.NewEnrichers(opts)...) {
		if err := eng.AddEnricher(en); err != nil {
			return err
		}
	}
	return ui.Run(context.Background(), ui.Options{Store: st, Engine: eng, Demo: true, Theme: theme})
}

func runIface(args []string) error {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	i, err := pickInterface(name)
	if err != nil {
		return err
	}
	fmt.Print(i.Describe())
	return nil
}
