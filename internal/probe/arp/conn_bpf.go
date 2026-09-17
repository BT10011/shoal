//go:build darwin || freebsd

package arp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/BT10011/shoal/internal/netif"
)

// bpfConn is a raw link-layer connection over a BSD packet filter device,
// filtered in the kernel to ARP frames only.
type bpfConn struct {
	fd   int
	path string
	blen int

	mu      sync.Mutex
	buf     []byte
	pending [][]byte
	closed  bool
}

// OpenConn attaches to the first free /dev/bpf device on the interface.
func OpenConn(iface netif.Interface) (Conn, error) {
	for i := 0; i < 256; i++ {
		path := fmt.Sprintf("/dev/bpf%d", i)
		fd, err := unix.Open(path, unix.O_RDWR, 0)
		switch {
		case err == nil:
			c := &bpfConn{fd: fd, path: path}
			if err := c.configure(iface); err != nil {
				unix.Close(fd)
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			return c, nil
		case errors.Is(err, unix.EBUSY):
			continue
		case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
			return nil, fmt.Errorf("%w: cannot open %s (%v); run with sudo, or add your user to the access_bpf group (Wireshark's ChmodBPF does this) and log in again", ErrPermission, path, err)
		case errors.Is(err, unix.ENOENT):
			return nil, errors.New("no /dev/bpf devices found")
		default:
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
	}
	return nil, errors.New("all /dev/bpf devices are busy")
}

// arpOnly accepts frames whose Ethernet type is ARP and drops the rest.
var arpOnly = []unix.BpfInsn{
	{Code: 0x28, K: 12},                   // ldh [12]           load ethertype
	{Code: 0x15, Jt: 0, Jf: 1, K: 0x0806}, // jeq #0x0806, L1, L2
	{Code: 0x06, K: 0xffffffff},           // L1: ret #-1        accept whole frame
	{Code: 0x06, K: 0},                    // L2: ret #0         drop
}

func (c *bpfConn) configure(iface netif.Interface) error {
	// Buffer length must be set before the interface is attached.
	_ = unix.IoctlSetPointerInt(c.fd, unix.BIOCSBLEN, 1<<16)
	blen, err := unix.IoctlGetInt(c.fd, unix.BIOCGBLEN)
	if err != nil {
		return fmt.Errorf("BIOCGBLEN: %w", err)
	}
	c.blen = blen
	c.buf = make([]byte, blen)

	var ifr ifreq
	if len(iface.Name) >= len(ifr.Name) {
		return fmt.Errorf("interface name %q too long", iface.Name)
	}
	copy(ifr.Name[:], iface.Name)
	if err := ioctlPtr(c.fd, unix.BIOCSETIF, unsafe.Pointer(&ifr)); err != nil {
		return fmt.Errorf("BIOCSETIF %s: %w", iface.Name, err)
	}
	if err := unix.IoctlSetPointerInt(c.fd, unix.BIOCIMMEDIATE, 1); err != nil {
		return fmt.Errorf("BIOCIMMEDIATE: %w", err)
	}
	// We build the whole Ethernet header ourselves, including the source MAC.
	if err := unix.IoctlSetPointerInt(c.fd, unix.BIOCSHDRCMPLT, 1); err != nil {
		return fmt.Errorf("BIOCSHDRCMPLT: %w", err)
	}
	// Do not hand our own transmitted frames back to us.
	_ = unix.IoctlSetPointerInt(c.fd, unix.BIOCSSEESENT, 0)

	tv := unix.Timeval{Usec: 100_000}
	if err := ioctlPtr(c.fd, unix.BIOCSRTIMEOUT, unsafe.Pointer(&tv)); err != nil {
		return fmt.Errorf("BIOCSRTIMEOUT: %w", err)
	}
	prog := unix.BpfProgram{Len: uint32(len(arpOnly)), Insns: &arpOnly[0]}
	if err := ioctlPtr(c.fd, unix.BIOCSETF, unsafe.Pointer(&prog)); err != nil {
		return fmt.Errorf("BIOCSETF: %w", err)
	}
	return nil
}

// ifreq mirrors struct ifreq: a 16-byte name and a 16-byte union that
// BIOCSETIF ignores.
type ifreq struct {
	Name [16]byte
	_    [16]byte
}

func ioctlPtr(fd int, req uint, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

func (c *bpfConn) WriteFrame(frame []byte) error {
	n, err := unix.Write(c.fd, frame)
	if err != nil {
		return err
	}
	if n != len(frame) {
		return fmt.Errorf("short write: %d of %d bytes", n, len(frame))
	}
	return nil
}

// ReadFrame returns the next captured frame, or (nil, nil) if none arrived
// within the read timeout.
func (c *bpfConn) ReadFrame() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) > 0 {
		f := c.pending[0]
		c.pending = c.pending[1:]
		return f, nil
	}
	if c.closed {
		return nil, errors.New("bpf: closed")
	}
	n, err := unix.Read(c.fd, c.buf)
	if err != nil {
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			return nil, nil
		}
		return nil, err
	}
	c.pending = splitBPF(c.buf[:n])
	if len(c.pending) == 0 {
		return nil, nil
	}
	f := c.pending[0]
	c.pending = c.pending[1:]
	return f, nil
}

// splitBPF walks a BPF read buffer: each record is a bpf_hdr (timestamp,
// caplen, datalen, hdrlen) followed by the frame, padded to 4 bytes.
func splitBPF(buf []byte) [][]byte {
	var frames [][]byte
	for len(buf) >= 18 {
		caplen := binary.LittleEndian.Uint32(buf[8:12])
		hdrlen := int(binary.LittleEndian.Uint16(buf[16:18]))
		end := hdrlen + int(caplen)
		if hdrlen < 18 || end > len(buf) {
			break
		}
		frames = append(frames, append([]byte(nil), buf[hdrlen:end]...))
		next := (end + 3) &^ 3
		if next > len(buf) {
			break
		}
		buf = buf[next:]
	}
	return frames
}

func (c *bpfConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return unix.Close(c.fd)
}
