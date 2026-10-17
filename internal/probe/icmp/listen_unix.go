//go:build !windows

package icmp

import (
	"context"
	"fmt"
	"net"

	xicmp "golang.org/x/net/icmp"
)

// Listen opens an ICMP socket, preferring the unprivileged kind. macOS allows
// datagram ICMP to every process; Linux allows it only to group ids inside
// net.ipv4.ping_group_range, and falls back to a raw socket, which needs
// cap_net_raw.
func Listen(ctx context.Context) (net.PacketConn, Mode, error) {
	conn, dgramErr := xicmp.ListenPacket("udp4", "0.0.0.0")
	if dgramErr == nil {
		return conn, ModeDatagram, nil
	}
	conn, rawErr := xicmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if rawErr == nil {
		return conn, ModeRaw, nil
	}
	return nil, "", fmt.Errorf("no ICMP socket available: unprivileged (%v); raw (%v). On Linux, either widen net.ipv4.ping_group_range or grant cap_net_raw with shoal --setup", dgramErr, rawErr)
}
