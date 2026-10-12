//go:build unix

package mdns

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// shareablePort sets the options that let a socket share port 5353 with the
// system's own responder. BSD and macOS need SO_REUSEPORT on every socket
// bound to the port; Linux accepts SO_REUSEADDR on both, whoever owns the
// other one.
func shareablePort(_, _ string, c syscall.RawConn) error {
	var opErr error
	err := c.Control(func(fd uintptr) {
		if opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); opErr != nil {
			return
		}
		opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	})
	if err != nil {
		return err
	}
	return opErr
}
