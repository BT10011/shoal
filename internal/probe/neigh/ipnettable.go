package neigh

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
)

// The layout of Windows' MIB_IPNETTABLE, which GetIpNetTable fills.
const (
	ipNetRowSize     = 24
	ipNetTypeInvalid = 2
	ipNetTypeStatic  = 4
)

// parseIPNetTable reads the table Windows' GetIpNetTable fills. It is read
// byte by byte here, rather than through a Go struct laid over the buffer,
// so the layout is tested on every platform:
//
//	DWORD dwNumEntries
//	then dwNumEntries rows of MIB_IPNETROW:
//	    DWORD dwIndex        interface index
//	    DWORD dwPhysAddrLen
//	    BYTE  bPhysAddr[8]   the MAC, in its first dwPhysAddrLen bytes
//	    DWORD dwAddr         the IPv4 address, in network byte order
//	    DWORD dwType         1 other, 2 invalid, 3 dynamic, 4 static
//
// The other DWORDs are little-endian, as Windows is. An invalid row is an
// address Windows asked for and got no answer from, so it is skipped, as
// Linux's incomplete rows are. name turns an interface index into a name.
func parseIPNetTable(buf []byte, name func(index uint32) string) ([]Entry, error) {
	if len(buf) < 4 {
		return nil, errors.New("neighbour table is too short to hold its row count")
	}
	n := binary.LittleEndian.Uint32(buf)
	rows := buf[4:]
	if uint64(n)*ipNetRowSize > uint64(len(rows)) {
		return nil, fmt.Errorf("neighbour table claims %d rows but holds only %d bytes of them", n, len(rows))
	}
	var out []Entry
	for i := range int(n) {
		r := rows[i*ipNetRowSize : (i+1)*ipNetRowSize]
		macLen := binary.LittleEndian.Uint32(r[4:])
		typ := binary.LittleEndian.Uint32(r[20:])
		if typ == ipNetTypeInvalid || macLen != 6 {
			continue
		}
		out = append(out, Entry{
			IP:        net.IPv4(r[16], r[17], r[18], r[19]).To4(),
			MAC:       net.HardwareAddr(append([]byte(nil), r[8:14]...)),
			Interface: name(binary.LittleEndian.Uint32(r[0:])),
			Permanent: typ == ipNetTypeStatic,
		})
	}
	return out, nil
}
