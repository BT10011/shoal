//go:build windows

package rdns

import (
	"strings"
	"testing"
)

func TestSystemResolversComeFromTheAdapters(t *testing.T) {
	got := systemResolvers()
	if len(got) == 0 {
		t.Skip("no DNS server is set on any adapter here")
	}
	for _, r := range got {
		if !strings.HasSuffix(r.Addr, ":"+DefaultPort) {
			t.Errorf("resolver %q, want the DNS port", r.Addr)
		}
		if !strings.Contains(r.Origin, "DNS server set on ") {
			t.Errorf("origin %q, want it to name the adapter the server is set on", r.Origin)
		}
	}
}
