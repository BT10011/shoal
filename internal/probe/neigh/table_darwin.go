package neigh

import (
	"fmt"
	"net"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

// TableSource names where the entries come from, for the UI and docs.
const TableSource = "routing table (sysctl NET_RT_FLAGS with RTF_LLINFO)"

// Table reads the kernel's IPv4 neighbour cache. On macOS the ARP table is
// part of the routing table: link-layer entries are routes carrying
// RTF_LLINFO whose gateway is a link address holding the MAC. (Modern
// FreeBSD and friends moved ARP out of the routing table, so this file is
// Darwin-only.)
func Table() ([]Entry, error) {
	rib, err := route.FetchRIB(unix.AF_INET, route.RIBType(unix.NET_RT_FLAGS), unix.RTF_LLINFO)
	if err != nil {
		return nil, fmt.Errorf("sysctl NET_RT_FLAGS: %w", err)
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, m := range msgs {
		rm, ok := m.(*route.RouteMessage)
		if !ok || len(rm.Addrs) <= unix.RTAX_GATEWAY {
			continue
		}
		dst, ok := rm.Addrs[unix.RTAX_DST].(*route.Inet4Addr)
		if !ok {
			continue
		}
		gw, ok := rm.Addrs[unix.RTAX_GATEWAY].(*route.LinkAddr)
		if !ok || len(gw.Addr) != 6 {
			continue
		}
		name := gw.Name
		if name == "" {
			if ni, err := net.InterfaceByIndex(gw.Index); err == nil {
				name = ni.Name
			}
		}
		out = append(out, Entry{
			IP:        net.IPv4(dst.IP[0], dst.IP[1], dst.IP[2], dst.IP[3]).To4(),
			MAC:       net.HardwareAddr(append([]byte(nil), gw.Addr...)),
			Interface: name,
			Permanent: rm.Flags&unix.RTF_STATIC != 0,
		})
	}
	return out, nil
}
