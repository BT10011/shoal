//go:build !(linux || darwin || dragonfly || freebsd || netbsd || openbsd)

package netif

import (
	"errors"
	"net"
)

const gatewaySource = "unsupported platform"

func defaultRoute() (net.IP, string, error) {
	return nil, "", errors.New("default route lookup not supported on this platform")
}
