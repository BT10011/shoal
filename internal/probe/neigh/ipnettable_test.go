package neigh

import (
	"encoding/binary"
	"net"
	"strings"
	"testing"
)

// ipNetRow builds one MIB_IPNETROW as GetIpNetTable lays it out.
func ipNetRow(index uint32, mac string, ip string, typ uint32) []byte {
	r := make([]byte, ipNetRowSize)
	binary.LittleEndian.PutUint32(r[0:], index)
	if mac != "" {
		hw, err := net.ParseMAC(mac)
		if err != nil {
			panic(err)
		}
		binary.LittleEndian.PutUint32(r[4:], uint32(len(hw)))
		copy(r[8:16], hw)
	}
	copy(r[16:20], net.ParseIP(ip).To4()) // network byte order: as written
	binary.LittleEndian.PutUint32(r[20:], typ)
	return r
}

func ipNetTable(rows ...[]byte) []byte {
	buf := binary.LittleEndian.AppendUint32(nil, uint32(len(rows)))
	for _, r := range rows {
		buf = append(buf, r...)
	}
	return buf
}

func TestParseIPNetTable(t *testing.T) {
	buf := ipNetTable(
		ipNetRow(12, "00:00:5e:00:53:01", "172.16.10.1", 3),  // dynamic
		ipNetRow(12, "00:00:5e:00:53:20", "172.16.10.20", 4), // static
		ipNetRow(12, "00:00:00:00:00:00", "172.16.10.99", 2), // invalid: asked, never answered
		ipNetRow(12, "", "172.16.10.98", 3),                  // no MAC
		ipNetRow(7, "00:00:5e:00:53:30", "198.51.100.30", 3), // another interface
		ipNetRow(12, "01:00:5e:00:00:fb", "224.0.0.251", 4),  // multicast, kept here; usable() drops it
	)
	names := map[uint32]string{12: "Ethernet", 7: "Wi-Fi"}
	got, err := parseIPNetTable(buf, func(i uint32) string { return names[i] })
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{IP: net.IPv4(172, 16, 10, 1).To4(), MAC: mustMAC("00:00:5e:00:53:01"), Interface: "Ethernet"},
		{IP: net.IPv4(172, 16, 10, 20).To4(), MAC: mustMAC("00:00:5e:00:53:20"), Interface: "Ethernet", Permanent: true},
		{IP: net.IPv4(198, 51, 100, 30).To4(), MAC: mustMAC("00:00:5e:00:53:30"), Interface: "Wi-Fi"},
		{IP: net.IPv4(224, 0, 0, 251).To4(), MAC: mustMAC("01:00:5e:00:00:fb"), Interface: "Ethernet", Permanent: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries %+v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if !g.IP.Equal(w.IP) || g.MAC.String() != w.MAC.String() || g.Interface != w.Interface || g.Permanent != w.Permanent {
			t.Errorf("entry %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestParseIPNetTableRefusesATruncatedBuffer(t *testing.T) {
	if got, err := parseIPNetTable(ipNetTable(), func(uint32) string { return "" }); err != nil || len(got) != 0 {
		t.Errorf("an empty table = %v, %v; want no entries and no error", got, err)
	}
	if _, err := parseIPNetTable([]byte{1, 0}, func(uint32) string { return "" }); err == nil {
		t.Error("a buffer shorter than the row count parsed without error")
	}
	buf := ipNetTable(ipNetRow(12, "00:00:5e:00:53:01", "172.16.10.1", 3))
	binary.LittleEndian.PutUint32(buf, 2) // claims a row it does not hold
	if _, err := parseIPNetTable(buf, func(uint32) string { return "" }); err == nil || !strings.Contains(err.Error(), "2 rows") {
		t.Errorf("a table claiming more rows than it holds = %v, want an error saying so", err)
	}
}

func mustMAC(s string) net.HardwareAddr {
	hw, err := net.ParseMAC(s)
	if err != nil {
		panic(err)
	}
	return hw
}
