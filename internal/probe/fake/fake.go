// Package fake provides scripted probes for --demo: a discoverer that
// "sweeps" one of the demo's invented networks, 192.168.1.0/24 unless told
// otherwise, and enrichers that answer from a fixed cast of devices.
// Nothing touches the network. Every Method says "simulated" so the
// provenance is honest. Vendor lookup is not faked: the real oui enricher
// works offline, so the demo uses it with genuine MAC prefixes.
package fake

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/store"
)

// Device is one scripted host.
type Device struct {
	IP       string
	MAC      string
	RDNS     string
	MDNSName string
	Services []string
	RTT      time.Duration
	Silent   bool // does not answer ICMP
	// Overheard marks a device whose address is not in the demo subnet, so
	// the sweep never asks it; it is found because its own ARP traffic is
	// overheard part-way through, as on a real segment.
	Overheard bool
	// AskingFor is the address an overheard device is looking for, usually
	// the gateway of the network it thinks it is on.
	AskingFor string
}

// Options tune the simulation. Zero values pick demo-friendly defaults.
type Options struct {
	Devices  []Device
	Interval time.Duration // pause between ARP "requests"
	Latency  time.Duration // base round trip for enricher lookups
	Seed     uint64
	// Range is what the sweep asks. Nil sweeps the subnet of the interface
	// it runs on, or 192.168.1.0/24 when it has none.
	Range *net.IPNet
	// Listen sweeps nothing: each device is overheard in turn, as it
	// speaks, the way a large network the user chose not to sweep is
	// watched.
	Listen bool
	// Resolver is the DNS server the rdns lookups pretend to ask:
	// 192.168.1.1, the home router, unless set.
	Resolver string
}

const (
	defaultInterval = 15 * time.Millisecond
	defaultLatency  = 150 * time.Millisecond
	subnet          = "192.168.1"
	hosts           = 254 // addresses a sweep of the default network asks
	// overhearAt is the address index at which the sweep overhears the
	// devices that are not in its subnet.
	overhearAt = 40
	// listenEvery is how many sweep intervals a listen-only scan waits
	// between the devices it overhears: 1.5 s at the default pace.
	listenEvery = 100
)

// Subnet is the network the demo pretends to scan.
func Subnet() *net.IPNet {
	_, n, _ := net.ParseCIDR(subnet + ".0/24")
	return n
}

// Interface is the interface the demo starts on: the subnet and the
// gateway at .1, whose MAC the history probe uses to recognise it.
func Interface() netif.Interface {
	return netif.Interface{Name: "en0", MAC: mac("00:00:5e:00:53:10"), IP: net.ParseIP(subnet + ".10"), Subnet: Subnet(),
		Gateway: net.ParseIP(subnet + ".1"), GatewaySource: "demo routing table"}
}

// Interfaces lists the networks the demo machine is on, as the picker (i)
// shows them: the home network the demo starts on, an AV network on a USB
// adapter, a large office network, and a VPN, which has no MAC and so
// cannot be swept with ARP. All of it is invented; the machine's own MACs
// are from the documentation range.
func Interfaces() []netif.Interface {
	noRoute := "only en0 has a default route (demo)"
	return []netif.Interface{
		Interface(),
		{Name: "en7", MAC: mac("00:00:5e:00:53:07"), IP: net.ParseIP("172.16.10.20"), Subnet: cidr("172.16.10.0/24"), GatewaySource: noRoute},
		{Name: "en8", MAC: mac("00:00:5e:00:53:08"), IP: net.ParseIP("172.20.4.15"), Subnet: cidr("172.20.0.0/16"),
			Gateway: net.ParseIP("172.20.0.1"), GatewaySource: "demo routing table"},
		{Name: "utun4", IP: net.ParseIP("198.51.100.7"), Subnet: cidr("198.51.100.0/24"), GatewaySource: noRoute},
	}
}

// DevicesFor is the cast on one of the demo's networks: DefaultDevices on
// the home network, a few AV boxes on en7 and an office on en8. Any other
// network is empty.
func DevicesFor(iface netif.Interface) []Device {
	switch iface.Name {
	case "", Interface().Name:
		return DefaultDevices()
	case "en7":
		return avDevices()
	case "en8":
		return officeDevices()
	}
	return []Device{} // not nil, which would mean the default cast
}

func mac(s string) net.HardwareAddr {
	m, _ := net.ParseMAC(s)
	return m
}

func cidr(s string) *net.IPNet {
	_, n, _ := net.ParseCIDR(s)
	return n
}

func (o Options) withDefaults() Options {
	if o.Devices == nil {
		o.Devices = DefaultDevices()
	}
	if o.Interval == 0 {
		o.Interval = defaultInterval
	}
	if o.Latency == 0 {
		o.Latency = defaultLatency
	}
	if o.Seed == 0 {
		o.Seed = 1
	}
	if o.Resolver == "" {
		o.Resolver = subnet + ".1"
	}
	return o
}

// DefaultDevices is the demo cast: genuine vendor prefixes (MikroTik,
// Synology, Apple, HP, Google, Philips, Raspberry Pi, Sonos, Espressif), a
// randomised-MAC phone, a hostname disagreement (the printer), two smart
// plugs fighting over one IP, and two visitors from an AV rack: a Sony PTZ
// camera with NDI that got no DHCP answer and fell back to a link-local
// address, and a Dante stagebox still carrying a static address from
// another venue.
func DefaultDevices() []Device {
	return []Device{
		{IP: "192.168.1.1", MAC: "2c:c8:1b:4a:10:01", RDNS: "gateway.lan", RTT: 900 * time.Microsecond},
		{IP: "192.168.1.20", MAC: "00:11:32:7f:a2:c4", RDNS: "nas.lan", MDNSName: "synology.local",
			Services: []string{"_smb._tcp", "_http._tcp", "_afpovertcp._tcp"}, RTT: 2 * time.Millisecond},
		{IP: "192.168.1.42", MAC: "3c:22:fb:9e:01:77", MDNSName: "studio-macbook.local",
			Services: []string{"_companion-link._tcp", "_rdlink._tcp"}, RTT: 4 * time.Millisecond},
		{IP: "192.168.1.50", MAC: "00:1e:0b:55:d3:1a", RDNS: "printer.lan", MDNSName: "HP-LaserJet-M404.local",
			Services: []string{"_ipp._tcp", "_pdl-datastream._tcp", "_http._tcp"}, RTT: 3 * time.Millisecond},
		{IP: "192.168.1.60", MAC: "f4:f5:d8:12:34:56", MDNSName: "Chromecast-Living-Room.local",
			Services: []string{"_googlecast._tcp"}, RTT: 8 * time.Millisecond},
		{IP: "192.168.1.77", MAC: "00:17:88:2b:cc:0e", RDNS: "hue.lan", MDNSName: "Philips-hue.local",
			Services: []string{"_hue._tcp", "_hap._tcp"}, RTT: 5 * time.Millisecond},
		{IP: "192.168.1.101", MAC: "da:3b:91:0c:44:e2", MDNSName: "iPhone.local", Silent: true},
		{IP: "192.168.1.150", MAC: "b8:27:eb:6d:2f:90", RDNS: "pi.lan", MDNSName: "raspberrypi.local",
			Services: []string{"_ssh._tcp", "_sftp-ssh._tcp"}, RTT: 2 * time.Millisecond},
		{IP: "192.168.1.200", MAC: "00:0e:58:ab:cd:ef", MDNSName: "Sonos-Kitchen.local",
			Services: []string{"_sonos._tcp", "_spotify-connect._tcp"}, RTT: 6 * time.Millisecond},
		{IP: "192.168.1.230", MAC: "84:cc:a8:01:02:03", RDNS: "plug-a.lan", RTT: 12 * time.Millisecond},
		{IP: "192.168.1.230", MAC: "84:cc:a8:04:05:06", RDNS: "plug-b.lan", RTT: 14 * time.Millisecond},
		{IP: "169.254.37.12", MAC: "00:01:4a:7c:2e:01", MDNSName: "PTZ-CAM-1.local", Services: []string{"_ndi._tcp", "_rtsp._tcp"},
			Silent: true, Overheard: true, AskingFor: "169.254.37.12"},
		{IP: "192.168.0.77", MAC: "00:1d:c1:12:34:56", MDNSName: "Stagebox-FOH.local",
			Services: []string{"_netaudio-arc._udp", "_netaudio-cmc._udp", "_netaudio-dbc._udp"},
			Silent:   true, Overheard: true, AskingFor: "192.168.0.1"},
	}
}

// avDevices is the cast on en7, an AV network with no router and no DNS:
// a switch, a Dante console, stagebox and wireless receiver, and an NDI
// camera.
func avDevices() []Device {
	dante := []string{"_netaudio-arc._udp", "_netaudio-cmc._udp", "_netaudio-dbc._udp"}
	return []Device{
		{IP: "172.16.10.1", MAC: "00:09:5b:2e:61:01", MDNSName: "AV-Switch.local", Services: []string{"_http._tcp"}, RTT: 700 * time.Microsecond},
		{IP: "172.16.10.11", MAC: "00:a0:de:44:19:0b", MDNSName: "Console-FOH.local", Services: dante, RTT: 1100 * time.Microsecond},
		{IP: "172.16.10.21", MAC: "00:1d:c1:3a:5b:02", MDNSName: "Stagebox-A.local", Services: dante, RTT: 900 * time.Microsecond},
		{IP: "172.16.10.31", MAC: "00:12:67:8c:20:31", MDNSName: "PTZ-CAM-2.local", Services: []string{"_ndi._tcp", "_rtsp._tcp"}, RTT: 1600 * time.Microsecond},
		{IP: "172.16.10.41", MAC: "00:0e:dd:51:7a:29", MDNSName: "Wireless-RX1.local", Services: []string{"_netaudio-arc._udp", "_http._tcp"}, RTT: 1300 * time.Microsecond},
	}
}

// officeDevices is the cast on en8, an office /16: the gateway, and a
// meeting room TV and a printer in the /24 around the demo machine. A NAS
// on another floor is found only by a sweep of the whole subnet.
func officeDevices() []Device {
	return []Device{
		{IP: "172.20.0.1", MAC: "00:00:0c:47:2a:01", RDNS: "gw.office.lan", RTT: 1200 * time.Microsecond},
		{IP: "172.20.4.21", MAC: "00:07:ab:6e:12:c4", MDNSName: "MeetingRoom-TV.local", Services: []string{"_airplay._tcp"}, RTT: 9 * time.Millisecond},
		{IP: "172.20.4.30", MAC: "00:1b:a9:3c:88:17", RDNS: "printer-4.office.lan", MDNSName: "Brother-Printer.local",
			Services: []string{"_ipp._tcp", "_http._tcp"}, RTT: 3 * time.Millisecond},
		{IP: "172.20.9.50", MAC: "24:5e:be:41:0d:6a", RDNS: "nas-02.office.lan", MDNSName: "nas-02.local",
			Services: []string{"_smb._tcp", "_http._tcp"}, RTT: 2 * time.Millisecond},
	}
}

type script struct {
	byIP  map[string][]Device
	byMAC map[string]Device
	rng   *lockedRand
}

func newScript(o Options) *script {
	s := &script{byIP: make(map[string][]Device), byMAC: make(map[string]Device), rng: newLockedRand(o.Seed)}
	for _, d := range o.Devices {
		s.byIP[d.IP] = append(s.byIP[d.IP], d)
		s.byMAC[d.MAC] = d
	}
	return s
}

func (s *script) lookup(d model.DeviceSnapshot, now time.Time) (Device, bool) {
	if dev, ok := s.byMAC[d.Key]; ok {
		return dev, true
	}
	if mac, ok := d.ResolvedAt(model.FieldMAC, now); ok {
		if dev, ok := s.byMAC[mac.Value]; ok {
			return dev, true
		}
	}
	return Device{}, false
}

type lockedRand struct {
	mu  sync.Mutex
	rng *rand.Rand
}

func newLockedRand(seed uint64) *lockedRand {
	return &lockedRand{rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
}

// jitter returns d scaled by a random factor in [0.5, 1.5).
func (l *lockedRand) jitter(d time.Duration) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return time.Duration(float64(d) * (0.5 + l.rng.Float64()))
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Discoverer simulates an ARP sweep of the network it runs on, or of
// 192.168.1.0/24 without one.
type Discoverer struct {
	opts   Options
	script *script
}

// NewDiscoverer creates the demo sweep.
func NewDiscoverer(opts Options) *Discoverer {
	opts = opts.withDefaults()
	return &Discoverer{opts: opts, script: newScript(opts)}
}

// Name is "arp" so the demo looks and resolves exactly like a real sweep.
func (d *Discoverer) Name() string { return "arp" }

// Run walks every host address, reporting each request and reply.
func (d *Discoverer) Run(ctx context.Context, iface netif.Interface, emit engine.Emit, report engine.Report) error {
	if d.opts.Listen {
		return d.listen(ctx, iface, emit, report)
	}
	scope := d.opts.Range
	if scope == nil {
		scope = iface.Subnet
	}
	if scope == nil {
		scope = Subnet()
	}
	first, total := hostRange(scope)
	report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("simulated sweep of %s (demo, no packets sent)", scope)})
	answered := 0
	for n := 1; n <= total; n++ {
		ip := ipv4(first + uint32(n-1))
		report(engine.ProbeEvent{Kind: engine.KindSent, Target: ip, Message: "who-has " + ip})
		if err := sleep(ctx, d.script.rng.jitter(d.opts.Interval)); err != nil {
			return err
		}
		if n == overhearAt {
			d.overhear(scope, emit, report)
		}
		for _, dev := range d.script.byIP[ip] {
			answered++
			report(engine.ProbeEvent{Kind: engine.KindReceived, Target: ip, Message: fmt.Sprintf("%s is-at %s", ip, dev.MAC)})
			method := fmt.Sprintf("simulated ARP reply from %s (demo)", ip)
			emit(model.Observation{DeviceKey: dev.MAC, Field: model.FieldMAC, Value: dev.MAC, Method: method, Confidence: 1})
			emit(model.Observation{DeviceKey: dev.MAC, Field: model.FieldIP, Value: ip, Method: method, Confidence: 1})
		}
		report(engine.ProbeEvent{Kind: engine.KindProgress, Done: n, Total: total})
	}
	report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("sweep complete: %d replies from %d addresses", answered, total)})
	return nil
}

// overhear plays the ARP frames of the devices that are not in the subnet:
// the sweep never asks them, but their own traffic gives them away.
func (d *Discoverer) overhear(scope *net.IPNet, emit engine.Emit, report engine.Report) {
	for _, dev := range d.opts.Devices {
		if !dev.Overheard {
			continue
		}
		var method, message string
		if dev.AskingFor == dev.IP {
			method = fmt.Sprintf("simulated gratuitous ARP from %s overheard during the sweep: the device is announcing that it now holds %s (demo); %s is outside %s, yet the frame arrived on this segment", dev.IP, dev.IP, dev.IP, scope)
			message = fmt.Sprintf("announce: %s is-at %s (gratuitous, overheard) · outside our subnet", dev.IP, dev.MAC)
		} else {
			method = fmt.Sprintf("simulated ARP request from %s overheard during the sweep, asking who-has %s (demo); %s is outside %s, yet the frame arrived on this segment", dev.IP, dev.AskingFor, dev.IP, scope)
			message = fmt.Sprintf("who-has %s tell %s (overheard) · outside our subnet", dev.AskingFor, dev.IP)
		}
		report(engine.ProbeEvent{Kind: engine.KindReceived, Target: dev.IP, Message: message})
		emit(model.Observation{DeviceKey: dev.MAC, Field: model.FieldMAC, Value: dev.MAC, Method: method, Confidence: 0.9})
		emit(model.Observation{DeviceKey: dev.MAC, Field: model.FieldIP, Value: dev.IP, Method: method, Confidence: 0.9})
	}
}

// listen sweeps nothing. It overhears each device in turn, as a listener
// hears whatever happens to speak, then waits for the scan to end.
func (d *Discoverer) listen(ctx context.Context, iface netif.Interface, emit engine.Emit, report engine.Report) error {
	report(engine.ProbeEvent{Kind: engine.KindInfo, Message: "listening only, no sweep (demo, no packets sent): devices appear as they speak"})
	for _, dev := range d.opts.Devices {
		if err := sleep(ctx, d.script.rng.jitter(listenEvery*d.opts.Interval)); err != nil {
			return err
		}
		var method, message string
		if gw := iface.Gateway; gw != nil && !gw.Equal(net.ParseIP(dev.IP)) {
			method = fmt.Sprintf("simulated ARP request from %s overheard while listening, asking who-has %s (demo)", dev.IP, gw)
			message = fmt.Sprintf("who-has %s tell %s (overheard)", gw, dev.IP)
		} else {
			method = fmt.Sprintf("simulated gratuitous ARP from %s overheard while listening (demo)", dev.IP)
			message = fmt.Sprintf("announce: %s is-at %s (gratuitous, overheard)", dev.IP, dev.MAC)
		}
		report(engine.ProbeEvent{Kind: engine.KindReceived, Target: dev.IP, Message: message})
		emit(model.Observation{DeviceKey: dev.MAC, Field: model.FieldMAC, Value: dev.MAC, Method: method, Confidence: 0.9})
		emit(model.Observation{DeviceKey: dev.MAC, Field: model.FieldIP, Value: dev.IP, Method: method, Confidence: 0.9})
	}
	<-ctx.Done()
	return ctx.Err()
}

// hostRange is the first host address of n, as a number, and how many
// host addresses n holds: all but its network and broadcast addresses,
// except in a /31 or /32, which have none to spare.
func hostRange(n *net.IPNet) (uint32, int) {
	ip := n.IP.To4()
	if ip == nil {
		return 0, 0
	}
	base := binary.BigEndian.Uint32(ip)
	ones, bits := n.Mask.Size()
	size := 1 << (bits - ones)
	if size <= 2 {
		return base, size
	}
	return base + 1, size - 2
}

func ipv4(v uint32) string {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).String()
}

// NewEnrichers returns the demo rdns, mdns and icmp enrichers. Pair them
// with the real oui enricher, which needs no network.
func NewEnrichers(opts Options) []engine.Enricher {
	opts = opts.withDefaults()
	s := newScript(opts)
	return []engine.Enricher{
		&rdnsEnricher{opts: opts, script: s},
		&mdnsEnricher{opts: opts, script: s},
		&icmpEnricher{opts: opts, script: s},
	}
}

type rdnsEnricher struct {
	opts   Options
	script *script
}

func (e *rdnsEnricher) Name() string            { return "rdns" }
func (e *rdnsEnricher) Triggers() []model.Field { return []model.Field{model.FieldIP} }
func (e *rdnsEnricher) Produces() model.Field   { return model.FieldHostname }
func (e *rdnsEnricher) Concurrency() int        { return 3 }
func (e *rdnsEnricher) Enrich(ctx context.Context, d model.DeviceSnapshot, emit engine.Emit, report engine.Report) error {
	dev, ok := e.script.lookup(d, time.Now())
	if !ok {
		return nil
	}
	resolver := e.opts.Resolver
	report(engine.ProbeEvent{Kind: engine.KindSent, Target: dev.IP, Message: fmt.Sprintf("PTR %s → %s", reverseName(dev.IP), resolver)})
	if err := sleep(ctx, e.script.rng.jitter(e.opts.Latency)); err != nil {
		return err
	}
	if dev.RDNS == "" {
		report(engine.ProbeEvent{Kind: engine.KindReceived, Target: dev.IP, Message: "NXDOMAIN from " + resolver})
		return nil
	}
	report(engine.ProbeEvent{Kind: engine.KindReceived, Target: dev.IP, Message: fmt.Sprintf("PTR %s from %s", dev.RDNS, resolver)})
	emit(model.Observation{DeviceKey: d.Key, Field: model.FieldHostname, Value: dev.RDNS,
		Method: fmt.Sprintf("simulated PTR answer from resolver %s (demo)", resolver), Confidence: 0.7, TTL: 5 * time.Minute})
	return nil
}

func reverseName(ip string) string {
	parts := strings.Split(ip, ".")
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, ".") + ".in-addr.arpa"
}

type mdnsEnricher struct {
	opts   Options
	script *script
}

func (e *mdnsEnricher) Name() string            { return "mdns" }
func (e *mdnsEnricher) Triggers() []model.Field { return []model.Field{model.FieldIP} }
func (e *mdnsEnricher) Produces() model.Field   { return model.FieldHostname }
func (e *mdnsEnricher) Concurrency() int        { return 2 }
func (e *mdnsEnricher) Enrich(ctx context.Context, d model.DeviceSnapshot, emit engine.Emit, report engine.Report) error {
	dev, ok := e.script.lookup(d, time.Now())
	if !ok {
		return nil
	}
	report(engine.ProbeEvent{Kind: engine.KindSent, Target: dev.IP, Message: fmt.Sprintf("PTR %s → 224.0.0.251:5353", reverseName(dev.IP))})
	if err := sleep(ctx, e.script.rng.jitter(e.opts.Latency)); err != nil {
		return err
	}
	if dev.MDNSName == "" {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: dev.IP, Message: "no mDNS answer within timeout"})
		return nil
	}
	report(engine.ProbeEvent{Kind: engine.KindReceived, Target: dev.IP, Message: fmt.Sprintf("PTR %s from %s", dev.MDNSName, dev.IP)})
	emit(model.Observation{DeviceKey: d.Key, Field: model.FieldHostname, Value: dev.MDNSName,
		Method: fmt.Sprintf("simulated multicast PTR answer from %s (demo)", dev.IP), Confidence: 0.9, TTL: 2 * time.Minute})
	if len(dev.Services) == 0 {
		return nil
	}
	report(engine.ProbeEvent{Kind: engine.KindSent, Target: dev.IP, Message: "PTR _services._dns-sd._udp.local → 224.0.0.251:5353"})
	if err := sleep(ctx, e.script.rng.jitter(e.opts.Latency)); err != nil {
		return err
	}
	for _, svc := range dev.Services {
		report(engine.ProbeEvent{Kind: engine.KindReceived, Target: dev.IP, Message: fmt.Sprintf("PTR %s.local from %s", svc, dev.IP)})
		emit(model.Observation{DeviceKey: d.Key, Field: model.FieldService, Value: svc,
			Method: fmt.Sprintf("simulated DNS-SD service enumeration answer from %s (demo)", dev.IP), Confidence: 0.9, TTL: 2 * time.Minute})
	}
	return nil
}

type icmpEnricher struct {
	opts   Options
	script *script
}

func (e *icmpEnricher) Name() string            { return "icmp" }
func (e *icmpEnricher) Triggers() []model.Field { return []model.Field{model.FieldIP} }
func (e *icmpEnricher) Produces() model.Field   { return model.FieldLatency }
func (e *icmpEnricher) Concurrency() int        { return 5 }
func (e *icmpEnricher) Enrich(ctx context.Context, d model.DeviceSnapshot, emit engine.Emit, report engine.Report) error {
	dev, ok := e.script.lookup(d, time.Now())
	if !ok {
		return nil
	}
	report(engine.ProbeEvent{Kind: engine.KindSent, Target: dev.IP, Message: "echo request seq=1 → " + dev.IP})
	if dev.Silent {
		if err := sleep(ctx, e.script.rng.jitter(e.opts.Latency*2)); err != nil {
			return err
		}
		report(engine.ProbeEvent{Kind: engine.KindInfo, Target: dev.IP, Message: "echo request timed out (host may drop ICMP)"})
		return nil
	}
	if err := sleep(ctx, e.script.rng.jitter(e.opts.Latency/5)); err != nil {
		return err
	}
	rtt := e.script.rng.jitter(dev.RTT)
	report(engine.ProbeEvent{Kind: engine.KindReceived, Target: dev.IP, Message: fmt.Sprintf("echo reply seq=1 from %s rtt=%s", dev.IP, formatRTT(rtt))})
	emit(model.Observation{DeviceKey: d.Key, Field: model.FieldLatency, Value: formatRTT(rtt),
		Method: fmt.Sprintf("simulated ICMP echo reply from %s (demo)", dev.IP), Confidence: 1})
	return nil
}

func formatRTT(d time.Duration) string {
	return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
}

// SeedHistory writes an invented previous visit into h, three days before
// now, so the demo shows what history does: an Epson projector that was
// here and is now missing, a printer that has moved address, a Raspberry
// Pi that has been renamed, and the Chromecast, the PTZ camera and the
// Dante box, which were not here before and so are new.
func SeedHistory(h *store.History, now time.Time) error {
	then := now.Add(-72 * time.Hour)
	id := store.NetworkID{Subnet: subnet + ".0/24", GatewayMAC: "2c:c8:1b:4a:10:01"}
	if _, _, err := h.Visit(id, subnet+".1", then); err != nil {
		return err
	}
	firstVisit := then.Add(-40 * 24 * time.Hour)
	var known []store.Remembered
	for _, d := range DefaultDevices() {
		switch d.MAC {
		case "f4:f5:d8:12:34:56", "00:01:4a:7c:2e:01", "00:1d:c1:12:34:56":
			continue // new today
		}
		r := store.Remembered{MAC: d.MAC, IP: d.IP, Hostname: d.MDNSName, FirstSeen: firstVisit, LastSeen: then, LastSource: "arp"}
		if r.Hostname == "" {
			r.Hostname = d.RDNS
		}
		switch d.MAC {
		case "00:1e:0b:55:d3:1a":
			r.IP = subnet + ".51" // the printer took a new lease
		case "b8:27:eb:6d:2f:90":
			r.Hostname = "octopi.local" // repurposed since
		}
		known = append(known, r)
	}
	known = append(known, store.Remembered{MAC: "00:26:ab:5e:77:10", IP: subnet + ".88", Hostname: "EPSON-PROJ.local",
		Vendor: "Seiko Epson Corporation", FirstSeen: firstVisit, LastSeen: then, LastSource: "arp"})
	return h.Record(id, known...)
}
