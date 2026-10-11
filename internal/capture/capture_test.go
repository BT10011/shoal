package capture

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BT10011/shoal/internal/engine"
	"github.com/BT10011/shoal/internal/netif"
)

var (
	iface = netif.Interface{Name: "eth0", IP: net.IPv4(172, 16, 10, 5).To4()}
	at    = time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)
)

func newTap(t *testing.T) *Tap {
	t.Helper()
	tap := New("v2.0.0-test")
	tap.dir = t.TempDir()
	tap.now = func() time.Time { return at }
	return tap
}

// whoHas is an ARP request for 172.16.10.20 from 00:00:5e:00:53:01.
var whoHas = []byte{
	0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x00, 0x00, 0x5e, 0x00, 0x53, 0x01, 0x08, 0x06,
	0x00, 0x01, 0x08, 0x00, 0x06, 0x04, 0x00, 0x01,
	0x00, 0x00, 0x5e, 0x00, 0x53, 0x01, 172, 16, 10, 5,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 172, 16, 10, 20,
}

func events() []engine.ProbeEvent {
	return []engine.ProbeEvent{
		{Probe: "arp", Kind: engine.KindSent, Message: "who-has 172.16.10.20 tell 172.16.10.5", At: at,
			Packet: &engine.Packet{Layer: engine.LayerEthernet, Data: whoHas}},
		{Probe: "arp", Kind: engine.KindProgress, Message: "1/254", At: at}, // no packet: not written
		{Probe: "dns-sd", Kind: engine.KindReceived, Message: "an mDNS question for local", At: at.Add(time.Millisecond),
			Packet: &engine.Packet{Layer: engine.LayerUDP, Overheard: true,
				Src: netip.MustParseAddrPort("172.16.10.30:5353"), Dst: netip.MustParseAddrPort("224.0.0.251:5353"),
				Data: []byte("\x00\x00\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x05local\x00\x00\x0c\x00\x01")}},
		{Probe: "icmp", Kind: engine.KindSent, Message: "echo request seq 1", At: at.Add(2 * time.Millisecond),
			Packet: &engine.Packet{Layer: engine.LayerICMP,
				// A socket bound to 0.0.0.0 reports no address of its own,
				// and the peer comes back in its IPv6-mapped form.
				Src: netip.AddrPort{}, Dst: netip.MustParseAddrPort("[::ffff:172.16.10.20]:0"),
				Data: []byte{8, 0, 0xf7, 0xfd, 0, 1, 0, 1}}},
	}
}

func TestNothingIsWrittenUnlessRecording(t *testing.T) {
	tap := newTap(t)
	for _, ev := range events() {
		tap.Observe(ev)
	}
	if entries, _ := os.ReadDir(tap.dir); len(entries) != 0 {
		t.Fatalf("files written without being asked: %v", entries)
	}
	if _, _, on := tap.Recording(); on {
		t.Fatal("not recording")
	}
	if path, n, err := tap.Stop(); path != "" || n != 0 || err != nil {
		t.Fatalf("stopping while idle does nothing: %q %d %v", path, n, err)
	}
}

func TestRecordingWritesEachPacketWithShoalsExplanation(t *testing.T) {
	tap := newTap(t)
	path, err := tap.Start(iface)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(tap.dir, "shoal-20261011-093000.pcapng"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the capture lists every device on the network, so only its owner may read it: %v %v", fi.Mode(), err)
	}
	if _, err := tap.Start(iface); err != ErrRecording {
		t.Errorf("a second Start = %v, want ErrRecording", err)
	}
	for _, ev := range events() {
		tap.Observe(ev)
	}
	if _, n, on := tap.Recording(); !on || n != 3 {
		t.Errorf("recording = %v with %d packets, want 3", on, n)
	}
	got, n, err := tap.Stop()
	if err != nil || got != path || n != 3 {
		t.Fatalf("Stop = %q %d %v", got, n, err)
	}
	tap.Observe(events()[0])
	if _, n2, _ := tap.Recording(); n2 != 0 {
		t.Error("nothing is written after Stop")
	}

	tshark, err := exec.LookPath("tshark")
	if err != nil {
		t.Skip("tshark is not installed; the file's structure is tested in internal/pcapng")
	}
	out, err := exec.Command(tshark, "-r", path, "-o", "ip.check_checksum:TRUE", "-o", "udp.check_checksum:TRUE",
		"-T", "fields", "-E", "separator=|",
		"-e", "frame.interface_name", "-e", "frame.comment", "-e", "frame.p2p_dir", "-e", "ip.dst", "-e", "icmp.type").CombinedOutput()
	if err != nil {
		t.Fatalf("tshark: %v\n%s", err, out)
	}
	// frame.p2p_dir is Wireshark's direction: 0 sent, 1 received.
	want := []string{
		"eth0|shoal arp: who-has 172.16.10.20 tell 172.16.10.5|0||",
		"eth0 (rebuilt)|shoal dns-sd, overheard: an mDNS question for local|1|224.0.0.251|",
		"eth0 (rebuilt)|shoal icmp: echo request seq 1|0|172.16.10.20|8",
	}
	if lines := strings.Split(strings.TrimSpace(string(out)), "\n"); strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("tshark read:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestStartNeverOverwritesAndStartFileDoes(t *testing.T) {
	tap := newTap(t)
	first, _ := tap.Start(iface)
	tap.Stop()
	second, err := tap.Start(iface)
	if err != nil || second == first || !strings.HasSuffix(second, "-2.pcapng") {
		t.Fatalf("a second capture in the same second = %q %v, want a -2 file beside %q", second, err, first)
	}
	tap.Stop()

	named := filepath.Join(tap.dir, "scan.pcapng")
	os.WriteFile(named, []byte("old"), 0o644)
	if _, err := tap.StartFile(named, iface); err != nil {
		t.Fatal(err)
	}
	tap.Stop()
	if b, _ := os.ReadFile(named); string(b) == "old" || len(b) < 28 {
		t.Error("--pcap names its file, so it is replaced")
	}
}

func TestPacketsThatCannotBeRebuiltAreCounted(t *testing.T) {
	tap := newTap(t)
	tap.Start(iface)
	tap.Observe(engine.ProbeEvent{Probe: "mdns", Kind: engine.KindReceived, Packet: &engine.Packet{Layer: engine.LayerUDP,
		Src: netip.MustParseAddrPort("[2001:db8::1]:5353"), Dst: netip.MustParseAddrPort("[ff02::fb]:5353"), Data: []byte{0}}})
	_, n, err := tap.Stop()
	if n != 0 || err == nil || !strings.Contains(err.Error(), "1 packets could not be written") {
		t.Errorf("Stop = %d %v", n, err)
	}
}

func TestObserveIsSafeFromEveryProbeAtOnce(t *testing.T) {
	tap := newTap(t)
	tap.Start(iface)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				for _, ev := range events() {
					tap.Observe(ev)
				}
			}
		}()
	}
	wg.Wait()
	if _, n, err := tap.Stop(); n != 8*50*3 || err != nil {
		t.Errorf("wrote %d packets (%v), want %d", n, err, 8*50*3)
	}
}
