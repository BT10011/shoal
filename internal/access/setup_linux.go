package access

import (
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// For is what full access takes for the program at binary, given why raw
// access was refused. On Linux it is one capability on the file.
func For(binary string, _ error) Setup {
	s := Setup{Missing: "On Linux that is the cap_net_raw capability on the program file, and this copy\ndoes not have it."}
	setcap := SetcapPath()
	if setcap == "" {
		s.Manual = "setcap, which grants it, is not installed. Install libcap2-bin (Debian, Ubuntu),\nlibcap-progs (openSUSE) or libcap (Fedora, Arch), then run shoal --setup again."
		return s
	}
	s.Changes = []string{fmt.Sprintf("give the cap_net_raw capability (setcap cap_net_raw+ep) to\n    %s, so it can send and hear ARP without running as root", binary)}
	s.command = []string{setcap, "cap_net_raw+ep", binary}
	return s
}

// StillRefused explains a refusal that setup did not cure, once a fresh
// process has tried again.
func StillRefused(binary string, refusal error) string {
	if HasCapability(binary) {
		return fmt.Sprintf("%s has the cap_net_raw capability, but Linux did not give it to shoal. "+
			"That happens when its filesystem is mounted nosuid, or when shoal runs where new privileges are blocked: "+
			"a container, a sandbox, or a service with NoNewPrivileges. Install shoal somewhere else, "+
			"or run it outside the sandbox. (%v)", binary, refusal)
	}
	return fmt.Sprintf("setcap did not leave the cap_net_raw capability on %s, so it still cannot send ARP. "+
		"Run sudo setcap cap_net_raw+ep %s and look at what it says. (%v)", binary, binary, refusal)
}

// Remove is what --uninstall runs as root to undo setup: nothing on Linux,
// where the capability goes with the file.
func Remove() []string { return nil }

// Installed lists what setup put on this machine beyond the program file.
func Installed() []string { return nil }

// Open shows a web page in the default browser.
func Open(url string) error { return exec.Command("xdg-open", url).Run() }

// HasCapability reports whether the file carries a capability set. The
// capabilities live in an extended attribute, so reading it needs no
// privileges and no cgo; only its presence matters here.
func HasCapability(path string) bool {
	buf := make([]byte, 64)
	n, err := unix.Getxattr(path, "security.capability", buf)
	return err == nil && n > 0
}

// SetcapPath finds setcap, which distributions keep in sbin, often off an
// ordinary user's PATH.
func SetcapPath() string {
	if p, err := exec.LookPath("setcap"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/sbin/setcap", "/sbin/setcap"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}
