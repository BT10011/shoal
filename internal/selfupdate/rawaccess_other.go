//go:build !linux

package selfupdate

import "io"

// reportRawAccess has nothing to say off Linux. macOS reaches the raw
// network through the BPF devices, which are granted by group membership
// rather than by anything attached to the binary, so replacing the file
// takes nothing away; FreeBSD runs shoal as root.
func reportRawAccess(io.Writer, string, string) {}
