package arp

import (
	"bytes"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHandle stands in for a wpcap handle. Like wpcap, it hands frames out
// of one buffer it reuses, so a caller that keeps the slice sees it change.
type fakeHandle struct {
	mu      sync.Mutex
	frames  [][]byte
	buf     []byte
	sent    [][]byte
	err     error
	closes  int
	busy    bool          // a call is in progress
	blocked chan struct{} // when set, next waits for it to close
	entered chan struct{} // closed when a blocked next has begun
	misuse  string
}

func (h *fakeHandle) enter() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closes > 0 {
		h.misuse = "used after close"
	}
	h.busy = true
}

func (h *fakeHandle) leave() {
	h.mu.Lock()
	h.busy = false
	h.mu.Unlock()
}

func (h *fakeHandle) next() ([]byte, error) {
	h.enter()
	defer h.leave()
	if h.blocked != nil {
		close(h.entered)
		<-h.blocked
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return nil, h.err
	}
	if len(h.frames) == 0 {
		return nil, nil
	}
	h.buf = append(h.buf[:0], h.frames[0]...)
	h.frames = h.frames[1:]
	return h.buf, nil
}

func (h *fakeHandle) send(frame []byte) error {
	h.enter()
	defer h.leave()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, append([]byte(nil), frame...))
	return nil
}

func (h *fakeHandle) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.busy {
		h.misuse = "closed while a call was using it"
	}
	h.closes++
}

// theirMAC is another device on the segment; ourMAC is in sweep_test.go.
var theirMAC = net.HardwareAddr{0x00, 0x00, 0x5e, 0x00, 0x53, 0x20}

// frameFrom is a minimal Ethernet frame from src; only the header matters.
func frameFrom(src net.HardwareAddr, fill byte) []byte {
	f := make([]byte, 42)
	copy(f[0:6], net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(f[6:12], src)
	f[12], f[13] = 0x08, 0x06
	for i := 14; i < len(f); i++ {
		f[i] = fill
	}
	return f
}

func TestPcapConnDropsOurOwnFramesAndCopiesTheRest(t *testing.T) {
	// Npcap hands what the sending handle sent to the receiving one, so
	// the sweep's own requests arrive as if overheard.
	recv := &fakeHandle{frames: [][]byte{frameFrom(ourMAC, 1), frameFrom(theirMAC, 2), frameFrom(theirMAC, 3)}}
	c := newPcapConn(recv, &fakeHandle{}, ourMAC)

	if f, err := c.ReadFrame(); f != nil || err != nil {
		t.Fatalf("our own frame came back as (%x, %v), want (nil, nil)", f, err)
	}
	first, err := c.ReadFrame()
	if err != nil || first == nil {
		t.Fatalf("a frame from another device: (%x, %v)", first, err)
	}
	second, _ := c.ReadFrame()
	if first[14] != 2 || second[14] != 3 {
		t.Errorf("frames read %d then %d, want 2 then 3: a frame must be copied out of the handle's buffer before the next read reuses it", first[14], second[14])
	}
}

func TestPcapConnKeepsEveryFrameWhenItsOwnMACIsUnknown(t *testing.T) {
	recv := &fakeHandle{frames: [][]byte{frameFrom(ourMAC, 1)}}
	c := newPcapConn(recv, &fakeHandle{}, nil)
	if f, _ := c.ReadFrame(); f == nil {
		t.Error("with no MAC to compare against, nothing can be called ours")
	}
}

func TestPcapConnReportsATimeoutAsNothingAndPassesErrorsOn(t *testing.T) {
	recv := &fakeHandle{}
	c := newPcapConn(recv, &fakeHandle{}, ourMAC)
	if f, err := c.ReadFrame(); f != nil || err != nil {
		t.Fatalf("a read that timed out gave (%x, %v), want (nil, nil) so the caller polls again", f, err)
	}
	recv.err = errors.New("pcap_next_ex: the adapter went away")
	if _, err := c.ReadFrame(); err == nil || !strings.Contains(err.Error(), "went away") {
		t.Errorf("a failing read gave %v, want the handle's own error", err)
	}
}

func TestPcapConnSendsOnItsOwnHandle(t *testing.T) {
	recv, send := &fakeHandle{}, &fakeHandle{}
	c := newPcapConn(recv, send, ourMAC)
	frame := frameFrom(ourMAC, 7)
	if err := c.WriteFrame(frame); err != nil {
		t.Fatal(err)
	}
	if len(send.sent) != 1 || !bytes.Equal(send.sent[0], frame) || len(recv.sent) != 0 {
		t.Errorf("sent %d frames on the send handle and %d on the receive handle, want 1 and 0", len(send.sent), len(recv.sent))
	}
}

func TestPcapConnNeverClosesAHandleUnderARead(t *testing.T) {
	recv := &fakeHandle{blocked: make(chan struct{}), entered: make(chan struct{})}
	send := &fakeHandle{}
	c := newPcapConn(recv, send, ourMAC)

	read := make(chan struct{})
	go func() {
		c.ReadFrame()
		close(read)
	}()
	<-recv.entered // the read is now inside the handle

	closed := make(chan struct{})
	go func() {
		c.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a read was still inside the handle")
	case <-time.After(50 * time.Millisecond):
	}
	close(recv.blocked) // the read times out, as wpcap's does every 100 ms
	<-read
	<-closed

	for name, h := range map[string]*fakeHandle{"receive": recv, "send": send} {
		if h.misuse != "" {
			t.Errorf("the %s handle was %s", name, h.misuse)
		}
		if h.closes != 1 {
			t.Errorf("the %s handle was closed %d times, want once", name, h.closes)
		}
	}
	if _, err := c.ReadFrame(); err == nil {
		t.Error("ReadFrame after Close must fail, so the listener's loop ends")
	}
	if err := c.WriteFrame(frameFrom(ourMAC, 0)); err == nil {
		t.Error("WriteFrame after Close must fail")
	}
	c.Close()
	if recv.closes != 1 || send.closes != 1 {
		t.Error("a second Close must not close the handles again")
	}
}

func TestNpcapDeviceName(t *testing.T) {
	const guid = "{00000000-0000-0000-0000-00005E005301}"
	if got := npfDevice(guid); got != `\Device\NPF_{00000000-0000-0000-0000-00005E005301}` {
		t.Errorf("npfDevice = %q", got)
	}
}

func TestNpcapRefusalsExplainThemselves(t *testing.T) {
	for _, e := range []*AccessError{ErrNoNpcap, ErrNpcapAdminOnly} {
		if !errors.Is(e, ErrPermission) {
			t.Errorf("%q does not match ErrPermission, so shoal would not offer to set up access", e.Reason)
		}
		var ae *AccessError
		if !errors.As(error(e), &ae) || ae.Reason == "" {
			t.Errorf("%q: the mode line needs its reason", e.Reason)
		}
		if len(e.Reason) > 32 {
			t.Errorf("reason %q is too long for the mode line", e.Reason)
		}
		for _, want := range []string{npcapURL, "Restrict Npcap driver's access to Administrators only", "shoal --setup", "start shoal again"} {
			if !strings.Contains(e.Advice, want) {
				t.Errorf("advice for %q does not mention %q:\n%s", e.Reason, want, e.Advice)
			}
		}
	}
	if !strings.Contains(ErrNoNpcap.Advice, "not installed") || !strings.Contains(ErrNpcapAdminOnly.Advice, "ticked") {
		t.Error("each refusal must say which of the two cases it is")
	}
}
