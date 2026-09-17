//go:build !(darwin || dragonfly || freebsd || netbsd || openbsd)

package arp

import (
	"errors"

	"github.com/BT10011/shoal/internal/netif"
)

// OpenConn is not yet implemented on this platform.
func OpenConn(netif.Interface) (Conn, error) {
	return nil, errors.New("raw ARP is not supported on this platform yet")
}
