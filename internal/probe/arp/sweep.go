package arp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
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
	opts Options
}

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
		return fmt.Errorf("subnet %s has %d hosts, more than the limit of %d; narrow the range", iface.Subnet, n, s.opts.MaxHosts)
	}
	targets := hostIPs(iface.Subnet, iface.IP)

	conn, err := s.opts.Open(iface)
	if err != nil {
		return err
	}

	self := fmt.Sprintf("address of this machine's interface %s (not probed)", iface.Name)
	emit(model.Observation{DeviceKey: iface.MAC.String(), Field: model.FieldMAC, Value: iface.MAC.String(), Source: "netif", Method: self, Confidence: 1})
	emit(model.Observation{DeviceKey: iface.MAC.String(), Field: model.FieldIP, Value: iface.IP.String(), Source: "netif", Method: self, Confidence: 1})
	emit(model.Observation{DeviceKey: iface.MAC.String(), Field: model.FieldFlag, Value: FlagSelf, Source: "netif", Method: self, Confidence: 1})

	report(engine.ProbeEvent{Kind: engine.KindInfo, Message: fmt.Sprintf("sweeping %s on %s: %d addresses at %d/s, then retry silent ones", iface.Subnet, iface.Name, len(targets), s.opts.Rate)})

	l := &listener{iface: iface, emit: emit, report: report, seen: make(map[string]bool)}
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
		if err := conn.WriteFrame(Request(iface.MAC, iface.IP, ip)); err != nil {
			return done, fmt.Errorf("send who-has %s: %w", ip, err)
		}
		done++
		report(engine.ProbeEvent{Kind: engine.KindSent, Target: ip.String(), Message: fmt.Sprintf("who-has %s tell %s", ip, iface.IP)})
		report(engine.ProbeEvent{Kind: engine.KindProgress, Done: done, Total: total})
	}
	return done, nil
}

// listener turns received ARP frames into observations.
type listener struct {
	iface  netif.Interface
	emit   engine.Emit
	report engine.Report

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
		p, err := Decode(frame)
		if err != nil {
			continue
		}
		l.handle(p, frame)
	}
}

func (l *listener) handle(p Packet, frame []byte) {
	if bytes.Equal(p.SenderMAC, l.iface.MAC) || bytes.Equal(p.SenderMAC, zeroMAC) || bytes.Equal(p.SenderMAC, broadcast) {
		return
	}
	if p.SenderIP.IsUnspecified() || !l.iface.Subnet.Contains(p.SenderIP) {
		return
	}
	toUs := p.IsReply() && p.TargetIP.Equal(l.iface.IP) && bytes.Equal(p.TargetMAC, l.iface.MAC)

	var method, message string
	var conf float32
	switch {
	case toUs:
		method = fmt.Sprintf("ARP reply from %s on %s answering our who-has: %s is-at %s", p.SenderIP, l.iface.Name, p.SenderIP, p.SenderMAC)
		message = fmt.Sprintf("%s is-at %s", p.SenderIP, p.SenderMAC)
		conf = 1
	case p.IsReply():
		method = fmt.Sprintf("ARP reply from %s to %s overheard on %s (not addressed to us)", p.SenderIP, p.TargetIP, l.iface.Name)
		message = fmt.Sprintf("%s is-at %s (reply to %s, overheard)", p.SenderIP, p.SenderMAC, p.TargetIP)
		conf = 0.9
	default:
		method = fmt.Sprintf("ARP request from %s overheard on %s (asking who-has %s); the sender's own address is stated in the request", p.SenderIP, l.iface.Name, p.TargetIP)
		message = fmt.Sprintf("who-has %s tell %s (overheard)", p.TargetIP, p.SenderIP)
		conf = 0.9
	}

	l.mu.Lock()
	l.seen[p.SenderIP.String()] = true
	l.mu.Unlock()

	l.report(engine.ProbeEvent{Kind: engine.KindReceived, Target: p.SenderIP.String(), Message: message})
	raw := append([]byte(nil), frame...)
	key := p.SenderMAC.String()
	l.emit(model.Observation{DeviceKey: key, Field: model.FieldMAC, Value: p.SenderMAC.String(), Method: method, Confidence: conf, Raw: raw})
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
