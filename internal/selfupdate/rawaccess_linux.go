package selfupdate

import (
	"fmt"
	"io"

	"github.com/BT10011/shoal/internal/access"
)

// On Linux the ability to send ARP without running as root is a capability
// attached to the file (cap_net_raw, granted by shoal --setup, a package's
// install script, install.sh or `make setcap`). Replacing the file drops
// it, so an update has to look for it on the old file and put it back on
// the new one.

// restoreRawAccess gives the new binary at target the capability the old
// one had, through sudo, which asks for the password on the terminal.
func (o Options) restoreRawAccess(target string) {
	setcap := access.SetcapPath()
	if setcap == "" {
		noSetcap(o.out(), target)
		return
	}
	run := o.elevator()
	if run == nil {
		fmt.Fprintf(o.out(), "\nThe old binary could send ARP without root; replacing it dropped that.\nTo give it back, run once:\n  %s\n", grantCommand(target))
		return
	}
	fmt.Fprintln(o.out(), "Giving the new binary raw network access (cap_net_raw), as the old one had: sudo may ask for your password.")
	if err := run(setcap, "cap_net_raw+ep", target); err != nil {
		fmt.Fprintf(o.out(), "\nCould not grant it (%v). Run once:\n  %s\n\nUntil then shoal asks for it when it starts.\n", err, grantCommand(target))
		return
	}
	fmt.Fprintln(o.out(), "Raw network access (cap_net_raw) granted again.")
}

// noSetcap says what to do when setcap is not installed to put the
// capability back.
func noSetcap(out io.Writer, target string) {
	fmt.Fprintf(out, "\nThe new binary cannot send ARP until it is granted raw network access,\nand setcap is not installed to grant it. Install it (libcap2-bin on Debian\nand Ubuntu, libcap-progs on openSUSE, libcap on Fedora and Arch) and run: %s\n", grantCommand(target))
}

func grantCommand(target string) string {
	return "sudo setcap cap_net_raw+ep " + target
}
