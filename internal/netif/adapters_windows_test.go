//go:build windows

package netif

import (
	"net"
	"regexp"
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

func TestAdapterGUIDNamesTheDefaultInterface(t *testing.T) {
	iface, err := Default()
	if err != nil {
		t.Skipf("no default interface on this machine: %v", err)
	}
	guid, err := AdapterGUID(iface.Index)
	if err != nil {
		t.Fatalf("the default interface %s (index %d) should have a GUID: %v", iface.Name, iface.Index, err)
	}
	// Npcap's device name is built from it, so it must be the braced form.
	if !regexp.MustCompile(`^\{[0-9A-Fa-f]{8}(-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12}\}$`).MatchString(guid) {
		t.Errorf("GUID %q is not in the braced form Npcap's device names use", guid)
	}
}
