// Package capture records the packets behind shoal's probe events to a
// pcapng file, so a scan can be followed in Wireshark packet by packet,
// with shoal's own explanation of each one as its comment.
//
// Nothing is recorded or kept unless the user asks: the Tap is subscribed
// to every engine, but does nothing until Start, and writes each packet to
// the file as it arrives, so there is no buffer to grow.
package capture

import (
	"bufio"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/netif"
	"github.com/BT10011/shoal/internal/pcapng"
)

// Tap writes packets to a capture file while recording is on.
type Tap struct {
	version string
	dir     string // where Start names its files; empty is the current directory
	now     func() time.Time

	mu  sync.Mutex
	rec *recording
}

type recording struct {
	path    string
	f       *os.File
	bw      *bufio.Writer
	w       *pcapng.Writer
	self    netip.Addr // this machine's address, for a socket bound to 0.0.0.0
	eth     int        // interface number for whole Ethernet frames
	raw     int        // interface number for packets with rebuilt IPv4 headers
	packets int
	skipped int   // packets that could not be written, such as IPv6
	err     error // the first write error; nothing more is written after it
}

// New creates a Tap. The version is written into each file as the
// application that wrote it.
func New(version string) *Tap {
	return &Tap{version: version, now: time.Now}
}

// ErrRecording is returned by Start while a recording is already running.
var ErrRecording = errors.New("already recording")

// Start begins recording to a new file named for the time, in the current
// directory, and returns its full path.
func (t *Tap) Start(iface netif.Interface) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rec != nil {
		return t.rec.path, ErrRecording
	}
	stem := filepath.Join(t.dir, "shoal-"+t.now().Format("20060102-150405"))
	path := stem + ".pcapng"
	for n := 2; ; n++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) && n < 100 {
			path = fmt.Sprintf("%s-%d.pcapng", stem, n)
			continue
		}
		if err != nil {
			return "", err
		}
		return t.begin(f, iface)
	}
}

// StartFile begins recording to the named file, replacing it if it exists:
// the user named it, as with tcpdump -w.
func (t *Tap) StartFile(path string, iface netif.Interface) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rec != nil {
		return t.rec.path, ErrRecording
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	return t.begin(f, iface)
}

// begin writes the file's header and its two interfaces. Called with mu held.
func (t *Tap) begin(f *os.File, iface netif.Interface) (string, error) {
	path, err := filepath.Abs(f.Name())
	if err != nil {
		path = f.Name()
	}
	r := &recording{path: path, f: f, bw: bufio.NewWriterSize(f, 64<<10)}
	if a, ok := netip.AddrFromSlice(iface.IP.To4()); ok {
		r.self = a
	}
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if r.w, err = pcapng.NewWriter(r.bw, "shoal "+t.version); err != nil {
		return fail(err)
	}
	if r.eth, err = r.w.AddInterface(pcapng.LinkEthernet, iface.Name,
		"Ethernet frames shoal sent and heard on "+iface.Name+": ARP, exactly as on the wire"); err != nil {
		return fail(err)
	}
	if r.raw, err = r.w.AddInterface(pcapng.LinkRaw, iface.Name+" (rebuilt)",
		"UDP and ICMP sent and received through shoal's sockets on "+iface.Name+". The kernel built the real headers, "+
			"so the IPv4 and UDP headers here are rebuilt by shoal from the socket's addresses; TTL, IP ID and options may differ from the wire"); err != nil {
		return fail(err)
	}
	if err := r.bw.Flush(); err != nil {
		return fail(err)
	}
	t.rec = r
	return path, nil
}

// Stop ends the recording and closes the file, returning its path and how
// many packets it holds. Stopping when not recording does nothing.
func (t *Tap) Stop() (path string, packets int, err error) {
	t.mu.Lock()
	r := t.rec
	t.rec = nil
	t.mu.Unlock()
	if r == nil {
		return "", 0, nil
	}
	err = r.err
	if ferr := r.bw.Flush(); err == nil {
		err = ferr
	}
	if cerr := r.f.Close(); err == nil {
		err = cerr
	}
	if err == nil && r.skipped > 0 {
		err = fmt.Errorf("%d packets could not be written (only IPv4 headers are rebuilt)", r.skipped)
	}
	return r.path, r.packets, err
}

// Recording reports whether a recording is running, where, and how many
// packets it has written so far.
func (t *Tap) Recording() (path string, packets int, on bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rec == nil {
		return "", 0, false
	}
	return t.rec.path, t.rec.packets, true
}

// Observe is the engine listener. It writes the event's packet, if it has
// one and a recording is running, before returning: the packet's bytes are
// only valid during the call.
func (t *Tap) Observe(ev engine.ProbeEvent) {
	p := ev.Packet
	if p == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.rec
	if r == nil || r.err != nil {
		return
	}
	pkt := pcapng.Packet{Time: ev.At, Comment: comment(ev)}
	switch ev.Kind {
	case engine.KindSent:
		pkt.Direction = pcapng.Outbound
	case engine.KindReceived:
		pkt.Direction = pcapng.Inbound
	}
	src, dst := r.resolve(p.Src), r.resolve(p.Dst)
	var err error
	switch p.Layer {
	case engine.LayerEthernet:
		pkt.Interface, pkt.Data = r.eth, p.Data
	case engine.LayerUDP:
		pkt.Interface = r.raw
		pkt.Data, err = pcapng.UDP(src, dst, p.Data)
	case engine.LayerICMP:
		pkt.Interface = r.raw
		pkt.Data, err = pcapng.ICMP(src.Addr(), dst.Addr(), p.Data)
	default:
		err = fmt.Errorf("unknown layer %d", p.Layer)
	}
	if err != nil {
		r.skipped++
		return
	}
	if err := r.w.WritePacket(pkt); err != nil {
		r.err = err
		return
	}
	r.packets++
}

// resolve turns a socket address into the one on the wire. Go's sockets
// often give IPv4 addresses in their IPv6-mapped form, and a socket bound to
// 0.0.0.0 reports that (or nothing) as its own address: the kernel picks the
// source per packet, and on the local network that is the interface's.
func (r *recording) resolve(ap netip.AddrPort) netip.AddrPort {
	a := ap.Addr().Unmap()
	if (!a.IsValid() || a.IsUnspecified()) && r.self.IsValid() {
		a = r.self
	}
	return netip.AddrPortFrom(a, ap.Port())
}

// comment is the explanation Wireshark shows with the packet: which probe,
// and what shoal made of it.
func comment(ev engine.ProbeEvent) string {
	if ev.Packet.Overheard {
		return fmt.Sprintf("shoal %s, overheard: %s", ev.Probe, ev.Message)
	}
	return fmt.Sprintf("shoal %s: %s", ev.Probe, ev.Message)
}
