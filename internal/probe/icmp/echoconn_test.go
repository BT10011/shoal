package icmp

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	xicmp "golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/model"
)

// fakeEcho is an echo API that answers each request after a delay, with the
// outcome the test chooses.
type fakeEcho struct {
	delay   time.Duration
	outcome echoOutcome
	echo    []byte // what the reply echoes; nil echoes the request's data
}

func (f *fakeEcho) send(dst net.IP, data []byte, _ time.Duration) echoResult {
	delay, outcome, echo := f.delay, f.outcome, f.echo
	time.Sleep(delay)
	if echo == nil {
		echo = data
	}
	switch outcome {
	case echoReplied:
		return echoResult{Outcome: echoReplied, From: dst, Data: echo}
	case echoUnreachable:
		return echoResult{Outcome: echoUnreachable, From: net.IPv4(172, 16, 10, 1).To4()}
	}
	return echoResult{}
}

func TestEchoConnRebuildsTheReplyTheAPIReported(t *testing.T) {
	conn := newEchoConn(prepared((&fakeEcho{delay: time.Millisecond, outcome: echoReplied}).send))
	defer conn.Close()
	req, _ := Echo(7, 2)
	if _, err := conn.WriteTo(req, ModeEchoAPI.addr(net.ParseIP(deviceIP))); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1500)
	n, from, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := ParseReply(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Echo || !reply.Mine || reply.Seq != 2 || reply.ID != 7 {
		t.Errorf("reply = %+v, want an echo reply to seq 2, id 7, carrying shoal's payload", reply)
	}
	if host(from) != deviceIP {
		t.Errorf("from = %v, want %s", from, deviceIP)
	}
}

func TestEchoConnReadsNothingWhenTheAPIHeardNothing(t *testing.T) {
	conn := newEchoConn(prepared((&fakeEcho{outcome: echoTimedOut}).send))
	defer conn.Close()
	req, _ := Echo(1, 1)
	conn.WriteTo(req, ModeEchoAPI.addr(net.ParseIP(deviceIP)))
	conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	_, _, err := conn.ReadFrom(make([]byte, 1500))
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("ReadFrom = %v, want a timeout, as a socket gives", err)
	}
}

func TestEchoConnReportsUnreachable(t *testing.T) {
	conn := newEchoConn(prepared((&fakeEcho{outcome: echoUnreachable}).send))
	defer conn.Close()
	req, _ := Echo(1, 1)
	conn.WriteTo(req, ModeEchoAPI.addr(net.ParseIP(deviceIP)))
	conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1500)
	n, from, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := ParseReply(buf[:n]); err != nil || !reply.Unreachable {
		t.Errorf("reply = %+v (%v), want destination unreachable", reply, err)
	}
	if host(from) != "172.16.10.1" {
		t.Errorf("from = %v, want the router that reported it", from)
	}
}

func TestEchoConnGivesTheAPITheTimeLeft(t *testing.T) {
	// Each request is answered in its own goroutine, so the calls are told
	// apart by their payload rather than by the order they arrive in.
	var mu sync.Mutex
	timeouts := map[string]time.Duration{}
	conn := newEchoConn(prepared(func(_ net.IP, data []byte, timeout time.Duration) echoResult {
		mu.Lock()
		defer mu.Unlock()
		timeouts[string(data)] = timeout
		return echoResult{}
	}))
	defer conn.Close()
	write := func(payload string) {
		t.Helper()
		req, err := (&xicmp.Message{Type: ipv4.ICMPTypeEcho, Body: &xicmp.Echo{ID: 1, Seq: 1, Data: []byte(payload)}}).Marshal(nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.WriteTo(req, ModeEchoAPI.addr(net.ParseIP(deviceIP))); err != nil {
			t.Fatal(err)
		}
	}
	write("no deadline")
	conn.SetWriteDeadline(time.Now().Add(300 * time.Millisecond))
	write("300ms left")
	conn.SetWriteDeadline(time.Now().Add(-time.Second))
	write("deadline passed")
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if got := timeouts["no deadline"]; got != defaultEchoTimeout {
		t.Errorf("no deadline: timeout %v, want %v", got, defaultEchoTimeout)
	}
	if got := timeouts["300ms left"]; got <= 200*time.Millisecond || got > 300*time.Millisecond {
		t.Errorf("deadline 300ms away: timeout %v, want what is left of it", got)
	}
	if got := timeouts["deadline passed"]; got != time.Millisecond {
		t.Errorf("deadline passed: timeout %v, want the 1ms floor rather than none", got)
	}
}

func TestEchoConnRefusesWhatIsNotAnEchoRequest(t *testing.T) {
	conn := newEchoConn(prepared((&fakeEcho{}).send))
	defer conn.Close()
	reply := echoBack(mustEcho(t))[0]
	if _, err := conn.WriteTo(reply, ModeEchoAPI.addr(net.ParseIP(deviceIP))); err == nil {
		t.Error("an echo reply was accepted for sending")
	}
	if _, err := conn.WriteTo(mustEcho(t), &net.IPAddr{IP: net.ParseIP("2001:db8::1")}); err == nil {
		t.Error("an IPv6 destination was accepted")
	}
}

func TestEchoConnCloseUnblocksARead(t *testing.T) {
	conn := newEchoConn(prepared((&fakeEcho{}).send))
	done := make(chan error, 1)
	go func() {
		_, _, err := conn.ReadFrom(make([]byte, 1500))
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	conn.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("ReadFrom after Close = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock ReadFrom")
	}
	if _, err := conn.WriteTo(mustEcho(t), ModeEchoAPI.addr(net.ParseIP(deviceIP))); !errors.Is(err, net.ErrClosed) {
		t.Errorf("WriteTo after Close = %v, want net.ErrClosed", err)
	}
}

func TestEnrichOverTheEchoAPI(t *testing.T) {
	f := &fakeEcho{delay: 2 * time.Millisecond, outcome: echoReplied}
	e := New(Options{
		Count: 3, Interval: time.Millisecond, Timeout: 200 * time.Millisecond,
		Listen: func(context.Context) (net.PacketConn, Mode, error) {
			return newEchoConn(prepared(f.send)), ModeEchoAPI, nil
		},
	})
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot("k", deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(rec.obs) != 1 || rec.obs[0].Field != model.FieldLatency {
		t.Fatalf("observations = %+v, want one latency", rec.obs)
	}
	if !strings.Contains(rec.obs[0].Method, "IcmpSendEcho") || !strings.Contains(rec.obs[0].Method, "best of 3") {
		t.Errorf("method = %q, want the echo API named and all three answers", rec.obs[0].Method)
	}
	received := rec.messages(engine.KindReceived)
	if len(received) != 3 {
		t.Fatalf("received %d replies, want 3: %v", len(received), received)
	}
	for _, m := range received {
		if !strings.Contains(m, "shoal rebuilt the reply") {
			t.Errorf("received %q, want it to say the reply was rebuilt, not read off the wire", m)
		}
	}
}

func TestEnrichOverTheEchoAPIIgnoresAReplyWithoutShoalsPayload(t *testing.T) {
	f := &fakeEcho{outcome: echoReplied, echo: []byte("someone else")}
	e := New(Options{
		Count: 1, Interval: time.Millisecond, Timeout: 50 * time.Millisecond,
		Listen: func(context.Context) (net.PacketConn, Mode, error) {
			return newEchoConn(prepared(f.send)), ModeEchoAPI, nil
		},
	})
	var rec recorder
	if err := e.Enrich(context.Background(), snapshot("k", deviceIP), rec.emit, rec.report); err != nil {
		t.Fatal(err)
	}
	if len(rec.obs) != 0 {
		t.Errorf("observations = %+v, want none: the echoed payload was not shoal's", rec.obs)
	}
}

// prepared is a prepareFunc that needs no setup.
func prepared(send echoFunc) prepareFunc {
	return func() (echoFunc, error) { return send, nil }
}

func TestEchoConnFailsAWriteItCannotPrepare(t *testing.T) {
	conn := newEchoConn(func() (echoFunc, error) { return nil, errors.New("no handle") })
	defer conn.Close()
	if _, err := conn.WriteTo(mustEcho(t), ModeEchoAPI.addr(net.ParseIP(deviceIP))); err == nil || !strings.Contains(err.Error(), "no handle") {
		t.Errorf("WriteTo = %v, want the setup's error, so the probe says it could not send", err)
	}
}

func mustEcho(t *testing.T) []byte {
	t.Helper()
	req, err := Echo(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
