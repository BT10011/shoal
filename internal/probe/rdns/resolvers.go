package rdns

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

// ResolvConf is where Unix systems record which DNS servers to ask. macOS
// keeps this file in sync with the resolver configd chose for the primary
// interface, so reading it works on both of shoal's platforms.
const ResolvConf = "/etc/resolv.conf"

// DefaultPort is the DNS service port.
const DefaultPort = "53"

// Resolver is one DNS server shoal may ask, and where shoal learned about it.
// Origin is shown to the user, so it explains the source rather than naming it.
type Resolver struct {
	Addr   string // host:port
	Origin string
}

func (r Resolver) String() string { return r.Addr }

// ParseResolvConf reads "nameserver" lines in file order. Anything else —
// search, domain, options, comments — is ignored: shoal only needs to know
// who to ask.
func ParseResolvConf(r io.Reader) []Resolver {
	var resolvers []Resolver
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if i := strings.IndexAny(text, "#;"); i >= 0 {
			text = text[:i]
		}
		fields := strings.Fields(text)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		ip := net.ParseIP(fields[1])
		if ip == nil {
			continue
		}
		resolvers = append(resolvers, Resolver{
			Addr:   net.JoinHostPort(ip.String(), DefaultPort),
			Origin: fmt.Sprintf("nameserver on line %d of %s", line, ResolvConf),
		})
	}
	return resolvers
}

// SystemResolvers returns the resolvers to ask, in the order the system
// prefers them. When the system names none, the gateway is worth a try: on a
// home network it is usually the resolver as well.
func SystemResolvers(gateway net.IP) []Resolver {
	var resolvers []Resolver
	if f, err := os.Open(ResolvConf); err == nil {
		resolvers = ParseResolvConf(f)
		f.Close()
	}
	if len(resolvers) == 0 && gateway != nil {
		resolvers = append(resolvers, Resolver{
			Addr:   net.JoinHostPort(gateway.String(), DefaultPort),
			Origin: fmt.Sprintf("the default gateway, because %s named no nameserver", ResolvConf),
		})
	}
	return dedupe(resolvers)
}

// dedupe keeps the first mention of each address; resolv.conf may repeat one.
func dedupe(in []Resolver) []Resolver {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, r := range in {
		if _, dup := seen[r.Addr]; dup {
			continue
		}
		seen[r.Addr] = struct{}{}
		out = append(out, r)
	}
	return out
}
