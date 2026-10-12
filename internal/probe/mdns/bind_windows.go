//go:build windows

package mdns

import (
	"errors"
	"net"
)

// bindGroup always fails on Windows, which cannot bind a socket to a
// multicast address, so ListenMulticast falls back to the wildcard bind.
// That socket can also be handed unicast datagrams for the port, which is
// the cost bind_unix.go avoids elsewhere.
func bindGroup(*net.UDPAddr) (net.PacketConn, error) {
	return nil, errors.New("Windows cannot bind a socket to a multicast address")
}
