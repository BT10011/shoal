package access

import (
	"errors"

	"golang.org/x/sys/windows"

	"github.com/BT10011/shoal/internal/probe/arp"
)

// NpcapURL is where Npcap is downloaded from. Its free licence does not
// allow shoal to include it or install it silently, so the user installs
// it from there.
const NpcapURL = "https://npcap.com/#download"

// restrictOption is the Npcap installer's wording for the box that limits
// it to administrators.
const restrictOption = `"Restrict Npcap driver's access to Administrators only"`

// For is what full access takes on Windows, which has no raw network
// access of its own: Npcap, installed by the user, with access open to
// accounts that are not administrators.
func For(_ string, refusal error) Setup {
	if errors.Is(refusal, arp.ErrNpcapAdminOnly) {
		return Setup{
			Missing: "Shoal gets that through Npcap on Windows, and Npcap is installed for\nadministrators only, while shoal is not running as one.",
			Manual: "Install Npcap again from npcap.com, this time with " + restrictOption + "\n" +
				"left unticked. Windows asks for an administrator's password while it installs.",
			URL: NpcapURL,
		}
	}
	return Setup{
		Missing: "Windows has no raw network access of its own: shoal gets it through Npcap,\nwhich is not installed.",
		Manual: "Install Npcap from npcap.com (the free edition), with " + restrictOption + "\n" +
			"left unticked. Windows asks for an administrator's password while it installs.\n" +
			"Npcap's licence does not allow shoal to include it.",
		URL: NpcapURL,
	}
}

// StillRefused explains a refusal that installing Npcap did not cure.
func StillRefused(_ string, refusal error) string {
	return "Npcap is still not usable: " + refusal.Error()
}

// Remove is what --uninstall runs as root to undo setup: nothing, since
// Npcap is not shoal's to remove.
func Remove() []string { return nil }

// Installed lists what setup put on this machine beyond the program file.
func Installed() []string { return nil }

// HasCapability and SetcapPath are for Linux's file capabilities.
func HasCapability(string) bool { return false }
func SetcapPath() string        { return "" }

// Open shows a web page in the default browser.
func Open(url string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}
