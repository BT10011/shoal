//go:build !linux

package selfupdate

import "io"

// Off Linux an update has no raw network access to carry over. macOS
// reaches the raw network through the BPF devices, which are opened to a
// group rather than to anything attached to the binary, so replacing the
// file takes nothing away; FreeBSD runs shoal as root; Windows goes through
// Npcap, installed on its own and untouched by an update.

func (o Options) restoreRawAccess(string) {}

func noSetcap(io.Writer, string) {}
