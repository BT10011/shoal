// Package access sets up full network access for shoal: what this machine
// lacks for the ARP sweep, the changes that grant it, and making them with
// the user's agreement and password.
//
// Shoal has no reduced mode (PROJECT_PLAN.md §7, "Release 2 — installing,
// upgrading and full access"). When raw packet access is refused, start-up
// and `shoal --setup` show the user the Setup for this platform and,
// agreed, Apply it. What setup takes differs by platform:
//
//   - Linux: cap_net_raw on the program file (setcap), through sudo.
//   - macOS: the access_bpf group and a start-up job that opens the packet
//     capture devices to it, through sudo, as Wireshark's ChmodBPF does.
//   - Windows: Npcap, which the user installs from npcap.com, since its
//     licence does not allow shoal to include it.
//   - FreeBSD: running shoal as root.
package access

import "strings"

// Setup is what granting full network access takes on this machine.
type Setup struct {
	// Missing says what shoal lacks, in a sentence or two wrapped to fit
	// a terminal.
	Missing string
	// Changes lists, in words, each change Apply makes, in order. It is
	// empty when the user has to act instead (Manual).
	Changes []string
	// Manual is what the user does themselves when shoal cannot make the
	// change: install Npcap, run as root.
	Manual string
	// URL is a page to open for Manual, if there is one.
	URL string
	// Note is what the change means beyond shoal, worth knowing before
	// agreeing to it.
	Note string
	// Settle is how long a fresh process may wait for the change to take
	// effect before calling it a failure: macOS applies a new group
	// membership to a running login after a moment, not at once.
	Settle int // seconds

	// command is what Apply runs as root to make Changes.
	command []string
}

// Automatic reports whether Apply can make the changes itself, given the
// administrator's password.
func (s Setup) Automatic() bool { return len(s.command) > 0 }

// Apply makes the changes, running the command through run, which runs it
// as root with the terminal attached (elevate.Run). For a Setup the user
// has to carry out themselves it does nothing.
func (s Setup) Apply(run func(name string, args ...string) error) error {
	if !s.Automatic() {
		return nil
	}
	return run(s.command[0], s.command[1:]...)
}

// Describe is the whole Setup in words, for the terminal: what is missing
// and what will be done about it.
func (s Setup) Describe() string {
	var b strings.Builder
	b.WriteString("Shoal sweeps the network with ARP, which needs full network access.\n")
	b.WriteString(s.Missing + "\n")
	if len(s.Changes) > 0 {
		b.WriteString("\nSetting it up will:\n")
		for _, c := range s.Changes {
			b.WriteString("  - " + c + "\n")
		}
	}
	if s.Note != "" {
		b.WriteString("\n" + s.Note + "\n")
	}
	if s.Manual != "" {
		b.WriteString("\n" + s.Manual + "\n")
	}
	return b.String()
}
