// Package rdns asks DNS resolvers for a device's name: the reverse (PTR)
// lookup of its IP address. It builds and parses the DNS messages itself,
// and talks to one named resolver at a time, so every answer can say which
// server produced it, how long it took and how long it stays valid.
package rdns

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Record is one PTR answer: the name it gives and how long it may be cached.
type Record struct {
	Name string
	TTL  time.Duration
}

// Reply is what a resolver said, reduced to the parts shoal shows.
type Reply struct {
	ID        uint16
	Question  string
	RCode     dnsmessage.RCode
	Truncated bool
	Records   []Record
	Others    int // answers that were not PTR records (CNAME, RRSIG, ...)
}

// ParseReply decodes a DNS reply and pulls out its PTR answers.
func ParseReply(wire []byte) (Reply, error) {
	var msg dnsmessage.Message
	if err := msg.Unpack(wire); err != nil {
		return Reply{}, fmt.Errorf("rdns: cannot decode %d-byte reply: %w", len(wire), err)
	}
	r := Reply{ID: msg.Header.ID, RCode: msg.Header.RCode, Truncated: msg.Header.Truncated}
	if len(msg.Questions) > 0 {
		r.Question = msg.Questions[0].Name.String()
	}
	for _, a := range msg.Answers {
		ptr, ok := a.Body.(*dnsmessage.PTRResource)
		if !ok {
			r.Others++
			continue
		}
		r.Records = append(r.Records, Record{
			Name: strings.TrimSuffix(ptr.PTR.String(), "."),
			TTL:  time.Duration(a.Header.TTL) * time.Second,
		})
	}
	return r, nil
}

// rcodeNames are the names RFC 1035 and every resolver log use. The
// dnsmessage package's own String method prints Go identifiers
// ("RCodeNameError"), which is not what a user reading the event log expects
// to see next to a dig output.
var rcodeNames = map[dnsmessage.RCode]string{
	dnsmessage.RCodeSuccess:        "NOERROR",
	dnsmessage.RCodeFormatError:    "FORMERR",
	dnsmessage.RCodeServerFailure:  "SERVFAIL",
	dnsmessage.RCodeNameError:      "NXDOMAIN",
	dnsmessage.RCodeNotImplemented: "NOTIMP",
	dnsmessage.RCodeRefused:        "REFUSED",
}

// Status is the reply's response code in its usual DNS spelling.
func (r Reply) Status() string {
	if name, ok := rcodeNames[r.RCode]; ok {
		return name
	}
	return fmt.Sprintf("rcode %d", r.RCode)
}

// Summary describes a reply in one line for the event log.
func (r Reply) Summary() string {
	var b strings.Builder
	b.WriteString(r.Status())
	switch {
	case len(r.Records) == 0:
		b.WriteString(", no PTR answer")
	case len(r.Records) == 1:
		fmt.Fprintf(&b, ", PTR %s (TTL %s)", r.Records[0].Name, r.Records[0].TTL)
	default:
		names := make([]string, len(r.Records))
		for i, rec := range r.Records {
			names[i] = rec.Name
		}
		fmt.Fprintf(&b, ", %d PTR answers: %s", len(names), strings.Join(names, ", "))
	}
	if r.Others > 0 {
		fmt.Fprintf(&b, ", %d non-PTR record(s)", r.Others)
	}
	if r.Truncated {
		b.WriteString(", truncated (TC set)")
	}
	return b.String()
}
