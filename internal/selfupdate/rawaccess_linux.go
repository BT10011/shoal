package selfupdate

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// reportRawAccess deals with the one thing an update silently takes away.
//
// On Linux the ability to send ARP without running as root is a capability
// attached to the file (cap_net_raw, granted by install.sh or `make
// setcap`). Replacing the file drops it, so a shoal that swept the subnet
// before an update would quietly fall back to reading the kernel's
// neighbour table afterwards, with no hint that the update caused it. If
// the old binary had the capability, put it back — or, when that needs a
// password shoal cannot ask for, say exactly what to run.
func reportRawAccess(out io.Writer, target, goos string) {
	if goos != "linux" || !hadRawAccess(target) {
		return
	}
	grant := fmt.Sprintf("sudo setcap cap_net_raw+ep %s", target)
	setcap, err := exec.LookPath("setcap")
	if err != nil {
		for _, p := range []string{"/usr/sbin/setcap", "/sbin/setcap"} {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				setcap, err = p, nil
				break
			}
		}
	}
	if setcap == "" {
		fmt.Fprintf(out, "\nThe new binary cannot send ARP until it is granted raw network access,\nand setcap is not installed to grant it. Install libcap (libcap2-bin on\nDebian and Ubuntu) and run: %s\n", grant)
		return
	}
	if os.Geteuid() == 0 {
		if err := exec.Command(setcap, "cap_net_raw+ep", target).Run(); err == nil {
			fmt.Fprintln(out, "Raw network access (cap_net_raw) granted again, as the old binary had.")
			return
		}
	}
	fmt.Fprintf(out, "\nThe old binary could send ARP without root; replacing it dropped that.\nTo give it back, run once:\n  %s\n\nWithout it Shoal still works, reading the kernel's neighbour table\ninstead of sweeping, and says so in the \"under the hood\" pane.\n", grant)
}

// hadRawAccess reports whether the file carries a capability set. The
// capabilities live in an extended attribute, so reading it needs no
// privileges and no cgo; only its presence matters here.
func hadRawAccess(path string) bool {
	buf := make([]byte, 64)
	n, err := unix.Getxattr(path, "security.capability", buf)
	return err == nil && n > 0
}
