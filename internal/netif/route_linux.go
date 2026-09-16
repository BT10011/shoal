package netif

import (
	"net"
	"os"
)

const gatewaySource = "default route from /proc/net/route"

func defaultRoute() (gateway net.IP, ifName string, err error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	return parseProcRoute(f)
}
