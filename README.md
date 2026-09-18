# Shoal

**A fast terminal LAN discovery tool that shows its work.**

Shoal finds the devices on the network segment you are plugged into and
fills in what they are, live, and does it **fast**: a typical /24 is swept
in about seven seconds, with names, makers, round trips and services
filling in as the answers arrive. Its defining feature is that **every
value on screen says how it was learned**: which probe found it, what was
sent and what came back, how sure it is, and when it expires. You can watch
the packets go out and the answers come back while it runs.

It was built with **AV-over-IP venues** in mind, where equipment moves
between sites and arrives carrying the wrong address:

- **"Which box has the wrong address?"** A camera that fell back to a
  `169.254.x.x` link-local address, or a stagebox still carrying a static IP
  from the last venue, is flagged `link-local` or `off-subnet`, found by
  listening to the traffic it cannot help sending. `--also 192.168.1.0/24`
  asks a venue's usual range too, for the device that never speaks.
- **"Which of these is the Dante stagebox?"** Dante and NDI devices are
  badged from what they announce about themselves, never guessed from a
  maker's name.
- **"What changed since last time?"** Shoal remembers each network, told
  apart by its gateway's MAC, and marks what is new, what moved, what was
  renamed and what is missing.

`shoal --demo`, a scripted network, after its scan has settled, with the
Dante stagebox selected:

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
│                                                                                        ││──────────────────────────────────────────────────────────│
│                                                                                        ││                                                          │
└────────────────────────────────────────────────────────────────────────────────────────┘└──────────────────────────────────────────────────────────┘
  DEMO  192.168.1.0/24  |  14 devices  |  scan 1 done                 ↑↓ move  ⏎ details  / filter  s sort  c stop  r rescan  t theme  ? help  q quit 
```

> Only scan networks you own or are authorised to test.

---

## Install

**macOS and Linux:** paste this into a terminal.

```
curl -fsSL https://github.com/BT10011/shoal/releases/latest/download/install.sh | sh
```

Then type `shoal` in any folder. **Windows** is coming with the next
release.

The installer:

1. downloads the build for your system and processor;
2. checks it against the release's published checksums, and installs
   nothing if they do not match;
3. puts it in `/usr/local/bin`, which is already on your PATH, asking for
   your password once;
4. on Linux, grants it the one capability it needs to send ARP without
   running as root (`cap_net_raw`);
5. if you install somewhere not on your PATH, adds that folder to your
   shell's startup file, once.

To install without administrator rights, choose a folder in your home
directory; the installer adds it to your PATH:

```
curl -fsSL https://github.com/BT10011/shoal/releases/latest/download/install.sh | SHOAL_INSTALL_DIR="$HOME/.local/bin" sh
```

`SHOAL_VERSION=v1.0.0` installs a particular release, and `SHOAL_NO_SUDO=1`
never uses sudo but says what needs it. Run the same line again to update.
To uninstall, delete `/usr/local/bin/shoal`.

**On macOS**, raw network access goes through the BPF devices, which are
root-only. Run `sudo shoal`, or install Wireshark's **ChmodBPF** package
once to use Shoal without sudo. Without either, Shoal runs in its reduced
mode, described below. Ping needs nothing.

**On FreeBSD**, run Shoal as root; it has no reduced mode there yet.

**From source**, with Go 1.27 or later:

```
git clone https://github.com/BT10011/shoal.git
cd shoal
make build           # bin/shoal
make setcap          # Linux: grant the capability to bin/shoal
```

---

## What Shoal is not

Shoal is deliberately narrow: it helps you see how each device was found
and identified, and nothing else. It is not a replacement for:

| Tool | How Shoal differs |
|---|---|
| **Nmap** | Shoal never scans ports, fingerprints an OS or probes for weaknesses. |
| **Wireshark** | Shoal shows only the packets its own probes send and receive, and explains each one. |
| **LanScan** | Not a clone: Shoal runs in a terminal and shows where every value came from. |
| **Fing** | Shoal does not identify models from a database; it states only what it can show evidence for. |
| **Angry IP Scanner** | Shoal stays on your network segment and never checks ports. |
| **arp-scan** | Shoal adds names, services, history and the reasoning behind each, live. |
| **Dante Controller** | Shoal only points out Dante devices; it cannot route or configure them. |

Nor is it a security or monitoring tool: it does not alert, block, score
or accuse. A device on the wrong subnet is far more often misconfigured
than malicious, and Shoal says so plainly.

---

## How it finds things

Each technique is a separate **probe**. Discoverers find devices; enrichers
add facts to a device already found. Each has a write-up in
[`docs/protocols/`](docs/protocols/), and each runs on its own with
`shoal probe <name>`.

| Probe | What it does | Needs |
|---|---|---|
| `arp` | Asks every address in the subnet "who has this IP?", then keeps listening, which is how wrong-subnet devices are heard | raw packet access |
| `neigh` | Reads the operating system's ARP cache after nudging each address: the fallback without raw access | nothing |
| `oui` | Looks the maker up from the MAC in the embedded IEEE registry | nothing |
| `rdns` | Asks your DNS server for each address's name | nothing |
| `mdns` | Asks each device its own name over multicast DNS | nothing |
| `dns-sd` | Listens to Bonjour and Avahi announcements, and asks once per scan which services are on offer | nothing |
| `nbns` | Asks Windows and Samba machines for their NetBIOS names | nothing |
| `icmp` | Measures the round trip, best of three pings | depends on OS |
| `rogue` | Flags addresses the subnet does not explain: link-local and off-subnet | nothing |
| `av` | Flags Dante and NDI devices from what they announce | nothing |
| `history` | Recognises the network and compares with earlier visits | nothing |

Without raw access Shoal still works, and says so in the "under the hood"
pane: the ARP cache stands in for the sweep, and everything else runs as
normal. What is lost is the listening that catches devices on the wrong
subnet, and `--also`.

On Linux with a firewall such as `ufw`, mDNS answers still arrive: Shoal
asks from port 5353 so answers come back by multicast, which such firewalls
allow.

---
## Using it

```
shoal                      scan the default interface's subnet
shoal --demo               a scripted network: no privileges, no packets sent
shoal --iface en0          choose the interface
shoal --also 192.168.1.0/24
                           also ask a venue's usual range, on this segment
shoal iface                show the interface, subnet and gateway Shoal would use
shoal probe <name>         run one probe on its own and print what it does
shoal version              which build this is
```

Other flags: `--unprivileged` to skip raw ARP, `--rate N` packets per second
(default 100), `--max-hosts N` for a subnet larger than a /22, `--theme
NAME` for one run, `--history PATH` and `--no-history`.

A theme kept in the picker (`t`, then Enter) is remembered in
`~/.config/shoal/config.toml` on Linux (`$XDG_CONFIG_HOME` if set), or
`~/Library/Application Support/shoal/config.toml` on macOS, and used every
time until you pick another. `--theme` changes one run without touching it.

In the TUI, press `?` for the full manual. The keys you will use most:

| Key | Does |
|---|---|
| `↑` `↓` `j` `k` | move, or scroll the focused pane |
| `Enter` | show how every value for the selected device was learned |
| `Tab` | move between panes; in "under the hood", `↑` pins the log to read one packet |
| `/` | filter, e.g. `/dante`, `/off-subnet`, `/192.168.1.` |
| `s` `S` | sort by the next column, reverse |
| `x` | show the raw packet behind each value |
| `c` `r` | stop the scan so the screen holds still, rescan |
| `t` | theme, previewed live |
| `q` | quit |

---

## History and privacy

Shoal remembers each network it scans and the devices seen on it, so the
next visit can say what changed. The file is SQLite, in your per-user data
directory, created readable by you only:

| Platform | Location |
|---|---|
| Linux, BSD | `$XDG_DATA_HOME/shoal/history.db`, else `~/.local/share/shoal/history.db` |
| macOS | `~/Library/Application Support/shoal/history.db` |

It holds, per network, each device's MAC, last address, name and maker, and
when it was first and last seen. Nothing leaves your machine. Run
`shoal probe history` to see what is in it, `--no-history` to keep nothing,
or delete the file to forget everything.

---

## Status

This is **release 1**. Planned next: the Windows release, then optional
modules such as LLDP, which would say which switch port and VLAN you are
plugged into. Deferred to later releases: port checks, device-type guesses,
IPv6, reading device models from mDNS, fuller vendor names, and configurable
columns. [`PROJECT_PLAN.md`](PROJECT_PLAN.md) has the detail and the
reasoning.

## Licence

Shoal is released under the [MIT licence](LICENSE): use it, change it and
pass it on, including commercially, as long as the copyright notice goes
with it. It comes with no warranty.

The libraries compiled into it are under MIT and BSD licences, whose texts
are in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) and in every
release archive.

## Credits

Shoal is built on:

- [TideUI](https://github.com/allisonhere/tideui) by allisonhere (MIT), for
  the panes, themes and theme picker;
- [Bubble Tea](https://github.com/charmbracelet/bubbletea),
  [Bubbles](https://github.com/charmbracelet/bubbles) and
  [Lip Gloss](https://github.com/charmbracelet/lipgloss) by Charm (MIT);
- [`golang.org/x/net`](https://pkg.go.dev/golang.org/x/net) and
  [`golang.org/x/sys`](https://pkg.go.dev/golang.org/x/sys) (BSD-3-Clause);
- [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite)
  (BSD-3-Clause), SQLite in pure Go;
- the IEEE's public registry of MAC address assignments, embedded for
  vendor lookup.
