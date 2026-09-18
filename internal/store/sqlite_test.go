package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestHistoryRemembersAcrossVisits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "history.db")
	h, err := OpenHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	venue := NetworkID{Subnet: "192.168.1.0/24", GatewayMAC: "00:00:5e:00:53:01"}
	day1 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	prev, known, err := h.Visit(venue, "192.168.1.1", day1)
	if err != nil || prev.Visits != 0 || len(known) != 0 {
		t.Fatalf("first visit: %+v %v %v", prev, known, err)
	}
	cam := Remembered{MAC: "00:01:4a:00:00:01", IP: "192.168.1.50", Hostname: "cam.local", Vendor: "Sony", FirstSeen: day1, LastSeen: day1.Add(time.Minute), LastSource: "arp"}
	if err := h.Record(venue, cam); err != nil {
		t.Fatal(err)
	}
	// Later in the same visit: a new address, no name yet, seen again.
	if err := h.Record(venue, Remembered{MAC: cam.MAC, IP: "192.168.1.51", FirstSeen: day1.Add(time.Hour), LastSeen: day1.Add(time.Hour), LastSource: "icmp"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		t.Fatalf("history directory should be private: %v %v", info.Mode(), err)
	}

	h, err = OpenHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	day2 := day1.Add(24 * time.Hour)
	prev, known, err = h.Visit(venue, "192.168.1.1", day2)
	if err != nil {
		t.Fatal(err)
	}
	if prev.Visits != 1 || !prev.FirstVisit.Equal(day1) || !prev.LastVisit.Equal(day1) || prev.GatewayIP != "192.168.1.1" {
		t.Fatalf("the network as it stood before this visit: %+v", prev)
	}
	if len(known) != 1 {
		t.Fatalf("known = %+v", known)
	}
	got := known[0]
	if got.IP != "192.168.1.51" || got.Hostname != "cam.local" || got.Vendor != "Sony" || got.LastSource != "icmp" {
		t.Errorf("newest address kept, remembered name not blanked: %+v", got)
	}
	if !got.FirstSeen.Equal(day1) || !got.LastSeen.Equal(day1.Add(time.Hour)) {
		t.Errorf("first seen only moves earlier, last seen only later: %+v", got)
	}
	if prev, _, _ = h.Visit(venue, "192.168.1.1", day2.Add(time.Hour)); prev.Visits != 2 {
		t.Errorf("visits = %d, want 2 before the third", prev.Visits)
	}

	// Same subnet, same gateway address, different router: a different venue.
	other := NetworkID{Subnet: "192.168.1.0/24", GatewayMAC: "2c:c8:1b:00:00:01"}
	if prev, known, err := h.Visit(other, "192.168.1.1", day2); err != nil || prev.Visits != 0 || len(known) != 0 {
		t.Fatalf("another venue on the same subnet must start empty: %+v %v %v", prev, known, err)
	}
}

func TestHistoryInMemory(t *testing.T) {
	h, err := OpenHistory("")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	id := NetworkID{Subnet: "10.0.0.0/24"}
	if id.String() != "10.0.0.0/24 (no gateway)" {
		t.Fatalf("key = %q", id.String())
	}
	now := time.Now()
	if _, _, err := h.Visit(id, "", now); err != nil {
		t.Fatal(err)
	}
	if err := h.Record(id, Remembered{MAC: "aa:aa:aa:aa:aa:aa", FirstSeen: now, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	if _, known, _ := h.Visit(id, "", now); len(known) != 1 || h.Path() != "" {
		t.Fatalf("in-memory history should persist for the process: %+v", known)
	}
}

func TestDefaultHistoryPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data/here")
	if p, err := DefaultHistoryPath(); err != nil || p != filepath.Join("/data/here", "shoal", "history.db") {
		t.Fatalf("XDG_DATA_HOME: %q %v", p, err)
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/tech")
	p, err := DefaultHistoryPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/home/tech", ".local", "share", "shoal", "history.db")
	if runtime.GOOS == "darwin" {
		want = filepath.Join("/home/tech", "Library", "Application Support", "shoal", "history.db")
	}
	if runtime.GOOS != "windows" && p != want {
		t.Fatalf("path = %q, want %q", p, want)
	}
}

func TestHistoryListsNetworksAndRecallsWithoutVisiting(t *testing.T) {
	h, err := OpenHistory("")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	a := NetworkID{Subnet: "10.0.0.0/24", GatewayMAC: "aa:aa:aa:aa:aa:01"}
	b := NetworkID{Subnet: "192.168.1.0/24", GatewayMAC: "bb:bb:bb:bb:bb:01"}
	t1 := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	h.Visit(a, "10.0.0.1", t1)
	h.Visit(b, "192.168.1.1", t1.Add(time.Hour))
	if err := h.Record(b,
		Remembered{MAC: "cc:cc:cc:cc:cc:01", IP: "192.168.1.5", FirstSeen: t1, LastSeen: t1},
		Remembered{MAC: "cc:cc:cc:cc:cc:02", IP: "192.168.1.6", FirstSeen: t1, LastSeen: t1},
	); err != nil {
		t.Fatal(err)
	}
	nets, err := h.Networks()
	if err != nil || len(nets) != 2 || nets[0].ID != b || nets[1].ID != a {
		t.Fatalf("most recent first: %+v %v", nets, err)
	}
	n, known, err := h.Recall(b)
	if err != nil || n.Visits != 1 || len(known) != 2 {
		t.Fatalf("recall: %+v %v %v", n, known, err)
	}
	if n, _, _ = h.Recall(b); n.Visits != 1 {
		t.Fatal("recalling must not count as a visit")
	}
}
