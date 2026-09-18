package arp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
)

// Conn sends and receives raw Ethernet frames on one interface. ReadFrame
// blocks briefly and returns (nil, nil) when nothing arrived, so callers
// can poll for cancellation; after Close it returns an error.
type Conn interface {
	WriteFrame(frame []byte) error
	ReadFrame() ([]byte, error)
	Close() error
}

// Options tune the sweep. Zero values are the defaults shown.
type Options struct {
	Open     func(netif.Interface) (Conn, error) // OpenConn
	Rate     int                                 // requests per second: 100
	Settle   time.Duration                       // wait for replies after each pass: 1s
	MaxHosts int                                 // refuse larger subnets: 1022 (/22)
	NoRetry  bool                                // skip the second pass for silent hosts
	// Also lists extra IPv4 ranges to ask on this segment (--also): a
	// venue's usual subnet, say, where gear carrying a stale static address
	// sits silent. They are asked with RFC 5227 probes (sender 0.0.0.0).
	Also []*net.IPNet
}

const (
	defaultRate     = 100
	defaultSettle   = time.Second
	defaultMaxHosts = 1022
)

func (o Options) withDefaults() Options {
	if o.Open == nil {
		o.Open = OpenConn
	}
	if o.Rate <= 0 {
		o.Rate = defaultRate
	}
	if o.Settle <= 0 {
		o.Settle = defaultSettle
	}
	if o.MaxHosts <= 0 {
		o.MaxHosts = defaultMaxHosts
	}
	return o
}

// Sweep is the ARP discoverer.
type Sweep struct {
	opts    Options
	running atomic.Bool
}

// Sweeping reports whether the sweep has its socket open and is decoding
// frames. A passive Listener on the same interface would see every one of
// those frames too; it asks this so the log does not show each twice.
func (s *Sweep) Sweeping() bool { return s.running.Load() }

// New creates a sweep.
func New(opts Options) *Sweep {
	return &Sweep{opts: opts.withDefaults()}
}

func (s *Sweep) Name() string { return "arp" }

// FlagSelf marks the device that is this machine.
const FlagSelf = "this-host"

// Run asks every host in the interface's subnet who-has, listens for
// replies and other ARP traffic, and retries addresses that stayed silent.
func (s *Sweep) Run(ctx context.Context, iface netif.Interface, emit engine.Emit, report engine.Report) error {
	if len(iface.MAC) != 6 || iface.IP.To4() == nil || iface.Subnet == nil {
		return fmt.Errorf("interface %q has no Ethernet MAC, IPv4 address or subnet", iface.Name)
	}
	if n := iface.Hosts(); n > s.opts.MaxHosts {
		return fmt.Errorf("subnet %s has %d hosts, more than the limit of %d; raise it with -max-hosts if you are authorised to scan this network", iface.Subnet, n, s.opts.MaxHosts)
	}
	targets := hostIPs(iface.Subnet, iface.IP)
	extra, err := s.extraTargets(iface)
	if err != nil {
		return err
	}
	targets = append(targets, extra...)

	conn, err := s.opts.Open(iface)
	if err != nil {
		return err
	}

	self := fmt.Sprintf("address of this machine's interface %s (not probed)", iface.Name)
	emit(model.Observation{DeviceKey: iface.MAC.String(), Field: model.FieldMAC, Value: iface.MAC.String(), Source: "netif", Method: self, Confidence: 1})
	emit(model.Observation{DeviceKey: iface.MAC.String(), Field: model.FieldIP, Value: iface.IP.String(), Source: "netif", Method: self, Confidence: 1})
	emit(model.Observation{DeviceKey: iface.MAC.String(), Field: model.FieldFlag, Value: FlagSelf, Source: "netif", Method: self, Confidence: 1})

	report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("sweeping %s on %s: %d addresses at %d/s, then retry silent ones", iface.Subnet, iface.Name, len(targets), s.opts.Rate)})
	if len(extra) > 0 {
		report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("also asking %d addresses in %s on this segment, as --also requested. Those requests are RFC 5227 probes, sender 0.0.0.0: they claim no address on that network and leave no entry in anyone's ARP cache, and a device holding the address must answer", len(extra), joinNets(s.opts.Also))})
	}

	l := &listener{iface: iface, emit: emit, report: report, seen: make(map[string]bool), wanted: s.wanted(iface)}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		l.loop(conn)
	}()
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-stop:
		}
		conn.Close()
	}()
	defer func() {
		close(stop)
		wg.Wait()
	}()
	// Cleared before the socket closes (defers run last-first), so for a
	// moment both may handle a frame; a repeat is better than a gap.
	s.running.Store(true)
	defer s.running.Store(false)

	total := len(targets)
	sent, err := s.pass(ctx, conn, iface, targets, 0, total, report)
	if err != nil {
		return err
	}
	if err := sleep(ctx, s.opts.Settle); err != nil {
		return err
	}

	if !s.opts.NoRetry {
		silent := l.silent(targets)
		if len(silent) > 0 {
			report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("%d addresses did not answer; asking each once more", len(silent))})
			total += len(silent)
			if _, err := s.pass(ctx, conn, iface, silent, sent, total, report); err != nil {
				return err
			}
			if err := sleep(ctx, s.opts.Settle); err != nil {
				return err
			}
		}
	}
	report(engine.ProbeEvent{Kind: engine.KindProgress, Done: total, Total: total})
	report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("sweep complete: %d of %d addresses answered", l.answered(), len(targets))})
	return nil
}

// pass sends one who-has per target at the configured rate.
func (s *Sweep) pass(ctx context.Context, conn Conn, iface netif.Interface, targets []net.IP, done, total int, report engine.Report) (int, error) {
	interval := time.Second / time.Duration(s.opts.Rate)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for _, ip := range targets {
		select {
		case <-ctx.Done():
			return done, ctx.Err()
		case <-ticker.C:
		}
		sender, note := iface.IP, ""
		if !iface.Subnet.Contains(ip) {
			sender, note = net.IPv4zero, fmt.Sprintf(" (probe, --also %s)", s.rangeOf(ip))
		}
		if err := conn.WriteFrame(Request(iface.MAC, sender, ip)); err != nil {
			return done, fmt.Errorf("send who-has %s: %w", ip, err)
		}
		done++
		report(engine.ProbeEvent{Kind: engine.KindSent, Target: ip.String(), Message: fmt.Sprintf("who-has %s tell %s%s", ip, sender, note)})
		report(engine.ProbeEvent{Kind: engine.KindProgress, Done: done, Total: total})
	}
	return done, nil
}

// extraTargets lists the addresses of the --also ranges that the subnet
// sweep does not already cover, each once, refusing a range too large to
// ask politely.
func (s *Sweep) extraTargets(iface netif.Interface) ([]net.IP, error) {
	seen := make(map[string]bool)
	var out []net.IP
	for _, r := range s.opts.Also {
		ones, bits := r.Mask.Size()
		if r.IP.To4() == nil || bits != 32 {
			return nil, fmt.Errorf("--also %s: only IPv4 ranges can be asked with ARP", r)
		}
		if size := 1 << (32 - ones); size > s.opts.MaxHosts {
			return nil, fmt.Errorf("--also %s has %d addresses, more than the limit of %d; raise it with -max-hosts if you are authorised to scan that range", r, size, s.opts.MaxHosts)
		}
		for _, ip := range hostIPs(r, iface.IP) {
			if iface.Subnet.Contains(ip) || seen[ip.String()] {
				continue
			}
			seen[ip.String()] = true
			out = append(out, ip)
		}
	}
	return out, nil
}

// rangeOf names the --also range an address was asked under.
func (s *Sweep) rangeOf(ip net.IP) *net.IPNet {
	for _, r := range s.opts.Also {
		if r.Contains(ip) {
			return r
		}
	}
	return nil
}

// wanted reports whether an address is one the sweep asks about, so a
// reply from it counts as an answer and the retry pass can skip it.
func (s *Sweep) wanted(iface netif.Interface) func(net.IP) bool {
	return func(ip net.IP) bool { return iface.Subnet.Contains(ip) || s.rangeOf(ip) != nil }
}

func joinNets(nets []*net.IPNet) string {
	parts := make([]string, len(nets))
	for i, n := range nets {
		parts[i] = n.String()
	}
	return strings.Join(parts, ", ")
}

// listener turns every ARP frame on the interface into facts. It is shared
// by the sweep, which listens while it asks, and by Listener, which only
// listens. Frames are kept whatever address they carry: a device on the
// wrong subnet, or one still probing for an address, is exactly the kind of
// thing worth writing down, and the method says what was unusual about it.
type listener struct {
	iface   netif.Interface
	emit    engine.Emit
	report  engine.Report
	onFrame func()            // called for every frame handled, if set
	skip    func() bool       // when set and true, frames are left to another probe
	wanted  func(net.IP) bool // addresses being asked about; nil means the subnet

	mu   sync.Mutex
	seen map[string]bool
}

func (l *listener) loop(conn Conn) {
	for {
		frame, err := conn.ReadFrame()
		if err != nil {
			return
		}
		if frame == nil {
			continue
		}
		if l.skip != nil && l.skip() {
			continue
		}
		p, err := Decode(frame)
		if err != nil {
			continue
		}
		if l.onFrame != nil {
			l.onFrame()
		}
		l.handle(p, frame)
	}
}

func (l *listener) handle(p Packet, frame []byte) {
	if bytes.Equal(p.SenderMAC, l.iface.MAC) || bytes.Equal(p.SenderMAC, zeroMAC) || bytes.Equal(p.SenderMAC, broadcast) {
		return
	}
	raw := append([]byte(nil), frame...)
	key := p.SenderMAC.String()

	// An ARP probe (RFC 5227): the sender has no address yet and is asking
	// whether the one it wants is free. The MAC is real; the address is not
	// the device's until it announces it.
	if p.SenderIP.IsUnspecified() {
		if !p.IsRequest() {
			return
		}
		method := fmt.Sprintf("ARP probe from %s overheard on %s: sender address 0.0.0.0, asking whether %s is free before taking it (RFC 5227); the device has no address yet", p.SenderMAC, l.iface.Name, p.TargetIP)
		l.report(engine.ProbeEvent{Kind: engine.KindReceived, Target: key,
			Message: fmt.Sprintf("probe: who-has %s tell 0.0.0.0 from %s (overheard; the sender has no address yet)", p.TargetIP, p.SenderMAC)})
		l.emit(model.Observation{DeviceKey: key, Field: model.FieldMAC, Value: key, Method: method, Confidence: 0.9, Raw: raw})
		return
	}

	// A reply to one of our probes (sent from 0.0.0.0 to an --also range)
	// comes back addressed to our MAC with target address 0.0.0.0.
	toUs := p.IsReply() && bytes.Equal(p.TargetMAC, l.iface.MAC) && (p.TargetIP.Equal(l.iface.IP) || p.TargetIP.IsUnspecified())
	toProbe := toUs && p.TargetIP.IsUnspecified()
	inSubnet := l.iface.Subnet.Contains(p.SenderIP)
	where := ""
	if !inSubnet {
		where = fmt.Sprintf("; %s is outside this interface's subnet %s, yet the frame arrived on this segment", p.SenderIP, l.iface.Subnet)
	}

	var method, message string
	var conf float32
	switch {
	case toProbe:
		method = fmt.Sprintf("ARP reply from %s on %s answering our probe for it (sent from 0.0.0.0, as --also does for extra ranges): %s is-at %s%s", p.SenderIP, l.iface.Name, p.SenderIP, p.SenderMAC, where)
		message = fmt.Sprintf("%s is-at %s (answering our probe)", p.SenderIP, p.SenderMAC)
		conf = 1
	case toUs:
		method = fmt.Sprintf("ARP reply from %s on %s answering our who-has: %s is-at %s%s", p.SenderIP, l.iface.Name, p.SenderIP, p.SenderMAC, where)
		message = fmt.Sprintf("%s is-at %s", p.SenderIP, p.SenderMAC)
		conf = 1
	case p.IsReply():
		method = fmt.Sprintf("ARP reply from %s to %s overheard on %s (not addressed to us)%s", p.SenderIP, p.TargetIP, l.iface.Name, where)
		message = fmt.Sprintf("%s is-at %s (reply to %s, overheard)", p.SenderIP, p.SenderMAC, p.TargetIP)
		conf = 0.9
	case p.SenderIP.Equal(p.TargetIP):
		method = fmt.Sprintf("gratuitous ARP from %s overheard on %s: the device is announcing that it now holds %s, which hosts do when they link up or take an address%s", p.SenderIP, l.iface.Name, p.SenderIP, where)
		message = fmt.Sprintf("announce: %s is-at %s (gratuitous, overheard)", p.SenderIP, p.SenderMAC)
		conf = 0.9
	default:
		method = fmt.Sprintf("ARP request from %s overheard on %s (asking who-has %s); the sender's own address is stated in the request%s", p.SenderIP, l.iface.Name, p.TargetIP, where)
		message = fmt.Sprintf("who-has %s tell %s (overheard)", p.TargetIP, p.SenderIP)
		conf = 0.9
	}
	if !inSubnet {
		message += " · outside our subnet"
	}

	wanted := inSubnet
	if l.wanted != nil {
		wanted = l.wanted(p.SenderIP)
	}
	if wanted {
		l.mu.Lock()
		l.seen[p.SenderIP.String()] = true
		l.mu.Unlock()
	}

	l.report(engine.ProbeEvent{Kind: engine.KindReceived, Target: p.SenderIP.String(), Message: message})
	l.emit(model.Observation{DeviceKey: key, Field: model.FieldMAC, Value: key, Method: method, Confidence: conf, Raw: raw})
	l.emit(model.Observation{DeviceKey: key, Field: model.FieldIP, Value: p.SenderIP.String(), Method: method, Confidence: conf, Raw: raw})
}

func (l *listener) silent(targets []net.IP) []net.IP {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []net.IP
	for _, ip := range targets {
		if !l.seen[ip.String()] {
			out = append(out, ip)
		}
	}
	return out
}

func (l *listener) answered() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.seen)
}

// hostIPs lists every host address in the subnet except exclude (and the
// network and broadcast addresses for prefixes shorter than /31).
func hostIPs(subnet *net.IPNet, exclude net.IP) []net.IP {
	ip4 := subnet.IP.To4()
	ones, bits := subnet.Mask.Size()
	if ip4 == nil || bits != 32 {
		return nil
	}
	base := binary.BigEndian.Uint32(ip4) & binary.BigEndian.Uint32(subnet.Mask)
	size := uint32(1) << (32 - ones)
	first, last := base, base+size-1
	if ones < 31 {
		first, last = base+1, base+size-2
	}
	var out []net.IP
	for n := first; n <= last; n++ {
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, n)
		if !ip.Equal(exclude) {
			out = append(out, ip)
		}
		if n == last {
			break
		}
	}
	return out
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ErrPermission wraps a raw-socket permission failure with advice.
var ErrPermission = errors.New("raw packet access denied")

// CheckAccess reports whether raw packet access is available on the
// interface by opening a connection and closing it again.
func CheckAccess(iface netif.Interface) error {
	conn, err := OpenConn(iface)
	if err != nil {
		return err
	}
	return conn.Close()
}
