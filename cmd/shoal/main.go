// Command shoal is a terminal LAN discovery tool that shows its work.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/BT10011/shoal/internal/config"
	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/probe/arp"
	"github.com/BT10011/shoal/internal/probe/av"
	"github.com/BT10011/shoal/internal/probe/fake"
	"github.com/BT10011/shoal/internal/probe/history"
	"github.com/BT10011/shoal/internal/probe/icmp"
	"github.com/BT10011/shoal/internal/probe/mdns"
	"github.com/BT10011/shoal/internal/probe/nbns"
	"github.com/BT10011/shoal/internal/probe/neigh"
	"github.com/BT10011/shoal/internal/probe/rdns"
	"github.com/BT10011/shoal/internal/probe/rogue"
	"github.com/BT10011/shoal/internal/store"
	"github.com/BT10011/shoal/internal/ui"
)

const usage = `shoal — LAN discovery that shows its work

Usage:
  shoal [flags]           Scan the local subnet and watch it fill in
  shoal --demo            Scripted fake data; no privileges or network needed
  shoal iface [name]      Show the interface, subnet and gateway shoal would scan
  shoal probe <name>      Run one probe standalone and print what it sends, receives and learns
  shoal version           Which build this is, for bug reports

Flags:
  --iface NAME            Interface to scan (default: the one carrying the default route)
  --unprivileged          Skip raw ARP; read the kernel neighbour table instead
  --rate N                Probe packets per second (default 100)
  --max-hosts N           Largest subnet to sweep, in addresses (default 1022, a /22)
  --also CIDR             Also ask every address in CIDR with ARP, on this segment only,
                          e.g. a venue's usual 192.168.1.0/24, to find gear carrying a stale
                          static address that never speaks. Repeatable. Needs raw access
  --history PATH          Where to remember networks and their devices between runs
                          (default: the per-user data directory; see shoal probe history)
  --no-history            Remember nothing and write nothing
  --theme NAME            Colour theme for this run, e.g. nord, dracula, gruvbox-dark, vt100.
                          A theme kept in the picker (t, Enter) is remembered instead
  --demo                  Scripted fake data

Only scan networks you own or are authorised to test.

shoal remembers each network it scans, recognised by its gateway's MAC, and
the devices seen on it, so the next visit can say what is new, what moved
and what is missing. Delete the history file, or run with --no-history, to
keep nothing.
`

// version is stamped at build time (make build, make release); a plain
// go build says "dev".
var version = "dev"

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
	case "version":
		fmt.Printf("shoal %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
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
	historyPath := fs.String("history", "", "where to remember networks between runs")
	noHistory := fs.Bool("no-history", false, "remember nothing")
	var also []*net.IPNet
	fs.Func("also", "also ask every address in this IPv4 range with ARP (repeatable)", alsoFlag(&also))
	if err := fs.Parse(args); err != nil {
		return err
	}
	themeName, saveTheme := themeSettings(fs, *theme)
	if *demo {
		return runDemo(themeName, saveTheme)
	}

	iface, err := pickInterface(*ifaceName)
	if err != nil {
		return err
	}
	st := store.NewMemory()
	eng := engine.New(st, iface)
	discoverer, mode := chooseDiscoverer(iface, *unprivileged, *rate, *maxHosts, also)
	if _, ok := discoverer.(*arp.Sweep); !ok && len(also) > 0 {
		// Asked for, and impossible: say so rather than quietly scan less.
		return fmt.Errorf("--also needs raw packet access to send ARP, and this run would use the kernel neighbour table (%s); run make setcap, or sudo", mode)
	}
	if err := eng.AddDiscoverer(discoverer); err != nil {
		return err
	}
	// With raw access, keep listening for ARP after the sweep: a device on
	// the wrong subnet only gives itself away when it happens to speak.
	if sweep, ok := discoverer.(*arp.Sweep); ok {
		if err := eng.AddDiscoverer(arp.NewListener(arp.ListenOptions{StandAside: sweep.Sweeping})); err != nil {
			return err
		}
	}
	// Judge every address against the subnet, so a box carrying an old
	// static or link-local address is pointed out.
	if err := eng.AddEnricher(rogue.New(iface.Subnet)); err != nil {
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
	if err := eng.AddEnricher(mdns.New(mdns.Options{Iface: iface})); err != nil {
		return err
	}
	// NetBIOS names the Windows and Samba hosts the other two miss.
	if err := eng.AddEnricher(nbns.New(nbns.Options{})); err != nil {
		return err
	}
	// The listener writes down the announcements crossing the network, and
	// keeps running after the sweep finishes. It asks once per scan which
	// services are on offer, so Dante and NDI gear shows on a quiet network.
	if err := eng.AddDiscoverer(mdns.NewListener(mdns.ListenerOptions{Browse: mdns.DefaultBrowse})); err != nil {
		return err
	}
	// Dante and NDI devices, from what they announce about themselves.
	if err := eng.AddEnricher(av.New()); err != nil {
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
	// Remember this network for next time. A history file that cannot be
	// opened never stops a scan: the mode line says what happened.
	if !*noHistory {
		h, err := openHistory(*historyPath)
		if err != nil {
			mode += " · no history (" + err.Error() + ")"
		} else {
			defer h.Close()
			if err := eng.AddDiscoverer(history.New(history.Options{History: h, Store: st, Status: eng.Status})); err != nil {
				return err
			}
		}
	}
	return ui.Run(context.Background(), ui.Options{Store: st, Engine: eng, Iface: iface, Mode: mode, Theme: themeName, SaveTheme: saveTheme})
}

// themeSettings picks the theme to start with: --theme when given, else
// the one kept last time in the picker, else the default. It also returns
// how to remember a new choice, or nil when there is nowhere to keep it.
// --theme is for this run only and leaves the remembered one alone.
func themeSettings(fs *flag.FlagSet, flagTheme string) (string, func(string) error) {
	explicit := false
	fs.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == "theme" })
	path, err := config.DefaultPath()
	if err != nil {
		return flagTheme, nil
	}
	save := func(name string) error {
		return config.Update(path, func(c *config.Config) { c.Theme = name })
	}
	if explicit {
		return flagTheme, save
	}
	if c, err := config.Load(path); err == nil && c.Theme != "" {
		return c.Theme, save
	}
	return flagTheme, save
}

// openHistory opens the history file at path, or the default one.
func openHistory(path string) (*store.History, error) {
	if path == "" {
		p, err := store.DefaultHistoryPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	return store.OpenHistory(path)
}

// chooseDiscoverer prefers a real ARP sweep and falls back to the kernel
// neighbour table when raw packet access is refused. The chosen probe
// explains itself in the event log, so the caller only needs the label.
func chooseDiscoverer(iface netif.Interface, unprivileged bool, rate, maxHosts int, also []*net.IPNet) (engine.Discoverer, string) {
	fallback := neigh.New(neigh.Options{Rate: rate, MaxHosts: maxHosts})
	if unprivileged {
		return fallback, "neigh · unprivileged"
	}
	switch err := arp.CheckAccess(iface); {
	case err == nil:
		mode := "arp sweep + listen"
		for _, r := range also {
			mode += " · also " + r.String()
		}
		return arp.New(arp.Options{Rate: rate, MaxHosts: maxHosts, Also: also}), mode
	case errors.Is(err, arp.ErrPermission):
		return fallback, "neigh · no raw packet access"
	default:
		return fallback, "neigh · " + err.Error()
	}
}

// alsoFlag parses one --also value into the list. A bare address is taken
// as that one host.
func alsoFlag(into *[]*net.IPNet) func(string) error {
	return func(v string) error {
		if !strings.Contains(v, "/") {
			v += "/32"
		}
		_, n, err := net.ParseCIDR(v)
		if err != nil {
			return err
		}
		if n.IP.To4() == nil {
			return fmt.Errorf("%s: only IPv4 ranges can be asked with ARP", v)
		}
		*into = append(*into, n)
		return nil
	}
}

func runDemo(theme string, saveTheme func(string) error) error {
	st := store.NewMemory()
	eng := engine.New(st, fake.Interface())
	opts := fake.Options{}
	if err := eng.AddDiscoverer(fake.NewDiscoverer(opts)); err != nil {
		return err
	}
	// The demo's history lives in memory, seeded with an invented visit
	// three days ago, so it shows what changed without touching the real file.
	h, err := store.OpenHistory("")
	if err != nil {
		return err
	}
	defer h.Close()
	if err := fake.SeedHistory(h, time.Now()); err != nil {
		return err
	}
	if err := eng.AddDiscoverer(history.New(history.Options{History: h, Store: st, Status: eng.Status})); err != nil {
		return err
	}
	ouiEnricher, err := realOUI()
	if err != nil {
		return err
	}
	for _, en := range append([]engine.Enricher{ouiEnricher, rogue.New(fake.Subnet()), av.New()}, fake.NewEnrichers(opts)...) {
		if err := eng.AddEnricher(en); err != nil {
			return err
		}
	}
	return ui.Run(context.Background(), ui.Options{Store: st, Engine: eng, Demo: true, Theme: theme, SaveTheme: saveTheme})
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
