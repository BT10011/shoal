package dnswire

import (
	"encoding/hex"
	"net"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
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

func TestReverseName(t *testing.T) {
	cases := map[string]string{
		"192.168.1.42": "42.1.168.192.in-addr.arpa.",
		"192.168.1.1":  "1.1.168.192.in-addr.arpa.",
		"10.0.0.255":   "255.0.0.10.in-addr.arpa.",
	}
	for in, want := range cases {
		got, err := ReverseName(net.ParseIP(in))
		if err != nil {
			t.Fatalf("ReverseName(%s): %v", in, err)
		}
		if got != want {
			t.Errorf("ReverseName(%s) = %q, want %q", in, got, want)
		}
	}
	if _, err := ReverseName(net.ParseIP("fe80::1")); err == nil {
		t.Error("ReverseName(fe80::1) = nil error, want an unsupported-address error")
	}
}

func TestUnicastQueryMatchesFixture(t *testing.T) {
	want := loadHex(t, "query-unicast.hex")
	got, err := Query(0x1a2b, "20.1.168.192.in-addr.arpa.", QueryOptions{
		Type:             dnsmessage.TypePTR,
		RecursionDesired: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !equal(got, want) {
		t.Fatalf("Query =\n%s\nwant\n%s", hex.Dump(got), hex.Dump(want))
	}
}

// TestMulticastQueryMatchesFixture pins the two differences from a unicast
// question: no recursion-desired bit, and the QU bit set in the class.
func TestMulticastQueryMatchesFixture(t *testing.T) {
	want := loadHex(t, "query-multicast.hex")
	got, err := Query(0x1a2b, "20.1.168.192.in-addr.arpa.", QueryOptions{
		Type:            dnsmessage.TypePTR,
		UnicastResponse: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !equal(got, want) {
		t.Fatalf("Query =\n%s\nwant\n%s", hex.Dump(got), hex.Dump(want))
	}
}

func TestQueryRejectsUnusableName(t *testing.T) {
	if _, err := Query(1, strings.Repeat("x", 300), QueryOptions{Type: dnsmessage.TypePTR}); err == nil {
		t.Error("Query(overlong name) = nil error, want an error")
	}
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
