// Package mdns asks devices for their own names over multicast DNS, the
// protocol Apple calls Bonjour. Unlike unicast DNS, there is no server: the
// question goes to 224.0.0.251:5353 and each host answers for itself, which
// is why mDNS knows the names of laptops and printers that the router's DNS
// has never heard of.
package mdns

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Section says where in the message a record appeared. mDNS responders put
// extra records they think will be useful in the additional section, so the
// section is worth showing rather than flattening away.
type Section string

const (
	SectionAnswer     Section = "answer"
	SectionAuthority  Section = "authority"
	SectionAdditional Section = "additional"
)

// SRV is a service's location: which host serves it, on which port.
type SRV struct {
	Target string
	Port   uint16
}

// Record is one resource record, decoded as far as shoal needs it.
type Record struct {
	Section Section
	Name    string // the record's owner name
	Type    dnsmessage.Type
	TTL     time.Duration
	PTR     string   // TypePTR: the name pointed at
	Text    []string // TypeTXT: key=value strings
	Addr    net.IP   // TypeA, TypeAAAA
	SRV     SRV      // TypeSRV
}

// Value renders the record's payload for the event log.
func (r Record) Value() string {
	switch r.Type {
	case dnsmessage.TypePTR:
		return r.PTR
	case dnsmessage.TypeTXT:
		return strings.Join(r.Text, " ")
	case dnsmessage.TypeA, dnsmessage.TypeAAAA:
		return r.Addr.String()
	case dnsmessage.TypeSRV:
		return fmt.Sprintf("%s:%d", r.SRV.Target, r.SRV.Port)
	default:
		return ""
	}
}

// TypeName gives a record type its DNS spelling. The dnsmessage package's
// own String method prints Go identifiers ("TypePTR").
func TypeName(t dnsmessage.Type) string {
	switch t {
	case dnsmessage.TypePTR:
		return "PTR"
	case dnsmessage.TypeTXT:
		return "TXT"
	case dnsmessage.TypeA:
		return "A"
	case dnsmessage.TypeAAAA:
		return "AAAA"
	case dnsmessage.TypeSRV:
		return "SRV"
	case dnsmessage.TypeNS:
		return "NS"
	case dnsmessage.TypeCNAME:
		return "CNAME"
	default:
		return fmt.Sprintf("type %d", t)
	}
}

// Response is a decoded mDNS message.
type Response struct {
	ID            uint16
	Authoritative bool
	Question      string
	Records       []Record
}

// ParseResponse decodes every section of an mDNS message.
//
// shoal decodes the message itself rather than handing it to
// golang.org/x/net/dns/dnsmessage, which rejects outright any name with a
// dot inside a label (golang/go#56246) — and, because Unpack is all or
// nothing, throws away the whole message with it.
//
// That is not an exotic case on a DNS-SD network. RFC 6763 §4.1.1 makes the
// service instance name a single label holding arbitrary UTF-8, and a dot in
// it is ordinary text, not a separator. NDI names its sources
// "<HOSTNAME> (Source Name)", and on a Mac that hostname ends ".LOCAL", so
// every NDI announcement carries a label like
// "STAGE-MBP.LOCAL (Scan Converter)". shoal used to drop each of those
// messages entirely — the address records in the same packet along with
// them — which is why an NDI source could sit on the network announcing
// itself perfectly well and never appear.
//
// Labels are joined with dots and a dot inside one is kept as it is rather
// than escaped as "\.". That keeps names readable on screen and leaves
// ServiceType working, at the cost of a theoretical ambiguity between two
// names that differ only in where a label ends — which nothing shoal does
// depends on.
func ParseResponse(wire []byte) (Response, error) {
	c := &cursor{msg: wire}
	header, err := c.header()
	if err != nil {
		return Response{}, fmt.Errorf("cannot decode %d-byte mDNS message: %w", len(wire), err)
	}
	r := Response{ID: header.id, Authoritative: header.flags&0x0400 != 0}
	for i := 0; i < int(header.counts[0]); i++ {
		name, err := c.name()
		if err != nil {
			return Response{}, fmt.Errorf("cannot decode %d-byte mDNS message: question %d: %w", len(wire), i+1, err)
		}
		if _, err := c.bytes(4); err != nil { // type and class
			return Response{}, fmt.Errorf("cannot decode %d-byte mDNS message: question %d: %w", len(wire), i+1, err)
		}
		if i == 0 {
			r.Question = name
		}
	}
	for i, section := range []Section{SectionAnswer, SectionAuthority, SectionAdditional} {
		for n := 0; n < int(header.counts[i+1]); n++ {
			rec, err := c.resource(section)
			if err != nil {
				return Response{}, fmt.Errorf("cannot decode %d-byte mDNS message: %s record %d: %w", len(wire), section, n+1, err)
			}
			r.Records = append(r.Records, rec)
		}
	}
	return r, nil
}

var (
	errShort    = errors.New("message ends mid-record")
	errBadLabel = errors.New("label length is not a length or a pointer")
	errLoop     = errors.New("compression pointers lead in a circle")
	errLongName = errors.New("name is longer than DNS allows")
)

// maxJumps bounds how many compression pointers one name may follow, which
// is what stops a crafted message looping forever.
const maxJumps = 64

// nameMax is the longest name DNS allows, in presentation form.
const nameMax = 255

// cursor walks a message, remembering how far it has read.
type cursor struct {
	msg []byte
	off int
}

type msgHeader struct {
	id     uint16
	flags  uint16
	counts [4]uint16 // questions, answers, authorities, additionals
}

func (c *cursor) header() (msgHeader, error) {
	b, err := c.bytes(12)
	if err != nil {
		return msgHeader{}, err
	}
	h := msgHeader{id: binary.BigEndian.Uint16(b), flags: binary.BigEndian.Uint16(b[2:])}
	for i := range h.counts {
		h.counts[i] = binary.BigEndian.Uint16(b[4+i*2:])
	}
	return h, nil
}

func (c *cursor) bytes(n int) ([]byte, error) {
	if n < 0 || c.off+n > len(c.msg) {
		return nil, errShort
	}
	b := c.msg[c.off : c.off+n]
	c.off += n
	return b, nil
}

func (c *cursor) uint16() (uint16, error) {
	b, err := c.bytes(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

// name reads one name, following compression pointers. A dot inside a label
// is kept: this is the whole reason shoal does its own decoding.
func (c *cursor) name() (string, error) {
	var b strings.Builder
	off, jumps, jumped := c.off, 0, false
	for {
		if off >= len(c.msg) {
			return "", errShort
		}
		n := int(c.msg[off])
		switch {
		case n == 0:
			off++
			if !jumped {
				c.off = off
			}
			if b.Len() == 0 {
				return ".", nil
			}
			return b.String(), nil
		case n&0xC0 == 0xC0:
			if off+1 >= len(c.msg) {
				return "", errShort
			}
			ptr := int(uint16(n&0x3F)<<8 | uint16(c.msg[off+1]))
			if !jumped {
				c.off, jumped = off+2, true
			}
			if jumps++; jumps > maxJumps {
				return "", errLoop
			}
			if ptr >= len(c.msg) {
				return "", errShort
			}
			off = ptr
		case n&0xC0 == 0:
			off++
			if off+n > len(c.msg) {
				return "", errShort
			}
			if b.Len()+n+1 > nameMax {
				return "", errLongName
			}
			b.Write(c.msg[off : off+n])
			b.WriteByte('.')
			off += n
		default:
			return "", errBadLabel
		}
	}
}

// resource reads one record, and always leaves the cursor at the end of its
// rdata, so a type shoal does not decode costs it nothing.
func (c *cursor) resource(section Section) (Record, error) {
	name, err := c.name()
	if err != nil {
		return Record{}, err
	}
	head, err := c.bytes(8) // type, class, ttl
	if err != nil {
		return Record{}, err
	}
	rdlen, err := c.uint16()
	if err != nil {
		return Record{}, err
	}
	end := c.off + int(rdlen)
	if end > len(c.msg) {
		return Record{}, errShort
	}
	rec := Record{
		Section: section,
		Name:    trimDot(name),
		Type:    dnsmessage.Type(binary.BigEndian.Uint16(head)),
		TTL:     time.Duration(binary.BigEndian.Uint32(head[4:])) * time.Second,
	}
	if err := c.rdata(&rec, end); err != nil {
		return Record{}, err
	}
	c.off = end // whatever the body held, the next record starts here
	return rec, nil
}

func (c *cursor) rdata(rec *Record, end int) error {
	switch rec.Type {
	case dnsmessage.TypePTR:
		target, err := c.name()
		if err != nil {
			return err
		}
		rec.PTR = trimDot(target)
	case dnsmessage.TypeSRV:
		b, err := c.bytes(6) // priority, weight, port
		if err != nil {
			return err
		}
		target, err := c.name()
		if err != nil {
			return err
		}
		rec.SRV = SRV{Target: trimDot(target), Port: binary.BigEndian.Uint16(b[4:])}
	case dnsmessage.TypeA:
		b, err := c.bytes(4)
		if err != nil {
			return err
		}
		rec.Addr = net.IP(append([]byte(nil), b...))
	case dnsmessage.TypeAAAA:
		b, err := c.bytes(16)
		if err != nil {
			return err
		}
		rec.Addr = net.IP(append([]byte(nil), b...))
	case dnsmessage.TypeTXT:
		for c.off < end {
			n, err := c.bytes(1)
			if err != nil {
				return err
			}
			s, err := c.bytes(int(n[0]))
			if err != nil {
				return err
			}
			rec.Text = append(rec.Text, string(s))
		}
	}
	return nil
}

// trimDot drops the root label's trailing dot, which is correct DNS but noise
// on screen.
func trimDot(name string) string { return strings.TrimSuffix(name, ".") }

// PointedAt returns the names the PTR records owned by owner point to, in
// message order.
func (r Response) PointedAt(owner string) []Record {
	var out []Record
	want := trimDot(owner)
	for _, rec := range r.Records {
		if rec.Type == dnsmessage.TypePTR && rec.Name == want && rec.PTR != "" {
			out = append(out, rec)
		}
	}
	return out
}

// Summary describes a response in one line for the event log.
func (r Response) Summary() string {
	if len(r.Records) == 0 {
		return "no records"
	}
	parts := make([]string, 0, len(r.Records))
	for _, rec := range r.Records {
		parts = append(parts, fmt.Sprintf("%s %s %s=%s (TTL %s)", rec.Section, TypeName(rec.Type), rec.Name, rec.Value(), rec.TTL))
	}
	return strings.Join(parts, "; ")
}
