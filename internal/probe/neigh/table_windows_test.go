//go:build windows

package neigh

import "testing"

func TestTableReadsWindowsNeighbourCache(t *testing.T) {
	entries, err := Table()
	if err != nil {
		t.Fatalf("GetIpNetTable should be readable without privileges: %v", err)
	}
	for _, e := range entries {
		if e.Interface == "" {
			t.Errorf("entry %v has no interface name; the probe filters by it", e.IP)
		}
	}
}
