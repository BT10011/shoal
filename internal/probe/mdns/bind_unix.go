//go:build unix

package mdns

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// bindGroup opens a UDP socket bound to the multicast group address itself,
// sharing the port with the system's own responder.
//
// This is done by hand rather than with net.ListenPacket because Go quietly
// substitutes the wildcard address for a multicast one (net.listenDatagram,
// so that one listener can join several groups). The substitution is not
// what shoal wants, and the cost was not obvious: a socket on 0.0.0.0:5353
// with SO_REUSEPORT joins the port-sharing group the system responder is
// in, and the kernel then hands *unicast* datagrams for port 5353 to
// whichever socket it picks. shoal was taking mDNS replies meant for
// avahi-daemon or mDNSResponder — including its own question to the
// responder on this host, which is why the machine shoal runs on never
// reported the services it offers. Bound to the group address, only
// multicast arrives here and unicast stays with the responder it was
// addressed to.
//
// Windows cannot bind a multicast address; see the survey in the plan. The
// caller falls back to the wildcard bind if this fails for any reason.
func bindGroup(group *net.UDPAddr) (net.PacketConn, error) {
	ip := group.IP.To4()
	if ip == nil {
		return nil, fmt.Errorf("%s is not an IPv4 group address", group.IP)
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.IPPROTO_UDP)
	if err != nil {
		return nil, fmt.Errorf("cannot open a UDP socket: %w", err)
	}
	// Both are needed to sit alongside the system responder: BSD and macOS
	// want SO_REUSEPORT on every socket on the port, Linux SO_REUSEADDR.
	for _, opt := range []int{unix.SO_REUSEADDR, unix.SO_REUSEPORT} {
		if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, opt, 1); err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("cannot share the port: %w", err)
		}
	}
	sa := &unix.SockaddrInet4{Port: group.Port}
	copy(sa.Addr[:], ip)
	if err := unix.Bind(fd, sa); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("cannot bind %s: %w", group, err)
	}
	// FilePacketConn duplicates the descriptor and puts the copy into the
	// runtime poller, which is what gives read deadlines; the original is
	// ours to close.
	f := os.NewFile(uintptr(fd), "mdns")
	conn, err := net.FilePacketConn(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("cannot use the bound socket: %w", err)
	}
	return conn, nil
}
