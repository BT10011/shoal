package arp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/BT10011/shoal/internal/netif"
)

// packetConn is a raw link-layer connection over an AF_PACKET socket bound
// to one interface. The socket's protocol is ETH_P_ARP, so the kernel
// delivers only ARP frames and no userspace filter is needed.
type packetConn struct {
	fd      int
	ifindex int

	mu     sync.Mutex
	buf    []byte
	closed bool
}

func htons(v uint16) uint16 {
	return v<<8 | v>>8
}

// OpenConn binds an AF_PACKET socket to the interface.
func OpenConn(iface netif.Interface) (Conn, error) {
	proto := int(htons(unix.ETH_P_ARP))
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, proto)
	if err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			return nil, fmt.Errorf("%w: AF_PACKET socket refused (%v); run with sudo, or grant the binary cap_net_raw (make setcap)", ErrPermission, err)
		}
		return nil, fmt.Errorf("AF_PACKET socket: %w", err)
	}
	c := &packetConn{fd: fd, ifindex: iface.Index, buf: make([]byte, 2048)}

	if err := unix.Bind(fd, &unix.SockaddrLinklayer{
		Protocol: htons(unix.ETH_P_ARP),
		Ifindex:  iface.Index,
	}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("bind %s: %w", iface.Name, err)
	}
	// Wake the reader periodically so it can notice cancellation.
	tv := unix.Timeval{Usec: 100_000}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("SO_RCVTIMEO: %w", err)
	}
	return c, nil
}

func (c *packetConn) WriteFrame(frame []byte) error {
	if len(frame) < ethHeaderLen {
		return errors.New("frame too short")
	}
	sa := &unix.SockaddrLinklayer{
		Protocol: htons(binary.BigEndian.Uint16(frame[12:14])),
		Ifindex:  c.ifindex,
		Halen:    6,
	}
	copy(sa.Addr[:6], frame[0:6])
	return unix.Sendto(c.fd, frame, 0, sa)
}

// ReadFrame returns the next received frame, or (nil, nil) when the read
// timed out or the frame was one we sent ourselves.
func (c *packetConn) ReadFrame() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("packet: closed")
	}
	n, from, err := unix.Recvfrom(c.fd, c.buf, 0)
	if err != nil {
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			return nil, nil
		}
		return nil, err
	}
	if ll, ok := from.(*unix.SockaddrLinklayer); ok && ll.Pkttype == unix.PACKET_OUTGOING {
		return nil, nil
	}
	return append([]byte(nil), c.buf[:n]...), nil
}

func (c *packetConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return unix.Close(c.fd)
}
