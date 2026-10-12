//go:build !windows

package capture

import "os"

// ownerOnly sets mode 0600. A new file is created with it already, but a
// file --pcap replaces keeps the mode it had.
func ownerOnly(path string) error {
	return os.Chmod(path, 0o600)
}
