// Package fake provides scripted probes for --demo: a discoverer that
// "sweeps" 192.168.1.0/24 and enrichers that answer from a fixed cast of
// devices. Nothing touches the network. Every Method says "simulated" so
// the provenance is honest. Vendor lookup is not faked: the real oui
// enricher works offline, so the demo uses it with genuine MAC prefixes.
package fake

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
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
}

const (
	defaultInterval = 15 * time.Millisecond
	defaultLatency  = 150 * time.Millisecond
	subnet          = "192.168.1"
	hosts           = 254
	// overhearAt is the address index at which the sweep overhears the
	// devices that are not in its subnet.
	overhearAt = 40
)

// Subnet is the network the demo pretends to scan.
func Subnet() *net.IPNet {
	_, n, _ := net.ParseCIDR(subnet + ".0/24")
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
	return o
}

// DefaultDevices is the demo cast: genuine vendor prefixes (MikroTik,
// Synology, Apple, HP, Google, Philips, Raspberry Pi, Sonos, Espressif), a
// randomised-MAC phone, a hostname disagreement (the printer), two smart
// plugs fighting over one IP, and two visitors from an AV rack: a Sony PTZ
// camera that got no DHCP answer and fell back to a link-local address, and
// a Dante audio box still carrying a static address from another venue.
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
		{IP: "169.254.37.12", MAC: "00:01:4a:7c:2e:01", MDNSName: "PTZ-CAM-1.local", Services: []string{"_rtsp._tcp"},
			Silent: true, Overheard: true, AskingFor: "169.254.37.12"},
		{IP: "192.168.0.77", MAC: "00:1d:c1:12:34:56", Silent: true, Overheard: true, AskingFor: "192.168.0.1"},
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

// Discoverer simulates an ARP sweep of 192.168.1.0/24.
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
func (d *Discoverer) Run(ctx context.Context, _ netif.Interface, emit engine.Emit, report engine.Report) error {
	report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("simulated sweep of %s.0/24 (demo, no packets sent)", subnet)})
	answered := 0
	for n := 1; n <= hosts; n++ {
		ip := fmt.Sprintf("%s.%d", subnet, n)
		report(engine.ProbeEvent{Kind: engine.KindSent, Target: ip, Message: "who-has " + ip})
		if err := sleep(ctx, d.script.rng.jitter(d.opts.Interval)); err != nil {
			return err
		}
		if n == overhearAt {
			d.overhear(emit, report)
		}
		for _, dev := range d.script.byIP[ip] {
			answered++
			report(engine.ProbeEvent{Kind: engine.KindReceived, Target: ip, Message: fmt.Sprintf("%s is-at %s", ip, dev.MAC)})
			method := fmt.Sprintf("simulated ARP reply from %s (demo)", ip)
			emit(model.Observation{DeviceKey: dev.MAC, Field: model.FieldMAC, Value: dev.MAC, Method: method, Confidence: 1})
			emit(model.Observation{DeviceKey: dev.MAC, Field: model.FieldIP, Value: ip, Method: method, Confidence: 1})
		}
		report(engine.ProbeEvent{Kind: engine.KindProgress, Done: n, Total: hosts})
	}
	report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("sweep complete: %d replies from %d addresses", answered, hosts)})
	return nil
}

// overhear plays the ARP frames of the devices that are not in the subnet:
// the sweep never asks them, but their own traffic gives them away.
func (d *Discoverer) overhear(emit engine.Emit, report engine.Report) {
	for _, dev := range d.opts.Devices {
		if !dev.Overheard {
			continue
		}
		var method, message string
		if dev.AskingFor == dev.IP {
			method = fmt.Sprintf("simulated gratuitous ARP from %s overheard during the sweep: the device is announcing that it now holds %s (demo); %s is outside %s.0/24, yet the frame arrived on this segment", dev.IP, dev.IP, dev.IP, subnet)
			message = fmt.Sprintf("announce: %s is-at %s (gratuitous, overheard) · outside our subnet", dev.IP, dev.MAC)
		} else {
			method = fmt.Sprintf("simulated ARP request from %s overheard during the sweep, asking who-has %s (demo); %s is outside %s.0/24, yet the frame arrived on this segment", dev.IP, dev.AskingFor, dev.IP, subnet)
			message = fmt.Sprintf("who-has %s tell %s (overheard) · outside our subnet", dev.AskingFor, dev.IP)
		}
		report(engine.ProbeEvent{Kind: engine.KindReceived, Target: dev.IP, Message: message})
		emit(model.Observation{DeviceKey: dev.MAC, Field: model.FieldMAC, Value: dev.MAC, Method: method, Confidence: 0.9})
		emit(model.Observation{DeviceKey: dev.MAC, Field: model.FieldIP, Value: dev.IP, Method: method, Confidence: 0.9})
	}
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
func (e *rdnsEnricher) Concurrency() int        { return 3 }
func (e *rdnsEnricher) Enrich(ctx context.Context, d model.DeviceSnapshot, emit engine.Emit, report engine.Report) error {
	dev, ok := e.script.lookup(d, time.Now())
	if !ok {
		return nil
	}
	resolver := subnet + ".1"
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
