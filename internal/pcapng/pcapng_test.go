package pcapng

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// block is one block as read back from a file.
type block struct {
	kind uint32
	body []byte
}

// readBlocks walks a file the way a reader must: every block's leading and
// trailing lengths agree and are a multiple of four.
func readBlocks(t *testing.T, b []byte) []block {
	t.Helper()
	var out []block
	for len(b) > 0 {
		if len(b) < 12 {
			t.Fatalf("%d stray bytes at the end", len(b))
		}
		kind := binary.LittleEndian.Uint32(b)
		total := binary.LittleEndian.Uint32(b[4:])
		if total%4 != 0 || int(total) > len(b) || total < 12 {
			t.Fatalf("block %#x has bad length %d", kind, total)
		}
		if tail := binary.LittleEndian.Uint32(b[total-4:]); tail != total {
			t.Fatalf("block %#x: leading length %d, trailing %d", kind, total, tail)
		}
		out = append(out, block{kind, b[8 : total-4]})
		b = b[total:]
	}
	return out
}

// options decodes an options list into code → values.
func options(t *testing.T, b []byte) map[uint16][]string {
	t.Helper()
	out := map[uint16][]string{}
	for len(b) >= 4 {
		code := binary.LittleEndian.Uint16(b)
		n := int(binary.LittleEndian.Uint16(b[2:]))
		if code == optEnd {
			return out
		}
		padded := (n + 3) &^ 3
		if 4+padded > len(b) {
			t.Fatalf("option %d overruns its block", code)
		}
		out[code] = append(out[code], string(b[4:4+n]))
		b = b[4+padded:]
	}
	t.Fatal("options not ended with opt_endofopt")
	return nil
}

func sample(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, "shoal test")
	if err != nil {
		t.Fatal(err)
	}
	eth, err := w.AddInterface(LinkEthernet, "eth0", "ARP frames as sent and heard")
	if err != nil {
		t.Fatal(err)
	}
	ip, err := w.AddInterface(LinkRaw, "eth0 (rebuilt)", "UDP and ICMP with IPv4 headers rebuilt by shoal")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 11, 9, 30, 0, 123456000, time.UTC)

	// An ARP who-has for 172.16.10.20 from 00:00:5e:00:53:01, 172.16.10.5.
	frame := []byte{
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x00, 0x00, 0x5e, 0x00, 0x53, 0x01, 0x08, 0x06,
		0x00, 0x01, 0x08, 0x00, 0x06, 0x04, 0x00, 0x01,
		0x00, 0x00, 0x5e, 0x00, 0x53, 0x01, 172, 16, 10, 5,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 172, 16, 10, 20,
	}
	if err := w.WritePacket(Packet{Interface: eth, Time: at, Data: frame, Direction: Outbound, Comment: "who-has 172.16.10.20 tell 172.16.10.5"}); err != nil {
		t.Fatal(err)
	}
	dgram, err := UDP(netip.MustParseAddrPort("172.16.10.5:5353"), netip.MustParseAddrPort("224.0.0.251:5353"), []byte("\x00\x00\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x05local\x00\x00\x0c\x00\x01"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WritePacket(Packet{Interface: ip, Time: at.Add(time.Millisecond), Data: dgram, Direction: Inbound, Comment: "overheard: an mDNS question"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WritePacket(Packet{Interface: ip, Time: at, Data: dgram}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWriterProducesWellFormedBlocks(t *testing.T) {
	blocks := readBlocks(t, sample(t))
	if len(blocks) != 6 {
		t.Fatalf("got %d blocks, want section, 2 interfaces, 3 packets", len(blocks))
	}

	shb := blocks[0]
	if shb.kind != blockSection || binary.LittleEndian.Uint32(shb.body) != byteOrderMagic {
		t.Fatalf("first block is not a little-endian section header: %#x", shb.kind)
	}
	if got := options(t, shb.body[16:])[optUserAppl]; len(got) != 1 || got[0] != "shoal test" {
		t.Errorf("application = %q", got)
	}

	for k, want := range []LinkType{LinkEthernet, LinkRaw} {
		idb := blocks[1+k]
		if idb.kind != blockInterface || LinkType(binary.LittleEndian.Uint16(idb.body)) != want {
			t.Errorf("interface %d: kind %#x link %d, want link %d", k, idb.kind, binary.LittleEndian.Uint16(idb.body), want)
		}
		if desc := options(t, idb.body[8:])[optIfDesc]; len(desc) != 1 || desc[0] == "" {
			t.Errorf("interface %d should describe where its packets came from", k)
		}
	}

	arp := blocks[3]
	if arp.kind != blockEnhanced || binary.LittleEndian.Uint32(arp.body) != 0 {
		t.Fatalf("first packet: kind %#x on interface %d", arp.kind, binary.LittleEndian.Uint32(arp.body))
	}
	us := uint64(binary.LittleEndian.Uint32(arp.body[4:]))<<32 | uint64(binary.LittleEndian.Uint32(arp.body[8:]))
	if want := time.Date(2026, 10, 11, 9, 30, 0, 123456000, time.UTC).UnixMicro(); us != uint64(want) {
		t.Errorf("timestamp %d µs, want %d", us, want)
	}
	n := binary.LittleEndian.Uint32(arp.body[12:])
	if n != 42 || binary.LittleEndian.Uint32(arp.body[16:]) != 42 {
		t.Errorf("lengths %d/%d, want 42", n, binary.LittleEndian.Uint32(arp.body[16:]))
	}
	opts := options(t, arp.body[20+44:]) // 42 bytes padded to 44
	if got := opts[optComment]; len(got) != 1 || got[0] != "who-has 172.16.10.20 tell 172.16.10.5" {
		t.Errorf("comment = %q", got)
	}
	if got := opts[optEPBFlags]; len(got) != 1 || binary.LittleEndian.Uint32([]byte(got[0])) != uint32(Outbound) {
		t.Errorf("direction flags = %q, want outbound", got)
	}

	bare := blocks[5]
	if n := binary.LittleEndian.Uint32(bare.body[12:]); len(bare.body) != 20+int((n+3)&^3) {
		t.Errorf("a packet with no comment or direction should carry no options: body %d bytes for %d of data", len(bare.body), n)
	}
}

func TestWritePacketRefusesAnUnknownInterface(t *testing.T) {
	w, _ := NewWriter(&bytes.Buffer{}, "shoal")
	if err := w.WritePacket(Packet{Interface: 0, Data: []byte{1}}); err == nil {
		t.Fatal("no interface has been added yet")
	}
}

func TestLongCommentIsCutToFit(t *testing.T) {
	var buf bytes.Buffer
	w, _ := NewWriter(&buf, "shoal")
	w.AddInterface(LinkRaw, "x", "")
	if err := w.WritePacket(Packet{Data: []byte{0x45}, Comment: strings.Repeat("a", 70000)}); err != nil {
		t.Fatal(err)
	}
	blocks := readBlocks(t, buf.Bytes())
	if got := options(t, blocks[2].body[24:])[optComment]; len(got) != 1 || len(got[0]) != maxOption {
		t.Errorf("comment should be cut to %d bytes", maxOption)
	}
}

func TestChecksumMatchesRFC1071(t *testing.T) {
	// RFC 1071 §3: these bytes sum to 0xddf2, so the checksum is 0x220d.
	if got := checksum(0, []byte{0x00, 0x01, 0xf2, 0x03, 0xf4, 0xf5, 0xf6, 0xf7}); got != 0x220d {
		t.Errorf("checksum = %#04x, want 0x220d", got)
	}
}

func TestRebuiltHeadersVerify(t *testing.T) {
	src, dst := netip.MustParseAddrPort("172.16.10.5:40000"), netip.MustParseAddrPort("192.0.2.53:53")
	b, err := UDP(src, dst, []byte("odd-length payload"))
	if err != nil {
		t.Fatal(err)
	}
	if checksum(0, b[:ipv4Len]) != 0 {
		t.Error("IPv4 header checksum does not verify")
	}
	if got := binary.BigEndian.Uint16(b[2:]); int(got) != len(b) {
		t.Errorf("IPv4 total length %d, packet %d", got, len(b))
	}
	if checksumOver(src.Addr(), dst.Addr(), b[ipv4Len:]) != 0 {
		t.Error("UDP checksum does not verify")
	}

	icmp, err := ICMP(src.Addr(), dst.Addr(), []byte{8, 0, 0, 0, 0, 1, 0, 1})
	if err != nil || icmp[9] != protoICMP || len(icmp) != ipv4Len+8 {
		t.Fatalf("ICMP: %v % x", err, icmp)
	}
	if _, err := UDP(netip.MustParseAddrPort("[2001:db8::1]:1"), dst, nil); err == nil {
		t.Error("IPv6 is not rebuilt")
	}
}

// checksumOver verifies a UDP segment whose checksum is filled in: summed
// with its pseudo-header it comes to zero.
func checksumOver(src, dst netip.Addr, segment []byte) uint16 {
	s, d := src.As4(), dst.As4()
	pseudo := append(append(append([]byte{}, s[:]...), d[:]...), 0, protoUDP, byte(len(segment)>>8), byte(len(segment)))
	return checksum(partial(0, pseudo), segment)
}

// TestWiresharkReadsIt hands the sample to tshark, when it is installed,
// with checksum validation on, and checks what it decodes.
func TestWiresharkReadsIt(t *testing.T) {
	tshark, err := exec.LookPath("tshark")
	if err != nil {
		t.Skip("tshark is not installed")
	}
	path := filepath.Join(t.TempDir(), "sample.pcapng")
	if err := os.WriteFile(path, sample(t), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(tshark, "-r", path, "-o", "ip.check_checksum:TRUE", "-o", "udp.check_checksum:TRUE",
		"-T", "fields", "-E", "separator=|",
		"-e", "frame.comment", "-e", "arp.dst.proto_ipv4", "-e", "ip.checksum.status", "-e", "udp.checksum.status", "-e", "dns.qry.name").CombinedOutput()
	if err != nil {
		t.Fatalf("tshark could not read the file: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		t.Fatalf("tshark decoded %d packets, want 3:\n%s", len(lines), out)
	}
	if want := "who-has 172.16.10.20 tell 172.16.10.5|172.16.10.20|||"; lines[0] != want {
		t.Errorf("ARP packet: %q, want %q", lines[0], want)
	}
	if want := "overheard: an mDNS question||1|1|local"; lines[1] != want {
		t.Errorf("mDNS packet: %q, want %q (checksum status 1 is good)", lines[1], want)
	}
}
