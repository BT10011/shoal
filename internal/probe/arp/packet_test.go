package arp

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
)

func loadHex(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var clean strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		clean.WriteString(strings.ReplaceAll(line, " ", ""))
	}
	b, err := hex.DecodeString(clean.String())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var (
	macA = net.HardwareAddr{0x3c, 0x22, 0xfb, 0x9e, 0x01, 0x77}
	macB = net.HardwareAddr{0x00, 0x11, 0x32, 0x7f, 0xa2, 0xc4}
	ipA  = net.IPv4(192, 168, 1, 42).To4()
	ipB  = net.IPv4(192, 168, 1, 20).To4()
)

func TestRequestMatchesFixture(t *testing.T) {
	want := loadHex(t, "request.hex")
	got := Request(macA, ipA, ipB)
	if !bytes.Equal(got, want) {
		t.Fatalf("Request =\n%s\nwant\n%s", hex.Dump(got), hex.Dump(want))
	}
	if len(got) != 60 {
		t.Fatalf("frame is %d bytes, want 60 (Ethernet minimum)", len(got))
	}
}

func TestReplyMatchesFixture(t *testing.T) {
	want := loadHex(t, "reply.hex")
	got := Reply(macB, ipB, macA, ipA)
	if !bytes.Equal(got, want) {
		t.Fatalf("Reply =\n%s\nwant\n%s", hex.Dump(got), hex.Dump(want))
	}
}

func TestDecodeFixtures(t *testing.T) {
	req, err := Decode(loadHex(t, "request.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if !req.IsRequest() || !bytes.Equal(req.EthDst, broadcast) || !bytes.Equal(req.SenderMAC, macA) ||
		!req.SenderIP.Equal(ipA) || !bytes.Equal(req.TargetMAC, zeroMAC) || !req.TargetIP.Equal(ipB) {
		t.Fatalf("request decoded as %+v", req)
	}
	rep, err := Decode(loadHex(t, "reply.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.IsReply() || !bytes.Equal(rep.EthSrc, macB) || !bytes.Equal(rep.SenderMAC, macB) ||
		!rep.SenderIP.Equal(ipB) || !bytes.Equal(rep.TargetMAC, macA) || !rep.TargetIP.Equal(ipA) {
		t.Fatalf("reply decoded as %+v", rep)
	}
}

func TestDecodeDoesNotAliasInput(t *testing.T) {
	frame := loadHex(t, "reply.hex")
	p, _ := Decode(frame)
	frame[6] = 0xee
	if p.EthSrc[0] == 0xee {
		t.Fatal("decoded packet shares memory with the frame")
	}
}

func TestDecodeRejectsNonARP(t *testing.T) {
	frame := loadHex(t, "request.hex")
	ipv4 := append([]byte(nil), frame...)
	ipv4[12], ipv4[13] = 0x08, 0x00
	for name, f := range map[string][]byte{
		"short":     frame[:20],
		"ethertype": ipv4,
		"hw type":   func() []byte { c := append([]byte(nil), frame...); c[15] = 6; return c }(),
	} {
		if _, err := Decode(f); !errors.Is(err, ErrNotARP) {
			t.Errorf("%s: err = %v, want ErrNotARP", name, err)
		}
	}
}
