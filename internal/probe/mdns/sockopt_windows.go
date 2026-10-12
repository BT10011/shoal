//go:build windows

package mdns

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// shareablePort sets the option that lets a socket share port 5353 with the
// system's own responder. Windows has no SO_REUSEPORT: SO_REUSEADDR alone
// lets a second socket bind a port another already holds, unless that one
// asked for SO_EXCLUSIVEADDRUSE.
func shareablePort(_, _ string, c syscall.RawConn) error {
	var opErr error
	err := c.Control(func(fd uintptr) {
		opErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
	})
	if err != nil {
		return err
	}
	return opErr
}
