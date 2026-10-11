package pcapng

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// The socket probes hand the kernel a UDP payload or an ICMP message and
// get the same back; the IPv4 and UDP headers around them are the kernel's
// and never reach shoal. To put such a packet in a capture, the headers are
// rebuilt here from the addresses the socket used. They are a faithful
// reconstruction, not a copy of the wire: TTL, identification and options
// may differ from what was actually sent, which is why the interface these
// packets are written under says so.

const (
	protoICMP  = 1
	protoUDP   = 17
	ipv4Len    = 20
	udpLen     = 8
	rebuiltTTL = 64
)

// UDP wraps a UDP payload in rebuilt IPv4 and UDP headers.
func UDP(src, dst netip.AddrPort, payload []byte) ([]byte, error) {
	if !src.Addr().Is4() || !dst.Addr().Is4() {
		return nil, fmt.Errorf("pcapng: UDP from %s to %s: only IPv4 is rebuilt", src, dst)
	}
	n := udpLen + len(payload)
	if ipv4Len+n > 0xFFFF {
		return nil, fmt.Errorf("pcapng: UDP payload of %d bytes is too large", len(payload))
	}
	b := ipv4Header(src.Addr(), dst.Addr(), protoUDP, n)
	b = binary.BigEndian.AppendUint16(b, src.Port())
	b = binary.BigEndian.AppendUint16(b, dst.Port())
	b = binary.BigEndian.AppendUint16(b, uint16(n))
	b = binary.BigEndian.AppendUint16(b, 0) // checksum, filled below
	b = append(b, payload...)
	sum := udpChecksum(src.Addr(), dst.Addr(), b[ipv4Len:])
	binary.BigEndian.PutUint16(b[ipv4Len+6:], sum)
	return b, nil
}

// ICMP wraps an ICMP message in a rebuilt IPv4 header. The message is
// written as given: on a datagram socket the kernel sets the identifier and
// checksum of an echo request itself, so a request shoal sent may show the
// values it handed the kernel rather than the ones that went out.
func ICMP(src, dst netip.Addr, message []byte) ([]byte, error) {
	if !src.Is4() || !dst.Is4() {
		return nil, fmt.Errorf("pcapng: ICMP from %s to %s: only IPv4 is rebuilt", src, dst)
	}
	if ipv4Len+len(message) > 0xFFFF {
		return nil, fmt.Errorf("pcapng: ICMP message of %d bytes is too large", len(message))
	}
	return append(ipv4Header(src, dst, protoICMP, len(message)), message...), nil
}

func ipv4Header(src, dst netip.Addr, proto byte, payload int) []byte {
	b := make([]byte, ipv4Len, ipv4Len+payload)
	b[0] = 0x45 // version 4, 5 words of header
	binary.BigEndian.PutUint16(b[2:], uint16(ipv4Len+payload))
	b[6] = 0x40 // don't fragment
	b[8] = rebuiltTTL
	b[9] = proto
	s, d := src.As4(), dst.As4()
	copy(b[12:16], s[:])
	copy(b[16:20], d[:])
	binary.BigEndian.PutUint16(b[10:], checksum(0, b))
	return b
}

func udpChecksum(src, dst netip.Addr, segment []byte) uint16 {
	s, d := src.As4(), dst.As4()
	pseudo := make([]byte, 0, 12)
	pseudo = append(pseudo, s[:]...)
	pseudo = append(pseudo, d[:]...)
	pseudo = append(pseudo, 0, protoUDP)
	pseudo = binary.BigEndian.AppendUint16(pseudo, uint16(len(segment)))
	sum := checksum(partial(0, pseudo), segment)
	if sum == 0 {
		return 0xFFFF // zero means "no checksum" in UDP
	}
	return sum
}

// checksum is the Internet checksum (RFC 1071) of b, continuing from a
// partial sum.
func checksum(acc uint32, b []byte) uint16 {
	acc = partial(acc, b)
	for acc>>16 != 0 {
		acc = acc&0xFFFF + acc>>16
	}
	return ^uint16(acc)
}

func partial(acc uint32, b []byte) uint32 {
	for i := 0; i+1 < len(b); i += 2 {
		acc += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		acc += uint32(b[len(b)-1]) << 8
	}
	return acc
}
