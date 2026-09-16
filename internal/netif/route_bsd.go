//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package netif

import (
	"errors"
	"net"
	"syscall"

	"golang.org/x/net/route"
)

const gatewaySource = "default route from the kernel routing table (sysctl NET_RT_DUMP)"

// defaultRoute finds the IPv4 default route via the BSD routing socket.
func defaultRoute() (gateway net.IP, ifName string, err error) {
	rib, err := route.FetchRIB(syscall.AF_INET, route.RIBTypeRoute, 0)
	if err != nil {
		return nil, "", err
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return nil, "", err
	}
	for _, m := range msgs {
		rm, ok := m.(*route.RouteMessage)
		if !ok || rm.Flags&syscall.RTF_UP == 0 || rm.Flags&syscall.RTF_GATEWAY == 0 {
			continue
		}
		if len(rm.Addrs) <= syscall.RTAX_GATEWAY {
			continue
		}
		dst, ok := rm.Addrs[syscall.RTAX_DST].(*route.Inet4Addr)
		if !ok || dst.IP != [4]byte{} {
			continue
		}
		gw, ok := rm.Addrs[syscall.RTAX_GATEWAY].(*route.Inet4Addr)
		if !ok {
			continue
		}
		ni, err := net.InterfaceByIndex(rm.Index)
		if err != nil {
			continue
		}
		return net.IP(gw.IP[:]), ni.Name, nil
	}
	return nil, "", errors.New("no IPv4 default route in routing table")
}
