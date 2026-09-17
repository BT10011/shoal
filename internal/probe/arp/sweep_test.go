package arp

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
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
	if _, ok := c.observation("01:02:03:04:05:06", model.FieldIP); ok {
		t.Fatal("off-subnet sender must be ignored")
	}
	for _, o := range c.obs {
		if o.DeviceKey == ourMAC.String() && o.Source != "netif" {
			t.Fatalf("our own echoed frame produced an arp observation: %+v", o)
		}
	}
	if c.count(engine.KindReceived) != 2 {
		t.Fatalf("received events = %d, want 2", c.count(engine.KindReceived))
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
