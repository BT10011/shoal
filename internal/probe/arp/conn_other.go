//go:build !linux && !darwin && !freebsd

package arp

import (
	"fmt"

	"github.com/BT10011/shoal/internal/netif"
)

// OpenConn is not implemented on this platform; use the neigh probe.
func OpenConn(netif.Interface) (Conn, error) {
	return nil, fmt.Errorf("%w: raw ARP is not supported on this platform", ErrPermission)
}
