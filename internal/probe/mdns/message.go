// Package mdns asks devices for their own names over multicast DNS, the
// protocol Apple calls Bonjour. Unlike unicast DNS, there is no server: the
// question goes to 224.0.0.251:5353 and each host answers for itself, which
// is why mDNS knows the names of laptops and printers that the router's DNS
// has never heard of.
package mdns

import (
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
func ParseResponse(wire []byte) (Response, error) {
	var msg dnsmessage.Message
	if err := msg.Unpack(wire); err != nil {
		return Response{}, fmt.Errorf("cannot decode %d-byte mDNS message: %w", len(wire), err)
	}
	r := Response{ID: msg.Header.ID, Authoritative: msg.Header.Authoritative}
	if len(msg.Questions) > 0 {
		r.Question = msg.Questions[0].Name.String()
	}
	for _, res := range msg.Answers {
		r.Records = append(r.Records, decode(SectionAnswer, res))
	}
	for _, res := range msg.Authorities {
		r.Records = append(r.Records, decode(SectionAuthority, res))
	}
	for _, res := range msg.Additionals {
		r.Records = append(r.Records, decode(SectionAdditional, res))
	}
	return r, nil
}

func decode(section Section, res dnsmessage.Resource) Record {
	rec := Record{
		Section: section,
		Name:    trimDot(res.Header.Name.String()),
		Type:    res.Header.Type,
		TTL:     time.Duration(res.Header.TTL) * time.Second,
	}
	switch body := res.Body.(type) {
	case *dnsmessage.PTRResource:
		rec.PTR = trimDot(body.PTR.String())
	case *dnsmessage.TXTResource:
		rec.Text = body.TXT
	case *dnsmessage.AResource:
		rec.Addr = net.IP(body.A[:])
	case *dnsmessage.AAAAResource:
		rec.Addr = net.IP(body.AAAA[:])
	case *dnsmessage.SRVResource:
		rec.SRV = SRV{Target: trimDot(body.Target.String()), Port: body.Port}
	}
	return rec
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
