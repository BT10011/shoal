package neigh

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
)

// Linux ARP flags from include/uapi/linux/if_arp.h.
const (
	atfCom  = 0x02 // entry is complete: the MAC is known
	atfPerm = 0x04 // entry is static
)

// parseProcARP reads the Linux /proc/net/arp table:
//
//	IP address  HW type  Flags  HW address         Mask  Device
//	10.0.0.1    0x1      0x2    00:00:5e:00:53:02  *     eth0
//
// Incomplete entries (flags without ATF_COM) are skipped: the kernel asked
// but never got an answer, so there is no MAC to report.
func parseProcARP(r io.Reader) ([]Entry, error) {
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		return nil, errors.New("/proc/net/arp is empty")
	}
	var out []Entry
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 6 {
			continue
		}
		ip := net.ParseIP(f[0]).To4()
		if ip == nil {
			continue
		}
		flags, err := strconv.ParseUint(strings.TrimPrefix(f[2], "0x"), 16, 32)
		if err != nil || flags&atfCom == 0 {
			continue
		}
		mac, err := net.ParseMAC(f[3])
		if err != nil || len(mac) != 6 {
			continue
		}
		out = append(out, Entry{
			IP:        ip,
			MAC:       mac,
			Interface: f[5],
			Permanent: flags&atfPerm != 0,
		})
	}
	return out, sc.Err()
}
