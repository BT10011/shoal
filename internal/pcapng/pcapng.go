// Package pcapng writes packet captures in the pcapng format, so a scan
// can be opened in Wireshark next to shoal's own reading of it.
//
// Only what shoal needs is written: one section, an interface per link
// type, and enhanced packet blocks carrying a direction and a comment.
// The comment is the point: it is shoal's explanation of the packet, which
// Wireshark shows in the packet details and can filter on (frame.comment).
//
// The format is defined in draft-ietf-opsawg-pcapng. Blocks are written in
// little-endian order; the byte-order magic in the section header tells a
// reader so.
package pcapng

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// LinkType says what the bytes of a packet start with.
type LinkType uint16

const (
	// LinkEthernet is a whole Ethernet frame, as ARP is sent and heard.
	LinkEthernet LinkType = 1
	// LinkRaw is an IPv4 packet with no link-layer header, which is what
	// the socket probes are written as: the kernel builds their frames, so
	// shoal never sees one.
	LinkRaw LinkType = 101
)

// Direction is whether a packet was sent or received.
type Direction int

const (
	Unknown Direction = iota
	Inbound
	Outbound
)

// Packet is one packet to write.
type Packet struct {
	Interface int // as returned by AddInterface
	Time      time.Time
	Data      []byte
	Direction Direction
	Comment   string // shoal's explanation; empty for none
}

const (
	blockSection   = 0x0A0D0D0A
	blockInterface = 0x00000001
	blockEnhanced  = 0x00000006
	byteOrderMagic = 0x1A2B3C4D

	optEnd      = 0
	optComment  = 1
	optUserAppl = 4 // section header: the application that wrote it
	optIfName   = 2
	optIfDesc   = 3
	optEPBFlags = 2

	maxOption = 0xFFFC // largest option value, a multiple of 4 under 65,536

	// snapLen is what every interface declares as its largest packet. No
	// packet shoal handles comes near it.
	snapLen = 262144
)

// Writer writes one pcapng section. It is not safe for concurrent use.
type Writer struct {
	w      io.Writer
	ifaces []LinkType
	buf    []byte
}

// NewWriter writes the section header, naming the application that wrote
// the file (shown by capinfos and Wireshark's capture file properties).
func NewWriter(w io.Writer, application string) (*Writer, error) {
	pw := &Writer{w: w}
	var body []byte
	body = binary.LittleEndian.AppendUint32(body, byteOrderMagic)
	body = binary.LittleEndian.AppendUint16(body, 1) // major version
	body = binary.LittleEndian.AppendUint16(body, 0) // minor version
	body = binary.LittleEndian.AppendUint64(body, ^uint64(0))
	body = appendOption(body, optUserAppl, []byte(application))
	body = appendEnd(body)
	return pw, pw.block(blockSection, body)
}

// AddInterface declares an interface the following packets can belong to,
// and returns its number. The description is where shoal says how the
// interface's packets were obtained.
func (w *Writer) AddInterface(link LinkType, name, description string) (int, error) {
	var body []byte
	body = binary.LittleEndian.AppendUint16(body, uint16(link))
	body = binary.LittleEndian.AppendUint16(body, 0) // reserved
	body = binary.LittleEndian.AppendUint32(body, snapLen)
	if name != "" {
		body = appendOption(body, optIfName, []byte(name))
	}
	if description != "" {
		body = appendOption(body, optIfDesc, []byte(description))
	}
	body = appendEnd(body)
	if err := w.block(blockInterface, body); err != nil {
		return 0, err
	}
	w.ifaces = append(w.ifaces, link)
	return len(w.ifaces) - 1, nil
}

// ErrNoInterface is returned for a packet naming an interface never added.
var ErrNoInterface = errors.New("pcapng: packet names an interface that was not added")

// WritePacket writes one enhanced packet block. Timestamps are in
// microseconds, the format's default resolution.
func (w *Writer) WritePacket(p Packet) error {
	if p.Interface < 0 || p.Interface >= len(w.ifaces) {
		return fmt.Errorf("%w (%d)", ErrNoInterface, p.Interface)
	}
	if len(p.Data) > snapLen {
		return fmt.Errorf("pcapng: packet of %d bytes is larger than the snapshot length", len(p.Data))
	}
	us := uint64(p.Time.UnixMicro())
	body := w.buf[:0]
	body = binary.LittleEndian.AppendUint32(body, uint32(p.Interface))
	body = binary.LittleEndian.AppendUint32(body, uint32(us>>32))
	body = binary.LittleEndian.AppendUint32(body, uint32(us))
	body = binary.LittleEndian.AppendUint32(body, uint32(len(p.Data))) // captured
	body = binary.LittleEndian.AppendUint32(body, uint32(len(p.Data))) // on the wire
	body = append(body, p.Data...)
	body = pad(body)
	if p.Direction != Unknown {
		// Bits 0-1 of epb_flags: 01 inbound, 10 outbound.
		body = appendOption(body, optEPBFlags, binary.LittleEndian.AppendUint32(nil, uint32(p.Direction)))
	}
	if p.Comment != "" {
		body = appendOption(body, optComment, []byte(p.Comment))
	}
	if p.Direction != Unknown || p.Comment != "" {
		body = appendEnd(body)
	}
	w.buf = body
	return w.block(blockEnhanced, body)
}

// block writes a block: type, total length, body, total length again.
func (w *Writer) block(kind uint32, body []byte) error {
	total := uint32(12 + len(body))
	var head [8]byte
	binary.LittleEndian.PutUint32(head[0:], kind)
	binary.LittleEndian.PutUint32(head[4:], total)
	var tail [4]byte
	binary.LittleEndian.PutUint32(tail[:], total)
	for _, part := range [][]byte{head[:], body, tail[:]} {
		if _, err := w.w.Write(part); err != nil {
			return err
		}
	}
	return nil
}

// appendOption adds one option: code, length, value padded to 32 bits. A
// value longer than an option can hold is cut short.
func appendOption(b []byte, code uint16, value []byte) []byte {
	if len(value) > maxOption {
		value = value[:maxOption]
	}
	b = binary.LittleEndian.AppendUint16(b, code)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(value)))
	return pad(append(b, value...))
}

func appendEnd(b []byte) []byte { return binary.LittleEndian.AppendUint32(b, optEnd) } // code 0, length 0

func pad(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}
