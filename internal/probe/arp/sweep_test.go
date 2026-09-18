package arp

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
	"github.com/BT10011/shoal/internal/netif"
)

// fakeConn answers who-has for scripted addresses and lets tests inject
// overheard frames.
type fakeConn struct {
	responders map[string]net.HardwareAddr
	frames     chan []byte
	closed     chan struct{}
	once       sync.Once

	mu   sync.Mutex
	sent [][]byte
}

func newFakeConn(responders map[string]net.HardwareAddr) *fakeConn {
	return &fakeConn{responders: responders, frames: make(chan []byte, 1024), closed: make(chan struct{})}
}

func (c *fakeConn) WriteFrame(f []byte) error {
	c.mu.Lock()
	c.sent = append(c.sent, append([]byte(nil), f...))
	c.mu.Unlock()
	p, err := Decode(f)
	if err != nil {
		return err
	}
	if mac, ok := c.responders[p.TargetIP.String()]; ok {
		c.frames <- Reply(mac, p.TargetIP, p.SenderMAC, p.SenderIP)
	}
	return nil
}

func (c *fakeConn) ReadFrame() ([]byte, error) {
	select {
	case <-c.closed:
		return nil, errors.New("closed")
	case f := <-c.frames:
		return f, nil
	case <-time.After(5 * time.Millisecond):
		return nil, nil
	}
}

func (c *fakeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *fakeConn) requests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, f := range c.sent {
		p, _ := Decode(f)
		out = append(out, p.TargetIP.String())
	}
	return out
}

type capture struct {
	mu     sync.Mutex
	obs    []model.Observation
	events []engine.ProbeEvent
}

func (c *capture) emit(o model.Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.obs = append(c.obs, o)
}

func (c *capture) report(e engine.ProbeEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *capture) count(kind engine.EventKind) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func (c *capture) observation(key string, f model.Field) (model.Observation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, o := range c.obs {
		if o.DeviceKey == key && o.Field == f {
			return o, true
		}
	}
	return model.Observation{}, false
}

var (
	ourMAC = net.HardwareAddr{0x3c, 0x22, 0xfb, 0x9e, 0x01, 0x77}
	ourIP  = net.IPv4(10, 0, 0, 2).To4()
)

func testIface(t *testing.T, cidr string) netif.Interface {
	t.Helper()
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return netif.Interface{Name: "test0", MAC: ourMAC, IP: ourIP, Subnet: subnet}
}

func fastOptions(conn Conn) Options {
	return Options{
		Open:   func(netif.Interface) (Conn, error) { return conn, nil },
		Rate:   2000,
		Settle: 30 * time.Millisecond,
	}
}

func TestHostIPs(t *testing.T) {
	_, s24, _ := net.ParseCIDR("10.0.0.0/24")
	ips := hostIPs(s24, net.IPv4(10, 0, 0, 2))
	if len(ips) != 253 || ips[0].String() != "10.0.0.1" || ips[1].String() != "10.0.0.3" || ips[len(ips)-1].String() != "10.0.0.254" {
		t.Fatalf("/24: %d hosts, first %v second %v last %v", len(ips), ips[0], ips[1], ips[len(ips)-1])
	}
	_, s31, _ := net.ParseCIDR("10.0.0.0/31")
	if ips := hostIPs(s31, nil); len(ips) != 2 {
		t.Fatalf("/31: %v", ips)
	}
	_, s32, _ := net.ParseCIDR("10.0.0.7/32")
	if ips := hostIPs(s32, nil); len(ips) != 1 || ips[0].String() != "10.0.0.7" {
		t.Fatalf("/32: %v", ips)
	}
	_, top, _ := net.ParseCIDR("255.255.255.248/29")
	if ips := hostIPs(top, nil); len(ips) != 6 || ips[5].String() != "255.255.255.254" {
		t.Fatalf("top of address space must not wrap: %v", ips)
	}
}

func TestSweepDiscoversRespondersAndRetriesSilent(t *testing.T) {
	macA := net.HardwareAddr{0x00, 0x11, 0x32, 0x00, 0x00, 0x01}
	macB := net.HardwareAddr{0xb8, 0x27, 0xeb, 0x00, 0x00, 0x02}
	conn := newFakeConn(map[string]net.HardwareAddr{"10.0.0.1": macA, "10.0.0.5": macB})
	c := &capture{}
	s := New(fastOptions(conn))
	if err := s.Run(context.Background(), testIface(t, "10.0.0.0/29"), c.emit, c.report); err != nil {
		t.Fatal(err)
	}

	// /29 = .1-.6 minus ourselves (.2) = 5 targets; 2 answer, 3 are retried.
	reqs := conn.requests()
	if len(reqs) != 8 {
		t.Fatalf("sent %d requests %v, want 5 + 3 retries", len(reqs), reqs)
	}
	if strings.Join(reqs[:5], " ") != "10.0.0.1 10.0.0.3 10.0.0.4 10.0.0.5 10.0.0.6" {
		t.Fatalf("first pass = %v", reqs[:5])
	}
	if strings.Join(reqs[5:], " ") != "10.0.0.3 10.0.0.4 10.0.0.6" {
		t.Fatalf("retry pass = %v", reqs[5:])
	}
	if c.count(engine.KindSent) != 8 || c.count(engine.KindReceived) != 2 {
		t.Fatalf("sent=%d received=%d", c.count(engine.KindSent), c.count(engine.KindReceived))
	}

	ip, ok := c.observation(macA.String(), model.FieldIP)
	if !ok || ip.Value != "10.0.0.1" || ip.Confidence != 1 || ip.Raw == nil {
		t.Fatalf("macA ip = %+v ok=%v", ip, ok)
	}
	if !strings.Contains(ip.Method, "answering our who-has") || !strings.Contains(ip.Method, "test0") {
		t.Fatalf("method = %q", ip.Method)
	}
	if mac, ok := c.observation(macB.String(), model.FieldMAC); !ok || mac.Value != macB.String() {
		t.Fatalf("macB mac = %+v ok=%v", mac, ok)
	}

	self, ok := c.observation(ourMAC.String(), model.FieldFlag)
	if !ok || self.Value != FlagSelf || self.Source != "netif" {
		t.Fatalf("self flag = %+v ok=%v", self, ok)
	}
	last := c.events[len(c.events)-1]
	if last.Kind != engine.KindInfo || !strings.Contains(last.Message, "2 of 5 addresses answered") {
		t.Fatalf("final event = %+v", last)
	}
}

func TestSweepOverhearsOtherTraffic(t *testing.T) {
	conn := newFakeConn(nil)
	other := net.HardwareAddr{0xf4, 0xf5, 0xd8, 0x00, 0x00, 0x09}
	third := net.HardwareAddr{0x00, 0x0e, 0x58, 0x00, 0x00, 0x0a}
	conn.frames <- Request(other, net.IPv4(10, 0, 0, 9), net.IPv4(10, 0, 0, 1))
	conn.frames <- Reply(third, net.IPv4(10, 0, 0, 10), other, net.IPv4(10, 0, 0, 9))
	conn.frames <- Request(ourMAC, ourIP, net.IPv4(10, 0, 0, 3))                                // our own frame echoed back
	conn.frames <- Request(net.HardwareAddr{1, 2, 3, 4, 5, 6}, net.IPv4(192, 168, 9, 9), ourIP) // off-subnet sender
	c := &capture{}
	opts := fastOptions(conn)
	opts.NoRetry = true
	if err := New(opts).Run(context.Background(), testIface(t, "10.0.0.0/28"), c.emit, c.report); err != nil {
		t.Fatal(err)
	}

	req, ok := c.observation(other.String(), model.FieldIP)
	if !ok || req.Value != "10.0.0.9" || req.Confidence != 0.9 || !strings.Contains(req.Method, "overheard") || !strings.Contains(req.Method, "who-has 10.0.0.1") {
		t.Fatalf("overheard request = %+v ok=%v", req, ok)
	}
	rep, ok := c.observation(third.String(), model.FieldIP)
	if !ok || rep.Value != "10.0.0.10" || rep.Confidence != 0.9 || !strings.Contains(rep.Method, "not addressed to us") {
		t.Fatalf("overheard reply = %+v ok=%v", rep, ok)
	}
	off, ok := c.observation("01:02:03:04:05:06", model.FieldIP)
	if !ok || off.Value != "192.168.9.9" || off.Confidence != 0.9 || !strings.Contains(off.Method, "outside this interface's subnet 10.0.0.0/28") {
		t.Fatalf("off-subnet sender must be kept and explained: %+v ok=%v", off, ok)
	}
	for _, o := range c.obs {
		if o.DeviceKey == ourMAC.String() && o.Source != "netif" {
			t.Fatalf("our own echoed frame produced an arp observation: %+v", o)
		}
	}
	if c.count(engine.KindReceived) != 3 {
		t.Fatalf("received events = %d, want 3", c.count(engine.KindReceived))
	}
}

func TestListenerKeepsProbesAnnouncementsAndStrangers(t *testing.T) {
	iface := testIface(t, "10.0.0.0/24")
	c := &capture{}
	l := &listener{iface: iface, emit: c.emit, report: c.report, seen: make(map[string]bool)}
	cam := net.HardwareAddr{0x00, 0x01, 0x4a, 0x00, 0x00, 0x01}
	stale := net.HardwareAddr{0x00, 0x1d, 0xc1, 0x00, 0x00, 0x02}
	linkLocal := net.IPv4(169, 254, 37, 12)

	// A camera choosing a link-local address: probe, then announce.
	probe := Request(cam, net.IPv4zero, linkLocal)
	p, _ := Decode(probe)
	l.handle(p, probe)
	if _, ok := c.observation(cam.String(), model.FieldIP); ok {
		t.Fatal("a probe from 0.0.0.0 must not give the device an address")
	}
	mac, ok := c.observation(cam.String(), model.FieldMAC)
	if !ok || mac.Confidence != 0.9 || !strings.Contains(mac.Method, "RFC 5227") || !strings.Contains(mac.Method, "no address yet") {
		t.Fatalf("probe should still record the MAC: %+v ok=%v", mac, ok)
	}
	announce := Request(cam, linkLocal, linkLocal)
	p, _ = Decode(announce)
	l.handle(p, announce)
	ip, ok := c.observation(cam.String(), model.FieldIP)
	if !ok || ip.Value != "169.254.37.12" || !strings.Contains(ip.Method, "gratuitous") || !strings.Contains(ip.Method, "outside this interface's subnet 10.0.0.0/24") {
		t.Fatalf("announcement = %+v ok=%v", ip, ok)
	}

	// A stale static address asking for its old gateway.
	req := Request(stale, net.IPv4(192, 168, 1, 50), net.IPv4(192, 168, 1, 1))
	p, _ = Decode(req)
	l.handle(p, req)
	ip, ok = c.observation(stale.String(), model.FieldIP)
	if !ok || ip.Value != "192.168.1.50" || !strings.Contains(ip.Method, "who-has 192.168.1.1") || !strings.Contains(ip.Method, "outside this interface's subnet") {
		t.Fatalf("stale static = %+v ok=%v", ip, ok)
	}
	if l.answered() != 0 {
		t.Fatal("strangers must not count as answered subnet addresses")
	}
	var msgs []string
	for _, e := range c.events {
		msgs = append(msgs, e.Message)
	}
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{"probe: who-has 169.254.37.12 tell 0.0.0.0", "announce: 169.254.37.12 is-at", "who-has 192.168.1.1 tell 192.168.1.50 (overheard) · outside our subnet"} {
		if !strings.Contains(joined, want) {
			t.Errorf("events missing %q:\n%s", want, joined)
		}
	}
	// A reply from 0.0.0.0 is nonsense and is dropped.
	bad := Reply(stale, net.IPv4zero, ourMAC, ourIP)
	p, _ = Decode(bad)
	before := len(c.obs)
	l.handle(p, bad)
	if len(c.obs) != before {
		t.Fatal("a reply claiming 0.0.0.0 must be ignored")
	}
}

func TestListenerStandsAsideWhileTheSweepRuns(t *testing.T) {
	var sweeping atomic.Bool
	sweeping.Store(true)
	conn := newFakeConn(nil)
	c := &capture{}
	l := NewListener(ListenOptions{
		Open:       func(netif.Interface) (Conn, error) { return conn, nil },
		StandAside: sweeping.Load,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, testIface(t, "10.0.0.0/24"), c.emit, c.report) }()

	during := net.HardwareAddr{0xb8, 0x27, 0xeb, 0, 0, 1}
	after := net.HardwareAddr{0xb8, 0x27, 0xeb, 0, 0, 2}
	conn.frames <- Request(during, net.IPv4(10, 0, 0, 9), net.IPv4(10, 0, 0, 1))
	time.Sleep(50 * time.Millisecond)
	if _, ok := c.observation(during.String(), model.FieldIP); ok || c.count(engine.KindReceived) != 0 {
		t.Fatal("while the sweep runs, the sweep handles the frame and the listener must not repeat it")
	}
	sweeping.Store(false)
	conn.frames <- Request(after, net.IPv4(10, 0, 0, 10), net.IPv4(10, 0, 0, 1))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := c.observation(after.String(), model.FieldIP); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := c.observation(after.String(), model.FieldIP); !ok {
		t.Fatal("once the sweep finishes the listener must take over")
	}
	c.mu.Lock()
	first := c.events[0]
	c.mu.Unlock()
	if !strings.HasPrefix(first.Message, "listening for ARP") || !strings.Contains(first.Message, "takes over when the sweep finishes") {
		t.Fatalf("the opening message should explain the hand-over and still read as listening: %q", first.Message)
	}
	cancel()
	<-done
}

func TestSweepSaysWhenItIsSweeping(t *testing.T) {
	conn := newFakeConn(nil)
	opts := fastOptions(conn)
	opts.NoRetry = true
	s := New(opts)
	if s.Sweeping() {
		t.Fatal("not sweeping before Run")
	}
	seen := false
	c := &capture{}
	report := func(e engine.ProbeEvent) {
		if e.Kind == engine.KindSent && s.Sweeping() {
			seen = true
		}
		c.report(e)
	}
	if err := s.Run(context.Background(), testIface(t, "10.0.0.0/29"), c.emit, report); err != nil {
		t.Fatal(err)
	}
	if !seen || s.Sweeping() {
		t.Fatalf("Sweeping should be true while asking and false after: during=%v after=%v", seen, s.Sweeping())
	}
}

func TestPassiveListenerRunsUntilCancelled(t *testing.T) {
	conn := newFakeConn(nil)
	c := &capture{}
	l := NewListener(ListenOptions{Open: func(netif.Interface) (Conn, error) { return conn, nil }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, testIface(t, "10.0.0.0/24"), c.emit, c.report) }()

	cam := net.HardwareAddr{0x00, 0x01, 0x4a, 0x00, 0x00, 0x01}
	conn.frames <- Request(cam, net.IPv4(169, 254, 37, 12), net.IPv4(169, 254, 37, 12))
	conn.frames <- Request(net.HardwareAddr{0xb8, 0x27, 0xeb, 0, 0, 1}, net.IPv4(10, 0, 0, 9), net.IPv4(10, 0, 0, 1))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := c.observation(cam.String(), model.FieldIP); ok && c.count(engine.KindProgress) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := c.observation(cam.String(), model.FieldIP); !ok {
		t.Fatal("listener did not record the overheard frame")
	}
	c.mu.Lock()
	var progress []engine.ProbeEvent
	for _, e := range c.events {
		if e.Kind == engine.KindProgress {
			progress = append(progress, e)
		}
	}
	c.mu.Unlock()
	if len(progress) < 2 || progress[len(progress)-1].Message != "listening" || progress[len(progress)-1].Total != 0 || progress[len(progress)-1].Done != len(progress) {
		t.Fatalf("progress should count frames heard with no total: %+v", progress)
	}
	if c.count(engine.KindSent) != 0 || len(conn.requests()) != 0 {
		t.Fatal("the listener must send nothing")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	last := c.events[len(c.events)-1]
	if last.Kind != engine.KindInfo || !strings.Contains(last.Message, "stopped listening after 2 ARP frames") {
		t.Fatalf("final event = %+v", last)
	}
	if err := NewListener(ListenOptions{}).Run(context.Background(), netif.Interface{Name: "utun0"}, c.emit, c.report); err == nil {
		t.Fatal("interface without MAC/subnet should be rejected")
	}
}

func TestSweepRefusesHugeSubnetAndBadInterface(t *testing.T) {
	c := &capture{}
	s := New(fastOptions(newFakeConn(nil)))
	err := s.Run(context.Background(), testIface(t, "10.0.0.0/16"), c.emit, c.report)
	if err == nil || !strings.Contains(err.Error(), "limit of 1022") {
		t.Fatalf("err = %v", err)
	}
	if err := s.Run(context.Background(), netif.Interface{Name: "utun0"}, c.emit, c.report); err == nil {
		t.Fatal("interface without MAC/subnet should be rejected")
	}
}

func TestSweepReportsOpenFailure(t *testing.T) {
	opts := fastOptions(nil)
	opts.Open = func(netif.Interface) (Conn, error) { return nil, errors.New("open /dev/bpf0: permission denied") }
	c := &capture{}
	err := New(opts).Run(context.Background(), testIface(t, "10.0.0.0/29"), c.emit, c.report)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v", err)
	}
	if len(c.obs) != 0 {
		t.Fatal("nothing should be emitted when the socket cannot be opened")
	}
}

func TestSweepStopsOnCancel(t *testing.T) {
	conn := newFakeConn(nil)
	opts := fastOptions(conn)
	opts.Rate = 50
	ctx, cancel := context.WithCancel(context.Background())
	c := &capture{}
	done := make(chan error, 1)
	go func() { done <- New(opts).Run(ctx, testIface(t, "10.0.0.0/24"), c.emit, c.report) }()
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if n := len(conn.requests()); n == 0 || n >= 253 {
		t.Fatalf("sent %d requests before cancel; expected a partial sweep", n)
	}
	select {
	case <-conn.closed:
	default:
		t.Fatal("connection was not closed")
	}
}

func cidr(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSweepAlsoAsksExtraRangesWithProbes(t *testing.T) {
	stale := net.HardwareAddr{0x00, 0x1d, 0xc1, 0x00, 0x00, 0x07}
	local := net.HardwareAddr{0x00, 0x11, 0x32, 0x00, 0x00, 0x01}
	// The fake answers a probe the way Linux does: to our MAC, target 0.0.0.0.
	conn := newFakeConn(map[string]net.HardwareAddr{"10.0.0.1": local, "192.168.1.2": stale})
	c := &capture{}
	opts := fastOptions(conn)
	// /30 = .1 and .2; the /28 overlaps our own /29 and must not repeat it.
	opts.Also = []*net.IPNet{cidr(t, "192.168.1.0/30"), cidr(t, "10.0.0.0/28")}
	if err := New(opts).Run(context.Background(), testIface(t, "10.0.0.0/29"), c.emit, c.report); err != nil {
		t.Fatal(err)
	}

	conn.mu.Lock()
	sent := append([][]byte(nil), conn.sent...)
	conn.mu.Unlock()
	senders := map[string]string{}
	var order []string
	for _, f := range sent {
		p, _ := Decode(f)
		if _, dup := senders[p.TargetIP.String()]; !dup {
			order = append(order, p.TargetIP.String())
		}
		senders[p.TargetIP.String()] = p.SenderIP.String()
	}
	// Subnet first (5), then 192.168.1.1-2, then the /28's hosts outside the /29 (.8-.14).
	want := "10.0.0.1 10.0.0.3 10.0.0.4 10.0.0.5 10.0.0.6 192.168.1.1 192.168.1.2 10.0.0.8 10.0.0.9 10.0.0.10 10.0.0.11 10.0.0.12 10.0.0.13 10.0.0.14"
	if got := strings.Join(order, " "); got != want {
		t.Fatalf("asked %s\nwant  %s", got, want)
	}
	if senders["10.0.0.3"] != "10.0.0.2" || senders["192.168.1.2"] != "0.0.0.0" || senders["10.0.0.9"] != "0.0.0.0" {
		t.Fatalf("subnet requests carry our address, extra-range ones 0.0.0.0: %v", senders)
	}
	// 14 asked, 2 answered: the 12 silent ones are retried once.
	if len(sent) != 14+12 {
		t.Fatalf("sent %d frames, want 14 plus 12 retries", len(sent))
	}

	ip, ok := c.observation(stale.String(), model.FieldIP)
	if !ok || ip.Value != "192.168.1.2" || ip.Confidence != 1 || !strings.Contains(ip.Method, "answering our probe") || !strings.Contains(ip.Method, "outside this interface's subnet 10.0.0.0/29") {
		t.Fatalf("stale device = %+v ok=%v", ip, ok)
	}
	var msgs []string
	for _, e := range c.events {
		msgs = append(msgs, e.Message)
	}
	joined := strings.Join(msgs, "\n")
	for _, w := range []string{
		"also asking 9 addresses in 192.168.1.0/30, 10.0.0.0/28 on this segment",
		"who-has 192.168.1.2 tell 0.0.0.0 (probe, --also 192.168.1.0/30)",
		"192.168.1.2 is-at 00:1d:c1:00:00:07 (answering our probe) · outside our subnet",
		"sweep complete: 2 of 14 addresses answered",
	} {
		if !strings.Contains(joined, w) {
			t.Errorf("events missing %q", w)
		}
	}
}

func TestSweepAlsoRefusesWhatItShouldNotAsk(t *testing.T) {
	c := &capture{}
	for _, tc := range []struct {
		also *net.IPNet
		want string
	}{
		{cidr(t, "169.254.0.0/16"), "169.254.0.0/16 has 65536 addresses, more than the limit of 1022"},
		{cidr(t, "fd00::/120"), "only IPv4 ranges"},
	} {
		opts := fastOptions(newFakeConn(nil))
		opts.Also = []*net.IPNet{tc.also}
		err := New(opts).Run(context.Background(), testIface(t, "10.0.0.0/29"), c.emit, c.report)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("--also %s: err = %v", tc.also, err)
		}
	}
	if len(c.obs) != 0 {
		t.Fatal("nothing should be sent or emitted when a range is refused")
	}
}
