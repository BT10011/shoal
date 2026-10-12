package icmp

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	xicmp "golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// echoOutcome is how one echo request through an operating system's echo
// API ended.
type echoOutcome int

const (
	echoTimedOut    echoOutcome = iota // no answer before the timeout
	echoReplied                        // an echo reply came back
	echoUnreachable                    // a router said the destination is unreachable
)

// echoResult is what the echo API said about one request.
type echoResult struct {
	Outcome echoOutcome
	From    net.IP // who answered: the device, or the router reporting it unreachable
	Data    []byte // the payload the reply echoed
}

// echoFunc sends one echo request carrying data to dst and waits up to
// timeout for the outcome. Windows' IcmpSendEcho is one.
type echoFunc func(dst net.IP, data []byte, timeout time.Duration) echoResult

// prepareFunc readies one request and returns the call that sends it. Any
// setup, such as opening a handle, happens here, before the probe starts
// timing the round trip.
type prepareFunc func() (echoFunc, error)

// echoConn presents an echo API as a net.PacketConn, so the probe's
// matching and timing code runs unchanged over it. Each WriteTo sends one
// request in its own goroutine; an answer is rebuilt into the ICMP message
// a socket would have read and queued for ReadFrom. The probe times the
// round trip itself, from the write to the read, which keeps microsecond
// resolution where the API reports whole milliseconds.
type echoConn struct {
	prepare prepareFunc
	replies chan echoAnswer
	done    chan struct{}
	close   sync.Once

	mu            sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
}

type echoAnswer struct {
	wire []byte
	from net.IP
}

// defaultEchoTimeout bounds a request written with no deadline set.
const defaultEchoTimeout = time.Second

func newEchoConn(prepare prepareFunc) *echoConn {
	return &echoConn{prepare: prepare, replies: make(chan echoAnswer, 16), done: make(chan struct{})}
}

// WriteTo sends the echo request b, as built by Echo, to addr.
func (c *echoConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	select {
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}
	msg, err := xicmp.ParseMessage(protocolICMP, b)
	if err != nil {
		return 0, err
	}
	req, ok := msg.Body.(*xicmp.Echo)
	if !ok || msg.Type != ipv4.ICMPTypeEcho {
		return 0, errors.New("only echo requests can be sent through the echo API")
	}
	var dst net.IP
	switch a := addr.(type) {
	case *net.IPAddr:
		dst = a.IP.To4()
	case *net.UDPAddr:
		dst = a.IP.To4()
	}
	if dst == nil {
		return 0, fmt.Errorf("%v is not an IPv4 address", addr)
	}
	c.mu.Lock()
	timeout := defaultEchoTimeout
	if !c.writeDeadline.IsZero() {
		timeout = max(time.Until(c.writeDeadline), time.Millisecond)
	}
	c.mu.Unlock()

	send, err := c.prepare()
	if err != nil {
		return 0, err
	}
	data := append([]byte(nil), req.Data...)
	go func() {
		r := send(dst, data, timeout)
		var answer xicmp.Message
		switch r.Outcome {
		case echoReplied:
			answer = xicmp.Message{Type: ipv4.ICMPTypeEchoReply, Body: &xicmp.Echo{ID: req.ID, Seq: req.Seq, Data: r.Data}}
		case echoUnreachable:
			answer = xicmp.Message{Type: ipv4.ICMPTypeDestinationUnreachable, Code: 1, Body: &xicmp.DstUnreach{Data: b[:8]}}
		default:
			return // silence, as a socket would read nothing
		}
		wire, err := answer.Marshal(nil)
		if err != nil {
			return
		}
		select {
		case c.replies <- echoAnswer{wire: wire, from: r.From}:
		case <-c.done:
		}
	}()
	return len(b), nil
}

// ReadFrom returns the next rebuilt answer, or a timeout error once the read
// deadline passes, as a socket would.
func (c *echoConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.mu.Lock()
	deadline := c.readDeadline
	c.mu.Unlock()
	var expired <-chan time.Time
	if !deadline.IsZero() {
		wait := time.Until(deadline)
		if wait <= 0 {
			return 0, nil, os.ErrDeadlineExceeded
		}
		t := time.NewTimer(wait)
		defer t.Stop()
		expired = t.C
	}
	select {
	case a := <-c.replies:
		return copy(p, a.wire), &net.IPAddr{IP: a.from}, nil
	case <-expired:
		return 0, nil, os.ErrDeadlineExceeded
	case <-c.done:
		return 0, nil, net.ErrClosed
	}
}

// Close stops delivering answers. A request still waiting in the API ends
// on its own timeout and its answer is dropped.
func (c *echoConn) Close() error {
	c.close.Do(func() { close(c.done) })
	return nil
}

// LocalAddr is unspecified: the API chooses the source address.
func (c *echoConn) LocalAddr() net.Addr { return &net.IPAddr{IP: net.IPv4zero} }

func (c *echoConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline, c.writeDeadline = t, t
	return nil
}

func (c *echoConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	return nil
}

// SetWriteDeadline also bounds how long the API waits for each request's
// answer, since the request is sent and answered in one call.
func (c *echoConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = t
	return nil
}
