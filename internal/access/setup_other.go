//go:build !linux && !darwin && !windows

package access

import "os/exec"

// For is what full access takes elsewhere, FreeBSD among them: root, since
// nothing grants the packet capture devices to an ordinary account.
func For(string, error) Setup {
	return Setup{
		Missing: "Here only root may open the packet capture devices.",
		Manual:  "Run it as root: sudo shoal",
	}
}

// StillRefused explains a refusal that setup did not cure.
func StillRefused(_ string, refusal error) string { return refusal.Error() }

// Remove is what --uninstall runs as root to undo setup: nothing here.
func Remove() []string { return nil }

// Installed lists what setup put on this machine beyond the program file.
func Installed() []string { return nil }

// HasCapability and SetcapPath are for Linux's file capabilities.
func HasCapability(string) bool { return false }
func SetcapPath() string        { return "" }

// Open shows a web page in the default browser.
func Open(url string) error { return exec.Command("xdg-open", url).Run() }
