//go:build windows

package netif

import (
	"net"
	"testing"
)

func TestDefaultRouteReadsTheAdapterTable(t *testing.T) {
	adapters, err := readAdapters()
	if err != nil {
		t.Fatalf("the adapter table should be readable on any Windows machine: %v", err)
	}
	if len(adapters) == 0 {
		t.Skip("no IPv4 adapters on this machine")
	}
	gw, name, err := defaultRoute()
	if err != nil {
		t.Skipf("no default gateway on this machine: %v", err)
	}
	if gw.To4() == nil {
		t.Errorf("gateway %v is not IPv4", gw)
	}
	if _, err := net.InterfaceByName(name); err != nil {
		t.Errorf("the default route's adapter %q must be findable by name, as Default looks it up: %v", name, err)
	}
}

func TestNameserversReadsTheAdapterTable(t *testing.T) {
	servers, err := Nameservers()
	if err != nil {
		t.Fatalf("the adapter table should be readable on any Windows machine: %v", err)
	}
	for _, s := range servers {
		if s.Adapter == "" {
			t.Errorf("server %v has no adapter name to show where it came from", s.IP)
		}
		if _, err := net.InterfaceByName(s.Adapter); err != nil {
			t.Errorf("server %v's adapter %q is not a name net.Interfaces knows: %v", s.IP, s.Adapter, err)
		}
	}
}
