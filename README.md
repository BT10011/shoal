# Shoal

**A fast terminal LAN discovery tool that shows its work.**

Shoal finds the devices on the network segment you are plugged into and
fills in what they are, live: a typical /24 is swept in about seven
seconds. Its defining feature is that **every value on screen says how it
was learned** — which probe found it, what was sent, what came back, how
sure it is and when it expires — so it is as much a way to *learn* how a
network is discovered as a way to list what is on one. ARP, DNS,
mDNS/Bonjour, NetBIOS and ICMP each know a different part of the picture,
and you can watch each one answer as it runs.

```
┌────────────────────────────────────────────────────────────────────────────────────────┐┌──────────────────────────────────────────────────────────┐
│> Devices                                                                             14││  Details                                     192.168.0.77│
│  IP ▾            HOSTNAME        VENDOR         RTT     FLAGS            SERVICES      ││ Stagebox-FOH.local  00:1d:c1:12:34:56                    │
│  169.254.37.12   PTZ-CAM-1.local Sony Corporat…         link-local +2    ndi,rtsp      ││first seen 20:56:06 · last heard 20:56:06 (now)           │
│  192.168.0.77    Stagebox-FOH.l… Audinate Pty L         off-subnet +2    dante         ││  heard from directly now via mdns, since scan 1 began    │
│  192.168.1.1     gateway.lan     Routerboard.c… 0.8ms                                  ││                                                          │
│  192.168.1.20    synology.local… Synology Inco… 2.5ms                    afpovertcp,ht…││ip         192.168.0.77                                   │
│  192.168.1.42    studio-macbook… Apple, Inc.    5.5ms                    companion-lin…││  ← simulated ARP request from 192.168.0.77 overheard     │
│  192.168.1.50    HP-LaserJet-M4… Hewlett Packa… 3.8ms   new-ip           http,ipp,pdl-…││    during the sweep, asking who-has 192.168.0.1 (demo);  │
│  192.168.1.60    Chromecast-Liv… Google, Inc.   11.9ms  new              googlecast    ││    192.168.0.77 is outside 192.168.1.0/24, yet the frame │
│  192.168.1.77    Philips-hue.lo… Philips Light… 7.2ms                    hap,hue       ││    arrived on this segment                               │
│✗ 192.168.1.88    EPSON-PROJ.loc… Seiko Epson C…         missing                        │└──────────────────────────────────────────────────────────┘
│  192.168.1.101   iPhone.local                           rand-mac                       │┌──────────────────────────────────────────────────────────┐
│  192.168.1.150   raspberrypi.lo… Raspberry Pi … 2.9ms   renamed          sftp-ssh,ssh  ││  Under the hood                simulated, no packets sent│
│  192.168.1.200   Sonos-Kitchen.… Sonos, Inc.    5.8ms                    sonos,spotify…││arp    ⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿ 254/254 done│
│  192.168.1.230   plug-a.lan      Espressif Inc. 16.4ms  dup-ip                         ││history visit 2 · 11 known · 3 new · 1 missing            │
│  192.168.1.230   plug-b.lan      Espressif Inc. 13.0ms  dup-ip                         ││oui    13 asked · 12 named                                │
│                                                                                        ││rogue  13 asked · 2 flagged                               │
│                                                                                        ││av     12 asked · 2 flagged                               │
│                                                                                        ││rdns   13 asked · 7 named                                 │
│                                                                                        ││mdns   13 asked · 10 named                                │
│                                                                                        ││icmp   13 asked · 10 answered                             │
└────────────────────────────────────────────────────────────────────────────────────────┘└──────────────────────────────────────────────────────────┘
  DEMO  192.168.1.0/24  |  14 devices  |  scan 1 done                 ↑↓ move  ⏎ details  / filter  s sort  c stop  r rescan  t theme  ? help  q quit 
```

`shoal --demo` gives you that screen with a scripted network: no
privileges, no packets sent.

> Only scan networks you own or are authorised to test.

## What it tells you

- **Who is here, and what are they?** Address, MAC, maker, name and round
  trip, with each value's source shown on demand.
- **What does each device offer?** The services it announces over
  mDNS/Bonjour. Dante and NDI gear is badged from what it says about
  itself, never guessed from a maker's name — handy if you work with
  audio or video over IP, and ignorable if you do not.
- **What does the subnet not explain?** A device that fell back to a
  `169.254.x.x` link-local address, or arrived carrying a static address
  from somewhere else, is flagged `link-local` or `off-subnet` — found by
  listening to traffic it cannot help sending. `--also 192.168.1.0/24`
  asks another range too, for the device that never speaks.
- **What changed since last time?** Shoal remembers each network, told
  apart by its gateway's MAC, and marks what is new, what moved, what was
  renamed and what is missing.

## Install

**macOS and Linux:** paste this into a terminal.

```
curl -fsSL https://github.com/BT10011/shoal/releases/latest/download/install.sh | sh
```

Then type `shoal` anywhere. **Windows** is coming with the next release.

The installer downloads the build for your system, checks it against the
release's published checksums and installs nothing if they do not match,
puts it in `/usr/local/bin` (asking for your password once), and on Linux
grants it the one capability it needs to send ARP without running as root.
To install without administrator rights, add
`SHOAL_INSTALL_DIR="$HOME/.local/bin"` before `sh` and it will add that
folder to your PATH; `SHOAL_VERSION=v1.0.0` picks a release and
`SHOAL_NO_SUDO=1` never uses sudo.

**Updating and removing.** Shoal updates itself:

```
shoal --update           fetch the latest release and replace this binary
shoal --update --check   say what the latest release is, change nothing
shoal --uninstall        remove shoal, its settings and its history
```

`--update` verifies the download against the release's checksums before
replacing anything, and re-grants the Linux capability it needs (use
`sudo shoal --update` if Shoal lives somewhere only root can write).
`--uninstall` lists everything it would delete — the program, the
remembered theme, the history of every network scanned, and the PATH line
the installer added — and removes nothing until you agree.

**Per-platform notes.** On **macOS**, raw network access goes through the
BPF devices, which are root-only: run `sudo shoal`, or install
Wireshark's **ChmodBPF** once. On **FreeBSD**, run Shoal as root. Without
raw access Shoal still works in a reduced mode, reading the kernel's ARP
cache instead of sweeping, and says so on screen.

**From source**, with Go 1.27 or later:

```
git clone https://github.com/BT10011/shoal.git
cd shoal
make build           # bin/shoal
make setcap          # Linux: grant the capability to bin/shoal
```

## Using it

```
shoal                      scan the default interface's subnet
shoal --demo               a scripted network: no privileges, no packets sent
shoal --iface en0          choose the interface
shoal --also 192.168.1.0/24   also ask another range, on this segment
shoal iface                the interface, subnet and gateway Shoal would use
shoal probe <name>         run one probe on its own and print what it does
shoal version              which build this is
```

Also: `--unprivileged`, `--rate N`, `--max-hosts N`, `--theme NAME`,
`--history PATH`, `--no-history`.

In the TUI: `Enter` shows how every value for the selected device was
learned, `x` adds the raw packets, `/` filters (`/dante`, `/off-subnet`),
`s` sorts, `Tab` moves between panes, `t` picks a theme and `q` quits.
Press **`?`** for the full manual, which explains every pane, column and
key without leaving the terminal.

## How it finds things

Each technique is a separate **probe**. Discoverers find devices;
enrichers add facts to one already found. Each has a write-up in
[`docs/protocols/`](docs/protocols/) and runs on its own with
`shoal probe <name>`.

| Probe | What it does |
|---|---|
| `arp` | Asks every address in the subnet "who has this IP?", then keeps listening — which is how wrong-subnet devices are heard. Needs raw packet access |
| `neigh` | Reads the OS's ARP cache after nudging each address: the fallback without raw access |
| `oui` | Looks the maker up from the MAC in the embedded IEEE registry |
| `rdns` | Asks your DNS server for each address's name |
| `mdns` | Asks each device its own name over multicast DNS |
| `dns-sd` | Listens to Bonjour and Avahi announcements, and asks once per scan which services are on offer |
| `nbns` | Asks Windows and Samba machines for their NetBIOS names |
| `icmp` | Measures the round trip, best of three pings |
| `rogue` | Flags addresses the subnet does not explain: link-local and off-subnet |
| `av` | Flags Dante and NDI devices from what they announce |
| `history` | Recognises the network and compares it with earlier visits |

## What Shoal is not

Shoal is deliberately narrow: it helps you see how each device was found
and identified, and nothing else.

| Tool | How Shoal differs |
|---|---|
| **Nmap** | Never scans ports, fingerprints an OS or probes for weaknesses |
| **Wireshark** | Shows only the packets its own probes send and receive, and explains each one |
| **LanScan** | Not a clone: runs in a terminal and shows where every value came from |
| **Fing** | Identifies nothing from a database; states only what it has evidence for |
| **Angry IP Scanner** | Stays on your network segment and never checks ports |
| **arp-scan** | Adds names, services, history and the reasoning behind each, live |
| **Dante Controller** | Only points Dante devices out; it cannot route or configure them |

Nor is it a security or monitoring tool: it does not alert, block, score
or accuse. A device on the wrong subnet is far more often misconfigured
than malicious, and Shoal says so plainly.

## History and privacy

Shoal remembers each network it scans and the devices seen on it, so the
next visit can say what changed. The file is SQLite, in your per-user data
directory (`~/.local/share/shoal/history.db` on Linux and the BSDs,
`~/Library/Application Support/shoal/history.db` on macOS), created
readable by you only. It holds, per network, each device's MAC, last
address, name and maker, and when it was first and last seen.

**Nothing leaves your machine.** `shoal probe history` shows what is in
it, `--no-history` keeps nothing, and deleting the file forgets
everything.

## Status

This is **release 1** (v1.0.0). Planned for release 2:

- **Windows**, with the same features as macOS and Linux.
- **Wireshark export**: save every packet Shoal's probes sent and received
  as a pcapng file, to open in Wireshark next to Shoal's explanation of
  each one.

After that, optional modules such as LLDP, which would say which switch
port and VLAN you are plugged into.

## Credits

Shoal is built with **[TideUI](https://github.com/allisonhere/tideui)** by
**[allisonhere](https://github.com/allisonhere)** — the panes, themes and
the live theme picker are all TideUI, and Allison contributes to Shoal
itself as well.

Also compiled in: [Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Bubbles](https://github.com/charmbracelet/bubbles) and
[Lip Gloss](https://github.com/charmbracelet/lipgloss) by Charm;
[`golang.org/x/net`](https://pkg.go.dev/golang.org/x/net) and
[`golang.org/x/sys`](https://pkg.go.dev/golang.org/x/sys);
[`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite), SQLite in
pure Go; and the IEEE's public registry of MAC address assignments, for
vendor lookup.

## Licence

Shoal is released under the [MIT licence](LICENSE): use it, change it and
pass it on, including commercially, as long as the copyright notice goes
with it. It comes with no warranty. Every library compiled into it is
under an MIT or BSD licence; the texts are in
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) and in every release
archive.
