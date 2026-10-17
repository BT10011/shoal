package access

import (
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// For is what full access takes on macOS, where only root may use the
// packet capture devices until they are opened to a group: Wireshark's
// approach (macSetup), changing only what is not done already.
func For(_ string, _ error) Setup {
	s := Setup{
		Missing: "macOS lets only root use the packet capture devices, /dev/bpf*, until they\nare opened to a group, and this account cannot use them yet.",
		Note: "Any program run from an account in the " + Group + " group can capture and send\n" +
			"raw packets, as with Wireshark's ChmodBPF, which shares the group.",
		Settle: 10,
	}
	name := setupUser()
	if name == "" {
		s.Manual = "Run shoal --setup from your own account rather than as root."
		return s
	}
	group := exec.Command("dscl", ".", "-read", "/Groups/"+Group).Run() == nil
	if !group {
		s.Changes = append(s.Changes, "create the "+Group+" group, for the accounts allowed to capture packets")
	}
	if !group || !member(name) {
		s.Changes = append(s.Changes, "add "+name+" to the "+Group+" group")
	}
	if !exists(WiresharkPlist) && !exists(DaemonPlist) {
		s.Changes = append(s.Changes, "install a start-up job, "+DaemonPlist+",\n    that opens /dev/bpf* to the "+Group+" group each time the Mac starts")
	}
	s.Changes = append(s.Changes, "open /dev/bpf* to the "+Group+" group now, without waiting for a restart")
	s.command = []string{"/bin/sh", "-c", macSetup, "shoal-setup", name}
	return s
}

// setupUser is the account to add to the group: the one running shoal, or
// under sudo the one that ran sudo. Plain root has no account to add.
func setupUser() string {
	if os.Geteuid() == 0 {
		return os.Getenv("SUDO_USER")
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// member reports whether the account is in Group, as Directory Services
// sees it: "user is a member of the group", or "is not a member".
func member(name string) bool {
	out, err := exec.Command("dsmemberutil", "checkmembership", "-U", name, "-G", Group).Output()
	return err == nil && strings.Contains(string(out), "is a member")
}

// StillRefused explains a refusal that setup did not cure. The usual cause
// is a login that has not picked up its new group yet.
func StillRefused(_ string, refusal error) string {
	return "macOS has not applied the " + Group + " group to this login yet. Log out and back in,\n" +
		"or restart the Mac, then run shoal again. (" + refusal.Error() + ")"
}

// Remove is what --uninstall runs as root to take setup's start-up job and
// its script away, or nil when neither is there. The group stays, since
// Wireshark may use it too.
func Remove() []string {
	if !exists(DaemonPlist) && !exists(SupportDir) {
		return nil
	}
	return []string{"/bin/sh", "-c", macRemove}
}

// Installed lists what setup put on this machine, for --uninstall to show.
func Installed() []string {
	var found []string
	for _, p := range []string{DaemonPlist, SupportDir} {
		if exists(p) {
			found = append(found, p)
		}
	}
	return found
}

// Open shows a web page in the default browser.
func Open(url string) error { return exec.Command("open", url).Run() }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
