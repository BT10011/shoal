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

	"github.com/BT10011/shoal/internal/capture"
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
  shoal --update          Replace this binary with the latest release (--check just looks)
  shoal --uninstall       Remove shoal, its settings and its history, after listing them

Flags:
  --iface NAME            Interface to scan (default: the one carrying the default route)
  --unprivileged          Skip raw ARP; read the kernel neighbour table instead
  --rate N                Probe packets per second (default 100; 500 for a whole large subnet)
  --max-hosts N           Largest subnet to sweep without asking, in addresses (default 1022,
                          a /22). On a larger network, up to a /16, shoal says what a full
                          sweep costs and asks first: all of it, the /24 around this
                          machine, or listen only
  --also CIDR             Also ask every address in CIDR with ARP, on this segment only,
                          e.g. a venue's usual 192.168.1.0/24, to find gear carrying a stale
                          static address that never speaks. Repeatable. Needs raw access
  --history PATH          Where to remember networks and their devices between runs
                          (default: the per-user data directory; see shoal probe history)
  --no-history            Remember nothing and write nothing
  --theme NAME            Colour theme for this run, e.g. nord, dracula, gruvbox-dark, vt100.
                          A theme kept in the picker (t, Enter) is remembered instead
  --pcap FILE             Record every packet shoal sends and hears to FILE (pcapng), from
                          start to quit, to follow the scan in Wireshark. Off unless asked;
                          in the TUI, w starts and stops a recording instead
  --demo                  Scripted fake data

In the TUI, i lists every interface and address this machine has and
scans the one chosen, with these same flags.

A capture holds every address and name shoal saw on the network: keep it
to yourself unless you have checked what is in it.

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
		// --update and --uninstall look like flags but are commands: they
		// change the machine rather than the scan, so they are taken before
		// any of the TUI's flags are parsed.
		switch {
		case isFlag(args, "update"):
			return runUpdate(without(args, "update"))
		case isFlag(args, "uninstall"):
			return runUninstall(without(args, "uninstall"))
		}
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

// isFlag reports whether args names this flag, in either of the spellings
// Go's flag package accepts.
func isFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "-"+name || a == "--"+name {
			return true
		}
	}
	return false
}

// without drops a flag from args, leaving whatever else was given for the
// command's own flag set to read.
func without(args []string, name string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != "-"+name && a != "--"+name {
			out = append(out, a)
		}
	}
	return out
}

func runTUI(args []string) error {
	fs := flag.NewFlagSet("shoal", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	demo := fs.Bool("demo", false, "use scripted fake probes instead of the network")
	ifaceName := fs.String("iface", "", "interface to scan")
	unprivileged := fs.Bool("unprivileged", false, "read the kernel neighbour table instead of sending ARP")
	rate := fs.Int("rate", 0, "probe packets per second")
	maxHosts := fs.Int("max-hosts", 0, "largest subnet to sweep, in addresses")
	theme := fs.String("theme", ui.DefaultTheme, "colour theme (see the picker, t, for every name)")
	historyPath := fs.String("history", "", "where to remember networks between runs")
	noHistory := fs.Bool("no-history", false, "remember nothing")
	pcapPath := fs.String("pcap", "", "record every packet to this pcapng file")
	var also []*net.IPNet
	fs.Func("also", "also ask every address in this IPv4 range with ARP (repeatable)", alsoFlag(&also))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, ok := ui.ThemeByName(*theme); !ok {
		return fmt.Errorf("--theme: there is no theme called %q; choose one of: %s", *theme, strings.Join(ui.ThemeNames(), ", "))
	}
	themeName, saveTheme := themeSettings(fs, *theme)
	if *demo {
		return runDemo(themeName, saveTheme)
	}

	iface, err := pickInterface(*ifaceName)
	if err != nil {
		return err
	}
	// Nothing is captured unless asked for, with --pcap or w; the tap only
	// writes while a recording is running.
	tap := capture.New(version)
	c := &scanConfig{unprivileged: *unprivileged, rate: *rate, maxHosts: *maxHosts, also: also, tap: tap}
	// Remember each network for next time. A history file that cannot be
	// opened never stops a scan: the mode line says what happened.
	if !*noHistory {
		h, err := openHistory(*historyPath)
		if err != nil {
			c.historyNote = " · no history (" + err.Error() + ")"
		} else {
			defer h.Close()
			c.history = h
		}
	}
	// The first interface is opened here, so a flag that cannot work on it
	// (--also without raw access) stops shoal before the screen clears.
	// A subnet too large to sweep without asking is left to the UI, which
	// asks before anything is sent.
	opts := ui.Options{Iface: iface, Theme: themeName, SaveTheme: saveTheme, Interfaces: netif.Candidates, Open: c.open, Capture: tap}
	s, large, err := c.open(iface, ui.SweepAsk)
	if err != nil {
		return err
	}
	if large == nil {
		opts.Store, opts.Engine, opts.Mode = s.Store, s.Engine, s.Mode
	}
	if *pcapPath != "" {
		if _, err := tap.StartFile(*pcapPath, iface); err != nil {
			return fmt.Errorf("--pcap: %w", err)
		}
	}
	err = ui.Run(context.Background(), opts)
	// A recording still running when shoal quits is closed here, and said
	// so once the screen is back.
	if path, n, cerr := tap.Stop(); path != "" {
		fmt.Printf("saved %d packets to %s; open it in Wireshark, where each packet's comment is shoal's reading of it\n", n, path)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "shoal: the capture may be incomplete: %v\n", cerr)
		}
	}
	return err
}

// largeRate is how fast a sweep of a whole large subnet goes when --rate
// is not given: five times the usual rate, with no retry pass, so a /16
// takes about two minutes rather than twenty.
const largeRate = 500

// scanConfig is what the command line asked for, applied to whichever
// interface is being scanned: the one shoal started on, or one chosen
// later with i.
type scanConfig struct {
	unprivileged bool
	rate         int
	maxHosts     int
	also         []*net.IPNet
	history      *store.History // nil when nothing is remembered
	historyNote  string         // why not, for the mode line
	tap          *capture.Tap   // records packets while the user asks; nil for none
}

// open builds every probe for one interface. Asked with SweepAsk about a
// subnet larger than shoal sweeps without asking, it returns what the UI
// needs to warn the user instead, and builds nothing.
func (c *scanConfig) open(iface netif.Interface, choice ui.SweepChoice) (ui.Session, *ui.LargeSweep, error) {
	raw, why := c.rawAccess(iface)
	if raw && choice == ui.SweepAsk {
		if large := c.large(iface); large != nil {
			return ui.Session{}, large, nil
		}
	}
	if !raw && len(c.also) > 0 {
		// Asked for, and impossible: say so rather than quietly scan less.
		return ui.Session{}, nil, fmt.Errorf("--also needs raw packet access to send ARP, and this run would use the kernel neighbour table (%s); run make setcap, or sudo", why)
	}

	st := store.NewMemory()
	eng := engine.New(st, iface)
	if c.tap != nil {
		eng.Subscribe(c.tap.Observe)
	}
	var mode string
	var discoverers []engine.Discoverer
	switch {
	case !raw:
		mode = "neigh · " + why
		discoverers = append(discoverers, neigh.New(neigh.Options{Rate: c.rate, MaxHosts: c.maxHosts}))
	case choice == ui.SweepNone:
		// The user declined to sweep a large subnet: listen only, so the
		// devices that speak still turn up.
		mode = "arp listen only · sweep declined"
		discoverers = append(discoverers, arp.NewListener(arp.ListenOptions{}))
	default:
		o := arp.Options{Rate: c.rate, MaxHosts: c.maxHosts, Also: c.also}
		mode = "arp sweep + listen"
		switch choice {
		case ui.SweepAll:
			o.MaxHosts, o.NoRetry = arp.LargestSweep, true
			if o.Rate <= 0 {
				o.Rate = largeRate
			}
			mode = fmt.Sprintf("arp sweep of all %s at %d/s, no retry + listen", iface.Subnet, o.Rate)
		case ui.SweepLocal:
			o.Range = around(iface)
			mode = fmt.Sprintf("arp sweep of %s only + listen", o.Range)
		}
		for _, r := range c.also {
			mode += " · also " + r.String()
		}
		sweep := arp.New(o)
		// With raw access, keep listening for ARP after the sweep: a device
		// on the wrong subnet only gives itself away when it happens to speak.
		discoverers = append(discoverers, sweep, arp.NewListener(arp.ListenOptions{StandAside: sweep.Sweeping}))
	}
	for _, d := range discoverers {
		if err := eng.AddDiscoverer(d); err != nil {
			return ui.Session{}, nil, err
		}
	}

	ouiEnricher, err := realOUI()
	if err != nil {
		return ui.Session{}, nil, err
	}
	enrichers := []engine.Enricher{
		// Judge every address against the subnet, so a box carrying an old
		// static or link-local address is pointed out.
		rogue.New(iface.Subnet),
		ouiEnricher,
		// Neither name probe needs privileges, so both run whichever
		// discoverer was chosen above. They answer for different halves of
		// a network: DNS knows what the router was told, mDNS knows what
		// each device calls itself.
		rdns.New(rdns.Options{Resolvers: rdns.SystemResolvers(iface.Gateway)}),
		mdns.New(mdns.Options{Iface: iface}),
		// NetBIOS names the Windows and Samba hosts the other two miss.
		nbns.New(nbns.Options{}),
		// Dante and NDI devices, from what they announce about themselves.
		av.New(),
	}
	for _, en := range enrichers {
		if err := eng.AddEnricher(en); err != nil {
			return ui.Session{}, nil, err
		}
	}
	// The listener writes down the announcements crossing the network, and
	// keeps running after the sweep finishes. It asks once per scan which
	// services are on offer, so Dante and NDI gear shows on a quiet network.
	if err := eng.AddDiscoverer(mdns.NewListener(mdns.ListenerOptions{Browse: mdns.DefaultBrowse})); err != nil {
		return ui.Session{}, nil, err
	}
	// ICMP is the one enricher that can be refused outright, so ask once
	// here rather than failing per device, and say so in the status bar.
	if icmpMode, err := icmp.CheckAccess(); err != nil {
		mode += " · no ICMP"
	} else {
		if err := eng.AddEnricher(icmp.New(icmp.Options{})); err != nil {
			return ui.Session{}, nil, err
		}
		mode += " · icmp " + icmpMode.Short()
	}
	mode += c.historyNote
	if c.history != nil {
		if err := eng.AddDiscoverer(history.New(history.Options{History: c.history, Store: st, Status: eng.Status})); err != nil {
			return ui.Session{}, nil, err
		}
	}
	return ui.Session{Store: st, Engine: eng, Iface: iface, Mode: mode}, nil, nil
}

// rawAccess reports whether the ARP sweep can run on iface, and if not, a
// few words on why for the mode line.
func (c *scanConfig) rawAccess(iface netif.Interface) (bool, string) {
	if c.unprivileged {
		return false, "unprivileged"
	}
	switch err := arp.CheckAccess(iface); {
	case err == nil:
		return true, ""
	case errors.Is(err, arp.ErrPermission):
		return false, "no raw packet access"
	default:
		return false, err.Error()
	}
}

// large describes iface's subnet when it is more than shoal sweeps without
// asking (--max-hosts, a /22 by default), or more than it sweeps at all.
func (c *scanConfig) large(iface netif.Interface) *ui.LargeSweep {
	limit := c.maxHosts
	if limit <= 0 {
		limit = arp.DefaultMaxHosts
	}
	hosts := iface.Hosts()
	if hosts <= min(limit, arp.LargestSweep) {
		return nil
	}
	l := &ui.LargeSweep{Iface: iface, Hosts: hosts, Limit: limit, Largest: arp.LargestSweep, Rate: c.rate, Local: around(iface)}
	if l.Rate <= 0 {
		l.Rate = largeRate
	}
	return l
}

// around is the /24 holding this machine's address: the part of a large
// subnet swept when the user would rather not sweep all of it.
func around(iface netif.Interface) *net.IPNet {
	mask := net.CIDRMask(24, 32)
	return &net.IPNet{IP: iface.IP.Mask(mask), Mask: mask}
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
