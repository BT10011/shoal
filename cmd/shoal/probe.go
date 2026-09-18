package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/BT10011/shoal/internal/dnswire"
	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/probe/arp"
	"github.com/BT10011/shoal/internal/probe/av"
	"github.com/BT10011/shoal/internal/probe/fake"
	"github.com/BT10011/shoal/internal/probe/icmp"
	"github.com/BT10011/shoal/internal/probe/mdns"
	"github.com/BT10011/shoal/internal/probe/nbns"
	"github.com/BT10011/shoal/internal/probe/neigh"
	"github.com/BT10011/shoal/internal/probe/oui"
	"github.com/BT10011/shoal/internal/probe/rdns"
	"github.com/BT10011/shoal/internal/probe/rogue"
	"github.com/BT10011/shoal/internal/store"
)

const probeUsage = `Usage: shoal probe <name> [flags]

Probes:
  arp [iface]     Active ARP sweep of the interface's subnet, plus any --also ranges
                  (see docs/protocols/arp.md)
  neigh [iface]   Kernel neighbour cache, no privileges needed (see docs/protocols/neigh.md)
  rdns <ip>       Reverse DNS (PTR) lookup, naming the resolver that answered (see docs/protocols/rdns.md)
  mdns <ip>       Ask a device its name over multicast, from port 5353 (see docs/protocols/mdns.md)
  dns-sd [-ask]   Listen to the Bonjour and Avahi conversation and write down what
                  passes; sends nothing unless -ask (also: shoal probe mdns with no ip)
  av [-for 10s]   Ask which services are on offer and point out the Dante and NDI
                  devices that answer (see docs/protocols/av.md)
  icmp <ip>       Echo requests, to measure the round trip (see docs/protocols/icmp.md)
  nbns <ip>       NetBIOS node status: the name table Windows and Samba hosts keep
                  (see docs/protocols/nbns.md)
  fake            Scripted demo probes; no network access (see docs/protocols/fake.md)
  oui <mac>       Vendor lookup in the embedded IEEE registry (see docs/protocols/oui.md)
  history [-history PATH]
                  List every network shoal remembers and the devices seen on each
                  (see docs/protocols/history.md)
  rogue <ip> [subnet]
                  Say whether an address belongs to the subnet, and if not why: link-local
                  or left over from another network (see docs/protocols/rogue.md)
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
	case "rdns":
		return runProbeRDNS(args[1:])
	case "mdns":
		return runProbeMDNS(args[1:])
	case "dns-sd":
		return runProbeMDNS(args[1:]) // with no address it listens
	case "av":
		return runProbeAV(args[1:])
	case "icmp":
		return runProbeICMP(args[1:])
	case "nbns":
		return runProbeNBNS(args[1:])
	case "oui":
		return runProbeOUI(args[1:])
	case "rogue":
		return runProbeRogue(args[1:])
	case "history":
		return runProbeHistory(args[1:])
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
	enrichers := append([]engine.Enricher{ouiEnricher, rogue.New(fake.Subnet())}, fake.NewEnrichers(opts)...)
	return runStandalone(netif.Interface{}, []engine.Discoverer{fake.NewDiscoverer(opts)}, enrichers, os.Stdout)
}

func runProbeARP(args []string) error {
	fs := flag.NewFlagSet("shoal probe arp", flag.ContinueOnError)
	rate := fs.Int("rate", 0, "who-has requests per second (default 100)")
	maxHosts := fs.Int("max-hosts", 0, "largest subnet to sweep, in addresses (default 1022)")
	noRetry := fs.Bool("no-retry", false, "do not re-ask addresses that stayed silent")
	var also []*net.IPNet
	fs.Func("also", "also ask every address in this IPv4 range, with RFC 5227 probes (repeatable)", alsoFlag(&also))
	if err := fs.Parse(args); err != nil {
		return err
	}
	iface, err := pickInterface(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Print(iface.Describe(), "\n")
	sweep := arp.New(arp.Options{Rate: *rate, MaxHosts: *maxHosts, NoRetry: *noRetry, Also: also})
	return runStandalone(iface, []engine.Discoverer{sweep}, nil, os.Stdout)
}

func runProbeNeigh(args []string) error {
	fs := flag.NewFlagSet("shoal probe neigh", flag.ContinueOnError)
	rate := fs.Int("rate", 0, "nudge datagrams per second (default 200)")
	maxHosts := fs.Int("max-hosts", 0, "largest subnet to nudge, in addresses (default 1022)")
	noNudge := fs.Bool("no-nudge", false, "send nothing; only read what the kernel already knew")
	if err := fs.Parse(args); err != nil {
		return err
	}
	iface, err := pickInterface(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Print(iface.Describe(), "\nneighbour cache source: ", neigh.TableSource, "\n\n")
	probe := neigh.New(neigh.Options{Rate: *rate, MaxHosts: *maxHosts, NoNudge: *noNudge})
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

func runProbeRDNS(args []string) error {
	fs := flag.NewFlagSet("shoal probe rdns", flag.ContinueOnError)
	server := fs.String("resolver", "", "ask this resolver instead of the system's (host or host:port)")
	timeout := fs.Duration("timeout", 0, "how long to wait for each resolver (default 2s)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shoal probe rdns <ip> [-resolver host] [-timeout 2s]")
	}
	ip := net.ParseIP(fs.Arg(0))
	if ip == nil {
		return fmt.Errorf("%q is not an IP address", fs.Arg(0))
	}

	resolvers, err := chooseResolvers(*server)
	if err != nil {
		return err
	}
	name, err := dnswire.ReverseName(ip)
	if err != nil {
		return err
	}
	fmt.Printf("question   %s\n", name)
	for i, r := range resolvers {
		fmt.Printf("resolver %d %-21s %s\n", i+1, r.Addr, r.Origin)
	}
	fmt.Println()

	probe := rdns.New(rdns.Options{Resolvers: resolvers, Timeout: *timeout})
	seed := seedDiscoverer{key: ip.String(), field: model.FieldIP, value: ip.String()}
	return runStandalone(netif.Interface{}, []engine.Discoverer{seed}, []engine.Enricher{probe}, os.Stdout)
}

// runProbeMDNSListen watches the multicast group without sending anything.
func runProbeMDNSListen(listenFor time.Duration, browse []string, enrichers []engine.Enricher) error {
	iface, err := netif.Default()
	if err != nil {
		return err
	}
	fmt.Print(iface.Describe())
	if len(browse) == 0 {
		fmt.Printf("group      %s:%d (passive; shoal sends nothing)\n", mdns.Group, mdns.Port)
	} else {
		fmt.Printf("group      %s:%d, asking for %s\n", mdns.Group, mdns.Port, strings.Join(browse, ", "))
	}
	if listenFor > 0 {
		fmt.Printf("listening  %s\n\n", listenFor)
	} else {
		fmt.Print("listening  until interrupted (^C)\n\n")
	}
	listener := mdns.NewListener(mdns.ListenerOptions{Browse: browse})
	return runStandalone(iface, []engine.Discoverer{listener}, enrichers, os.Stdout, listenFor)
}

// runProbeAV asks the network which services are on offer and points out
// the Dante and NDI devices that answer. Each device that speaks is also
// asked its own name, which is how a device shows its list of services is
// its own rather than relayed. Devices are named by address: without a
// sweep there are no MACs, so vendors are not known here.
func runProbeAV(args []string) error {
	fs := flag.NewFlagSet("shoal probe av", flag.ContinueOnError)
	listenFor := fs.Duration("for", 10*time.Second, "how long to listen after asking")
	if err := fs.Parse(args); err != nil {
		return err
	}
	iface, err := netif.Default()
	if err != nil {
		iface = netif.Interface{}
	}
	return runProbeMDNSListen(*listenFor, mdns.DefaultBrowse, []engine.Enricher{av.New(), mdns.New(mdns.Options{Iface: iface})})
}

// chooseResolvers takes the one the user named, or the system's, falling back
// to the gateway when the system names none.
func chooseResolvers(server string) ([]rdns.Resolver, error) {
	if server == "" {
		iface, err := netif.Default()
		if err != nil {
			// Without an interface there is no gateway to fall back to,
			// but resolv.conf may still name a resolver.
			iface = netif.Interface{}
		}
		resolvers := rdns.SystemResolvers(iface.Gateway)
		if len(resolvers) == 0 {
			return nil, fmt.Errorf("no resolver found in %s and no default gateway; name one with -resolver", rdns.ResolvConf)
		}
		return resolvers, nil
	}
	addr := server
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(server, rdns.DefaultPort)
	}
	return []rdns.Resolver{{Addr: addr, Origin: "named with -resolver"}}, nil
}

func runProbeMDNS(args []string) error {
	fs := flag.NewFlagSet("shoal probe mdns", flag.ContinueOnError)
	wait := fs.Duration("wait", 0, "how long to listen for answers (default 1s)")
	listenFor := fs.Duration("for", 0, "listen mode: stop after this long (default: until interrupted)")
	ask := fs.Bool("ask", false, "listen mode: also ask once which services are on offer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		var browse []string
		if *ask {
			browse = mdns.DefaultBrowse
		}
		return runProbeMDNSListen(*listenFor, browse, nil)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shoal probe mdns [ip] [-wait 1s] [-for 30s]")
	}
	ip := net.ParseIP(fs.Arg(0))
	if ip == nil {
		return fmt.Errorf("%q is not an IP address", fs.Arg(0))
	}
	name, err := dnswire.ReverseName(ip)
	if err != nil {
		return err
	}
	iface, err := netif.Default()
	if err != nil {
		iface = netif.Interface{} // let the system pick where multicast goes
	}
	fmt.Printf("question   %s\ngroup      %s:%d (link-local; routers do not forward it)\nasked on   %s, from port %d\n\n", name, mdns.Group, mdns.Port, orDefault(iface.Name), mdns.Port)

	probe := mdns.New(mdns.Options{Wait: *wait, Iface: iface})
	seed := seedDiscoverer{key: ip.String(), field: model.FieldIP, value: ip.String()}
	return runStandalone(netif.Interface{}, []engine.Discoverer{seed}, []engine.Enricher{probe}, os.Stdout)
}

func orDefault(name string) string {
	if name == "" {
		return "the default multicast interface"
	}
	return name
}

func runProbeICMP(args []string) error {
	fs := flag.NewFlagSet("shoal probe icmp", flag.ContinueOnError)
	count := fs.Int("count", 0, "echo requests to send (default 3)")
	interval := fs.Duration("interval", 0, "pause between them (default 100ms)")
	timeout := fs.Duration("timeout", 0, "wait for the last reply (default 1s)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shoal probe icmp <ip> [-count 3] [-interval 100ms] [-timeout 1s]")
	}
	ip := net.ParseIP(fs.Arg(0))
	if ip == nil {
		return fmt.Errorf("%q is not an IP address", fs.Arg(0))
	}
	mode, err := icmp.CheckAccess()
	if err != nil {
		return err
	}
	fmt.Printf("target     %s\nsocket     %s\n\n", ip, mode)

	probe := icmp.New(icmp.Options{Count: *count, Interval: *interval, Timeout: *timeout})
	seed := seedDiscoverer{key: ip.String(), field: model.FieldIP, value: ip.String()}
	return runStandalone(netif.Interface{}, []engine.Discoverer{seed}, []engine.Enricher{probe}, os.Stdout)
}

func runProbeNBNS(args []string) error {
	fs := flag.NewFlagSet("shoal probe nbns", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 0, "how long to wait for the name table (default 1s)")
	port := fs.Int("port", 0, "ask this port instead of 137, for testing against a responder you control")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shoal probe nbns <ip> [-timeout 1s] [-port 137]")
	}
	ip := net.ParseIP(fs.Arg(0))
	if ip == nil {
		return fmt.Errorf("%q is not an IP address", fs.Arg(0))
	}
	asked := nbns.Port
	if *port != 0 {
		asked = *port
	}
	fmt.Printf("target     %s:%d\nquestion   \"*\" (the wildcard name: whatever you are)\n\n", ip, asked)

	probe := nbns.New(nbns.Options{Timeout: *timeout, Port: *port})
	seed := seedDiscoverer{key: ip.String(), field: model.FieldIP, value: ip.String()}
	return runStandalone(netif.Interface{}, []engine.Discoverer{seed}, []engine.Enricher{probe}, os.Stdout)
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

func runProbeRogue(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: shoal probe rogue <ip> [subnet]")
	}
	ip := net.ParseIP(args[0])
	if ip == nil {
		return fmt.Errorf("%q is not an IP address", args[0])
	}
	var subnet *net.IPNet
	if len(args) == 2 {
		_, n, err := net.ParseCIDR(args[1])
		if err != nil {
			return err
		}
		subnet = n
	} else {
		iface, err := pickInterface("")
		if err != nil {
			return err
		}
		subnet = iface.Subnet
		fmt.Printf("subnet     %s (from %s)\n\n", subnet, iface.Name)
	}
	seed := seedDiscoverer{key: ip.String(), field: model.FieldIP, value: ip.String()}
	return runStandalone(netif.Interface{}, []engine.Discoverer{seed}, []engine.Enricher{rogue.New(subnet)}, os.Stdout)
}

// runProbeHistory prints what the history file holds. The probe itself
// only works during a scan, since it needs the gateway to answer; this
// shows what it has to compare against.
func runProbeHistory(args []string) error {
	fs := flag.NewFlagSet("shoal probe history", flag.ContinueOnError)
	path := fs.String("history", "", "history file (default: the per-user data directory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		p, err := store.DefaultHistoryPath()
		if err != nil {
			return err
		}
		*path = p
	}
	if _, err := os.Stat(*path); err != nil {
		fmt.Printf("history    %s\n\nnothing remembered yet: the file is created on the first scan\n", *path)
		return nil
	}
	h, err := store.OpenHistory(*path)
	if err != nil {
		return err
	}
	defer h.Close()
	nets, err := h.Networks()
	if err != nil {
		return err
	}
	fmt.Printf("history    %s\nnetworks   %d\n", *path, len(nets))
	for _, n := range nets {
		_, known, err := h.Recall(n.ID)
		if err != nil {
			return err
		}
		fmt.Printf("\n%s\n  gateway %s · %d visits · first %s · last %s · %d devices\n",
			n.ID, orNone(n.GatewayIP), n.Visits, n.FirstVisit.Local().Format("2006-01-02 15:04"), n.LastVisit.Local().Format("2006-01-02 15:04"), len(known))
		for _, r := range known {
			fmt.Printf("  %-17s  %-15s  %-28s  last heard %s by %s\n", r.MAC, r.IP, r.Hostname, r.LastSeen.Local().Format("2006-01-02 15:04"), orNone(r.LastSource))
		}
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
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
func runStandalone(iface netif.Interface, discoverers []engine.Discoverer, enrichers []engine.Enricher, w io.Writer, limit ...time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// Probes that listen rather than ask never finish on their own.
	if len(limit) > 0 && limit[0] > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit[0])
		defer cancel()
	}

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
			fmt.Fprintln(w, "\nstopped")
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
