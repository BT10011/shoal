//go:build windows

package netif

import (
	"net"
	"testing"
)

func TestDefaultRouteReadsTheAdapterTable(t *testing.T) {
	adapters, err := adapterRoutes()
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
