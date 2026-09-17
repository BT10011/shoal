// Package dnswire builds the DNS questions shoal asks. Unicast DNS and
// multicast DNS use the same message format and differ only in who is asked
// and in a couple of header and class bits, so both probes share this much.
// Parsing stays with each probe: rdns wants one PTR answer, mdns wants
// several record types across every section.
package dnswire

import (
	"fmt"
	"net"

	"golang.org/x/net/dns/dnsmessage"
)

// ClassUnicastResponse is the top bit of the class field, which mDNS reuses
// as the "QU" bit: answer me directly instead of multicasting to everyone
// (RFC 6762 §5.4).
const ClassUnicastResponse dnsmessage.Class = 0x8000

// ReverseName returns the in-addr.arpa name that holds an IPv4 address's PTR
// records: 192.168.1.42 becomes "42.1.168.192.in-addr.arpa.".
//
// The octets are reversed because DNS names get more specific from right to
// left, while IPv4 addresses get more specific from left to right.
func ReverseName(ip net.IP) (string, error) {
	v4 := ip.To4()
	if v4 == nil {
		return "", fmt.Errorf("%s is not an IPv4 address (ip6.arpa lookups are not implemented)", ip)
	}
	return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa.", v4[3], v4[2], v4[1], v4[0]), nil
}

// QueryOptions vary the question between the two kinds of DNS.
type QueryOptions struct {
	Type dnsmessage.Type // PTR, SRV, TXT, A, ...

	// RecursionDesired asks a unicast resolver to chase the answer. It is
	// meaningless in mDNS, where every host answers only for itself.
	RecursionDesired bool

	// UnicastResponse sets the QU bit, for mDNS.
	UnicastResponse bool
}

// Query builds a single-question DNS message.
func Query(id uint16, name string, opts QueryOptions) ([]byte, error) {
	qname, err := dnsmessage.NewName(name)
	if err != nil {
		return nil, fmt.Errorf("%q is not a usable DNS name: %w", name, err)
	}
	class := dnsmessage.ClassINET
	if opts.UnicastResponse {
		class |= ClassUnicastResponse
	}
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{ID: id, RecursionDesired: opts.RecursionDesired},
		Questions: []dnsmessage.Question{{
			Name:  qname,
			Type:  opts.Type,
			Class: class,
		}},
	}
	return msg.Pack()
}
