package ui

import (
	"fmt"
	"strings"

	"github.com/allisonhere/tideui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// manual is the in-app counterpart to docs/protocols/. Lines beginning with
// "# " are headings; paragraphs wrap to the modal; lines indented with two
// spaces keep that indent when they wrap.
const manual = `# How to read this screen

Shoal finds the devices on your local subnet and, above all, shows how it
knows what it knows. Every value on screen came from a packet or a lookup,
and the details pane can tell you which one. Nothing appears by magic.

# The panes

  Devices: one row per device found so far. The row under the cursor is
  the one the details pane describes. Move with the arrow keys or j and k,
  a page at a time with PgUp and PgDn, and to either end with g and G.

  Details: everything known about the selected device, grouped by field
  and then by the probe that learned it. Press Enter (or Tab) to move the
  focus here and scroll it with the same movement keys; Esc goes back.

  Under the hood: what the probes are doing right now. A progress bar per
  discoverer that sweeps, a rolling wave for one that only listens (it has
  no end to count towards, so the row says how many messages it has heard
  and the wave goes flat when it stops), a queue count per enricher, and
  beneath the rule a log of every packet sent (→) and received (←), with
  the time it happened. The
  log follows the newest event until you Tab into the pane and press ↑:
  that pins it, selects an event, and shows that event in full, wrapped
  rather than cut off, with a line saying what kind of event it was and
  which address it concerned. Move through the log with the usual keys,
  g for the oldest event; G lets it follow again, as does stepping down
  onto the newest event. The header says "pinned" while it is.

Tab moves between the three panes. On a terminal narrower than 80
columns or shorter than 16 rows the panes become tabs instead.

# Probes: discoverers and enrichers

A probe is one technique for learning about the network. There are two
kinds, and the difference explains the order in which the table fills in.

A discoverer finds devices. The ARP sweep asks every address in the subnet
"who has this IP?" and notes which MAC address answers; it needs raw
packet access. Without it, shoal nudges each address and reads the
kernel's neighbour table instead, which is the same information one step
removed. The mDNS listener sends nothing at all: it writes down what
devices announce about themselves on the multicast group.

An enricher adds facts to a device that is already known. Each one is
triggered by a field: when a device gains an IP, rdns asks the resolver
for a PTR record, mdns asks the device its own name, nbns asks for its
NetBIOS name table and icmp measures the round trip. When a device gains a
MAC, oui looks the vendor up in the embedded IEEE registry.

# The columns

  IP: the address the device answered from.
  MAC: its hardware address. Devices are keyed by MAC, so two rows with
  the same IP are two devices fighting over one address, and both carry a
  duplicate-ip flag.
  HOSTNAME: the best name any probe found. A trailing ! means two probes
  disagree about the name; the details pane shows both.
  VENDOR: the maker, from the first three bytes of the MAC. A randomised
  or locally-administered MAC has no vendor, and says so in the details.
  RTT: the best of three ICMP round trips.
  FLAGS: short badges for things worth a second look, explained under
  "Devices that do not belong" below.

The narrow column at the far left is freshness, described below. Columns
that do not fit are dropped, MAC first, because it is always in the
details pane.

# Reading the details pane

Each fact is shown as its value, then an arrow with the method: a plain
sentence saying what was sent, what came back and from where. Below that
is the probe that learned it, its confidence, how long ago, and when it
expires.

  Confidence is the probe's own estimate, from 0 to 1, of how sure it is
  that the value belongs to this device. An ARP reply is 1.0: the device
  itself said so. A PTR record from a resolver is 0.7: the resolver was
  told, and may be out of date.

  When two probes give different values for one field, both are listed
  and the second is marked "disagrees". The one shown in the table is the
  most confident; if that is a tie, the one from the higher-priority
  source; if still tied, the newest.

  TTL is how long the value is trusted. DNS and mDNS answers carry their
  own; when it runs out the value disappears from the table and a rescan
  fetches it again. A value with no expiry stays until it is replaced.

Press x to see the raw packet or answer behind each value as a hex dump,
so you can check the interpretation against the bytes. Values that came
from a lookup table rather than the wire say so instead.

# Freshness

The first column of the table, and the third line of the details pane,
say how recently a probe heard from the device directly. Only probes that
exchanged packets with the device count: arp, neigh, mdns, nbns and icmp.
A resolver or the vendor table answering says nothing about whether the
device is still there.

  (blank): heard from since the current scan began.
  ?: not heard from yet this scan, which is still running. Shown muted.
  ✗: the scan finished, including every follow-up lookup, without hearing
  from it. Shown in the error colour.
  -: nothing has ever exchanged packets with it.

# Devices that do not belong

Gear moves between venues and arrives still carrying an address from the
last one, or with no configuration at all. It never registers properly,
and the question on the floor is which box it is. The FLAGS column marks
what the details pane explains in full:

  link-local: the address is in 169.254.0.0/16, which a device gives
  itself when it asks for DHCP and nothing answers. A DHCP server that is
  down or absent on this VLAN, a cable in the wrong socket, or a box that
  was never set up.
  off-subnet: the address is outside the subnet being scanned, yet the
  device is on this segment. Usually a static address left over from
  another network or venue, or a lease from a different VLAN.
  dup-ip: two devices claim the same address; both are marked.
  self: this machine.
  rand-mac: a randomised, locally-administered MAC, so no vendor can be
  looked up. Phones and laptops do this on purpose.

The first two are found without sending anything. A device on the wrong
subnet still broadcasts ARP for its old gateway and announces itself when
it links up, and the ARP listener writes every such frame down for as long
as the scan runs, after the sweep has finished. Type /link-local or
/off-subnet to see only those devices. A row with no IP at all is a device
seen only at layer 2, usually one still probing for an address.

That also sets the limit: all of this is layer 2, so it only works on the
same VLAN or switch segment as the device. Nothing crosses a router. When a
box that must be there does not show, that is the first thing to check.

# Scans

Everything shoal knows is kept until you quit. Press c to stop a scan
when you want the screen to hold still: discoverers are cancelled and
queued lookups dropped, so nothing more arrives; a lookup already waiting
for a reply finishes on its own timeout. Press r to run every discoverer
again and re-ask the enrichers about every known device: new answers sit
beside the old ones with fresh timestamps, so the freshness column shows
what has gone quiet. The status bar shows which scan is running, and
whether it has been stopped.

# Filter and sort

Press / and type to narrow the table as you type. The pattern is matched
against every value of every field, so an address, a name, a vendor, a
service such as _ipp._tcp or a flag such as duplicate-ip all work. A
pattern containing * ? or [ is a glob that must match a whole value;
anything else is a substring. Case is ignored. Enter keeps the filter
and returns to the table; Esc clears it.

Press s to sort by the next column and S to reverse. The arrow in the
header shows the current order. Devices with no value in that column
always sort last.

# Keys

  ↑ ↓ j k      move; scroll details or select a log event when focused
  PgUp PgDn    a page at a time
  g G          first and last device; in the log, oldest event and follow
  Enter        focus the details pane          Esc   back to the table
  Tab          next pane
  /            filter                          Esc   clear the filter
  s S          sort by the next column, reverse
  x            show or hide raw packets in the details pane
  c            stop the scan so the screen holds still
  r            rescan
  t            theme picker: preview live, Enter keeps, Esc reverts
  ?            this manual                     q     quit

# Going deeper

Each probe has a written explanation in docs/protocols/ in the source
tree: arp.md, neigh.md, oui.md, rdns.md, mdns.md, nbns.md, icmp.md and
fake.md for the demo cast. Every probe also runs by itself from the
command line, printing what it sends, receives and concludes:

  shoal probe arp
  shoal probe mdns 192.168.1.20
  shoal probe oui 00:11:32:aa:bb:cc

For anything deeper than that, Wireshark shows the same packets in full
and Nmap will tell you what shoal deliberately does not.`

// help is the scroll state of the manual.
type help struct {
	open   bool
	scroll tideui.PaneScroller
}

func (h *help) toggle() {
	h.open = !h.open
	h.scroll.ScrollToTop()
}

// helpPanelWidth is the width of the manual's panel, borders excluded.
func (a *app) helpPanelWidth() int { return min(76, max(20, a.width-4)) }

// helpRows is how many manual lines fit in the panel.
func (a *app) helpRows() int { return max(3, a.height-8) }

func (a *app) handleHelpKey(msg tea.KeyMsg) {
	rows := a.helpRows()
	total := len(a.helpLines())
	switch msg.String() {
	case "esc", "?", "q":
		a.help.open = false
	case "down", "j":
		a.help.scroll.ScrollDown(1)
	case "up", "k":
		a.help.scroll.ScrollUp(1)
	case "pgdown", " ", "ctrl+d":
		a.help.scroll.ScrollDown(rows)
	case "pgup", "b", "ctrl+u":
		a.help.scroll.ScrollUp(rows)
	case "g", "home":
		a.help.scroll.ScrollToTop()
	case "G", "end":
		a.help.scroll.ScrollDown(total)
	}
	a.help.scroll.ClampTo(total, rows)
}

// helpLines lays the manual out for the current panel width: headings
// styled, paragraphs wrapped, indented lines wrapped with a hanging indent.
func (a *app) helpLines() []string {
	s := a.renderer.Styles
	width := a.helpPanelWidth() - 4
	heading := s.OverlayBody.Bold(true).Foreground(s.Theme.BorderFocus)
	if s.PlainUI {
		heading = s.OverlayBody.Bold(true)
	}

	var lines []string
	var para []string
	flush := func() {
		if len(para) == 0 {
			return
		}
		indent := ""
		if strings.HasPrefix(para[0], "  ") {
			indent = "  "
		}
		trimmed := make([]string, len(para))
		for i, l := range para {
			trimmed[i] = strings.TrimSpace(l)
		}
		for _, l := range wrap(strings.Join(trimmed, " "), max(8, width-len(indent))) {
			lines = append(lines, indent+l)
		}
		para = nil
	}
	for _, raw := range strings.Split(manual, "\n") {
		switch {
		case strings.HasPrefix(raw, "# "):
			flush()
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				lines = append(lines, "")
			}
			lines = append(lines, heading.Render(ansi.Truncate(strings.ToUpper(strings.TrimPrefix(raw, "# ")), width, "")))
		case strings.TrimSpace(raw) == "":
			flush()
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				lines = append(lines, "")
			}
		case strings.HasPrefix(raw, "  ") && len(para) > 0 && isKeyRow(raw):
			// Rows of the key table stay one per line.
			flush()
			para = append(para, raw)
			flush()
		case isKeyRow(raw):
			flush()
			lines = append(lines, ansi.Truncate(raw, width, "…"))
		default:
			para = append(para, raw)
		}
	}
	flush()
	return lines
}

// isKeyRow spots the aligned rows of the key table and the command
// examples, which must not be re-flowed into a paragraph.
func isKeyRow(line string) bool {
	if !strings.HasPrefix(line, "  ") {
		return false
	}
	body := strings.TrimSpace(line)
	return strings.Contains(body, "   ") || strings.HasPrefix(body, "shoal ")
}

// helpModal renders the manual as a soft panel with the visible window of
// lines and a footer saying where in the text the reader is.
func (a *app) helpModal() tideui.Overlay {
	width := a.helpPanelWidth()
	rows := a.helpRows()
	lines := a.helpLines()
	a.help.scroll.ClampTo(len(lines), rows)
	off := a.help.scroll.Offset()
	end := min(len(lines), off+rows)
	visible := append([]string(nil), lines[off:end]...)
	for len(visible) < rows {
		visible = append(visible, "")
	}
	hints := a.renderer.RenderSoftHints(width-4,
		tideui.SoftHint{Key: "↑↓", Label: "scroll"},
		tideui.SoftHint{Key: "esc", Label: "close"},
		tideui.SoftHint{Key: fmt.Sprintf("%d-%d", off+1, end), Label: fmt.Sprintf("of %d", len(lines))},
	)
	if a.renderer.Styles.PlainUI {
		hints = a.renderer.RenderSoftHints(width-4,
			tideui.SoftHint{Key: "^v", Label: "scroll"},
			tideui.SoftHint{Key: "esc", Label: "close"},
			tideui.SoftHint{Key: fmt.Sprintf("%d-%d", off+1, end), Label: fmt.Sprintf("of %d", len(lines))},
		)
	}
	content := a.renderer.RenderSoftBody(width, strings.Join(append(visible, "", hints), "\n"))
	return a.renderer.SoftPanelOverlay(tideui.SoftPanel{Prefix: "shoal", Title: "help", Content: content, Width: width})
}

func lipglossWidth(s string) int { return lipgloss.Width(s) }
