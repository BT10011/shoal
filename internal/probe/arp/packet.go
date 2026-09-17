// Package arp discovers hosts on the local link by asking every address in
// the subnet "who has this IP?" and listening for the replies, and by
// overhearing other hosts' ARP traffic while it does so.
package arp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
)

// Wire-format constants for Ethernet II + ARP over IPv4.
const (
	EtherTypeARP   = 0x0806
	HardwareEther  = 1
	ProtocolIPv4   = 0x0800
	OpRequest      = 1
	OpReply        = 2
	ethHeaderLen   = 14
	arpPacketLen   = 28
	frameLen       = ethHeaderLen + arpPacketLen
	minEthernetLen = 60
)

var (
	broadcast = net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	zeroMAC   = net.HardwareAddr{0, 0, 0, 0, 0, 0}
)

// Packet is a decoded ARP frame.
type Packet struct {
	EthSrc    net.HardwareAddr
	EthDst    net.HardwareAddr
	Op        uint16
	SenderMAC net.HardwareAddr
	SenderIP  net.IP
	TargetMAC net.HardwareAddr
	TargetIP  net.IP
}

// IsRequest reports whether the packet is a who-has.
func (p Packet) IsRequest() bool { return p.Op == OpRequest }

// IsReply reports whether the packet is an is-at.
func (p Packet) IsReply() bool { return p.Op == OpReply }

// Request builds a broadcast who-has frame for targetIP from the given
// interface address, padded to the Ethernet minimum.
func Request(srcMAC net.HardwareAddr, srcIP, targetIP net.IP) []byte {
	return encode(broadcast, srcMAC, OpRequest, srcMAC, srcIP, zeroMAC, targetIP)
}

// Reply builds a unicast is-at frame answering a request.
func Reply(srcMAC net.HardwareAddr, srcIP net.IP, dstMAC net.HardwareAddr, dstIP net.IP) []byte {
	return encode(dstMAC, srcMAC, OpReply, srcMAC, srcIP, dstMAC, dstIP)
}

func encode(ethDst, ethSrc net.HardwareAddr, op uint16, sha net.HardwareAddr, spa net.IP, tha net.HardwareAddr, tpa net.IP) []byte {
	f := make([]byte, minEthernetLen)
	copy(f[0:6], ethDst)
	copy(f[6:12], ethSrc)
	binary.BigEndian.PutUint16(f[12:14], EtherTypeARP)
	a := f[ethHeaderLen:]
	binary.BigEndian.PutUint16(a[0:2], HardwareEther)
	binary.BigEndian.PutUint16(a[2:4], ProtocolIPv4)
	a[4] = 6
	a[5] = 4
	binary.BigEndian.PutUint16(a[6:8], op)
	copy(a[8:14], sha)
	copy(a[14:18], spa.To4())
	copy(a[18:24], tha)
	copy(a[24:28], tpa.To4())
	return f
}

// ErrNotARP is returned by Decode for frames that are not IPv4-over-Ethernet ARP.
var ErrNotARP = errors.New("not an IPv4 ARP frame")

// Decode parses an Ethernet frame carrying ARP. Trailing padding is ignored.
func Decode(frame []byte) (Packet, error) {
	if len(frame) < frameLen {
		return Packet{}, fmt.Errorf("%w: frame is %d bytes, need %d", ErrNotARP, len(frame), frameLen)
	}
	if binary.BigEndian.Uint16(frame[12:14]) != EtherTypeARP {
		return Packet{}, fmt.Errorf("%w: ethertype 0x%04x", ErrNotARP, binary.BigEndian.Uint16(frame[12:14]))
	}
	a := frame[ethHeaderLen:]
	if binary.BigEndian.Uint16(a[0:2]) != HardwareEther || binary.BigEndian.Uint16(a[2:4]) != ProtocolIPv4 || a[4] != 6 || a[5] != 4 {
		return Packet{}, fmt.Errorf("%w: hardware/protocol type not Ethernet/IPv4", ErrNotARP)
	}
	return Packet{
		EthDst:    net.HardwareAddr(append([]byte(nil), frame[0:6]...)),
		EthSrc:    net.HardwareAddr(append([]byte(nil), frame[6:12]...)),
		Op:        binary.BigEndian.Uint16(a[6:8]),
		SenderMAC: net.HardwareAddr(append([]byte(nil), a[8:14]...)),
		SenderIP:  net.IPv4(a[14], a[15], a[16], a[17]).To4(),
		TargetMAC: net.HardwareAddr(append([]byte(nil), a[18:24]...)),
		TargetIP:  net.IPv4(a[24], a[25], a[26], a[27]).To4(),
	}, nil
}
