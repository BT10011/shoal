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

Choose whichever suits you: each installs the same build, made by GitHub
Actions from the tagged source. On a Mac, Shoal needs macOS 13 (Ventura)
or later.

**macOS and Linux, with [Homebrew](https://brew.sh):**

```
brew install bt10011/tap/shoal
```

That installs Shoal from its own tap, BT10011/homebrew-tap. Naming the tap
in full is also how Homebrew 6 and later are told to trust Shoal's formula,
and only that one, from a tap that is not Homebrew's own. From then on,
`brew upgrade` keeps it up to date.

**Debian and Ubuntu:**

```
curl -fsSLO https://github.com/BT10011/shoal/releases/latest/download/shoal-linux-amd64.deb
sudo apt install ./shoal-linux-amd64.deb
```

**Fedora and RHEL:**

```
curl -fsSLO https://github.com/BT10011/shoal/releases/latest/download/shoal-linux-amd64.rpm
sudo dnf install ./shoal-linux-amd64.rpm
```

**openSUSE:**

```
curl -fsSLO https://github.com/BT10011/shoal/releases/latest/download/shoal-linux-amd64.rpm
sudo zypper install --allow-unsigned-rpm ./shoal-linux-amd64.rpm
```

zypper refuses a package not signed with a GPG key unless told to take
it. Shoal's packages are checked another way: see "Checking a download".

**Arch:**

```
curl -fsSLO https://github.com/BT10011/shoal/releases/latest/download/shoal-linux-amd64.pkg.tar.zst
sudo pacman -U shoal-linux-amd64.pkg.tar.zst
```

On 64-bit Arm, use `arm64` in place of `amd64`. A package grants Shoal
the network access it needs as it installs, so there is nothing else to do.

**Windows:** download `shoal-windows-amd64.zip` (`-arm64` for Arm) from
the [latest release](https://github.com/BT10011/shoal/releases/latest) and
unzip it. Shoal sends ARP through **[Npcap](https://npcap.com)**, which you
install once yourself, since its licence does not allow Shoal to include
it: leave "Restrict Npcap driver's access to Administrators only"
unticked. `shoal --setup` walks you through it.

**The install script**, for macOS, Linux and FreeBSD:

```
curl -fsSL https://github.com/BT10011/shoal/releases/latest/download/install.sh | sh
```

To read it, and check where it came from, before it runs:

```
curl -fsSLO https://github.com/BT10011/shoal/releases/latest/download/install.sh
gh attestation verify install.sh --repo BT10011/shoal
less install.sh
sh install.sh
```

It downloads the build for your system and checks it against the
release's published checksums. If the GitHub CLI is installed and signed
in, it also checks that Shoal's release workflow built it. If either check
fails, nothing is installed. It puts `shoal` in `/usr/local/bin`, asking
for your password once, then runs `shoal --setup`. The whole script runs
from its last line, so a download cut short runs nothing. Settings, before
`sh`:
- `SHOAL_INSTALL_DIR="$HOME/.local/bin"` installs without administrator
  rights and adds that folder to your PATH.
- `SHOAL_VERSION=v2.0.0` picks a release.
- `SHOAL_NO_SUDO=1` never uses sudo.

**From source**, with Go 1.27 or later:

```
git clone https://github.com/BT10011/shoal.git
cd shoal
make build           # bin/shoal
bin/shoal --setup    # full network access for that binary
```

### Full network access

Shoal sweeps the network with ARP, which takes raw network access, and it
does not scan without it. The first time it runs it says what is missing
and what it will change, and asks for your password once. `shoal --setup`
does the same whenever you like.

| System | What setup does |
|---|---|
| Linux | Gives the shoal program the `cap_net_raw` capability, so it never runs as root. The packages do this as they install. |
| macOS | Adds you to the `access_bpf` group, and installs a small start-up job that opens the packet capture devices, `/dev/bpf*`, to that group. This is what Wireshark's ChmodBPF does, and the two share the group. Any program you run can then capture packets, as with Wireshark. |
| Windows | Opens npcap.com for you to install Npcap; Windows asks for an administrator's password. |
| FreeBSD | Nothing to set up: run `sudo shoal`. |

### Checking a download

Every release file is built from its tag by GitHub Actions, and releases
are immutable: once published, no file in one can be changed. With the
[GitHub CLI](https://cli.github.com):

```
gh attestation verify shoal-linux-amd64.tar.gz --repo BT10011/shoal
gh release verify-asset v2.0.0 shoal-linux-amd64.tar.gz --repo BT10011/shoal
```

The first checks that Shoal's release workflow built the file, the second
that it is the file the release holds. Or compare it with the release's
`SHA256SUMS`: `sha256sum --ignore-missing -c SHA256SUMS` (on macOS,
`shasum -a 256 -c SHA256SUMS`).

Shoal is not yet signed with an Apple or Microsoft certificate. So a file
downloaded in a browser may warn the first time it runs:
- macOS says it cannot check the developer, or cannot check Shoal for
  malware. To allow it, run `xattr -d com.apple.quarantine shoal` in its
  folder, or after the first try, click Open Anyway in System Settings,
  under Privacy & Security.
- Windows SmartScreen says it protected your PC.

Homebrew, the Linux packages and the install script do not download
through a browser, so they show neither warning.

### Upgrading

```
shoal --upgrade           fetch the latest release and install it
shoal --upgrade --check   say what the latest release is, change nothing
```

`--upgrade` checks the download against the release's checksums before
changing anything. If a package manager installed Shoal, `--upgrade`
hands the update to it:
- Homebrew runs `brew upgrade`.
- apt, dnf, zypper or pacman installs the new package.

Where only the administrator can write, as in `/usr/local/bin`, it asks
for your password through sudo. On Linux the new copy keeps the network
access the old one had.

**From v1.0**, which knows the command only as `--update`:

```
sudo shoal --update
```

The first time v2.0 starts, it asks for your password to set up full
network access. On a Mac where v1.0 ran with `sudo`, which left Shoal's
settings and history belonging to root, it offers to give them back to you
too, even when Wireshark already gives it network access.

### Removing it

```
shoal --uninstall
```

Before deleting anything, it lists everything it would remove and asks
you to agree (in a script, `--yes` agrees in advance; an answer piped in
never counts):
- the program, through the package manager if one installed it;
- the remembered theme;
- the history of every network scanned;
- the PATH line the installer added;
- on macOS, the start-up job setup installed.

Npcap on Windows and the `access_bpf` group on macOS stay, since other
programs use them.

## Using it

```
shoal                      scan the default interface's subnet
shoal --demo               a scripted network: no privileges, no packets sent
shoal --iface en0          choose the interface
shoal --also 192.168.1.0/24   also ask another range, on this segment
shoal iface                the interface, subnet and gateway Shoal would use
shoal probe <name>         run one probe on its own and print what it does
shoal --setup              give Shoal the network access it needs, once
shoal version              which build this is
```

Also: `--rate N`, `--max-hosts N`, `--theme NAME`, `--history PATH`,
`--no-history`, `--pcap FILE`.

In the TUI: `Enter` shows how every value for the selected device was
learned, `x` adds the raw packets, `/` filters (`/dante`, `/off-subnet`),
`s` sorts, `Tab` moves between panes, `i` switches to another interface or
address this machine has, `d` removes a device that has stopped
answering (`D` removes them all), `w` records packets for Wireshark, `t`
picks a theme and `q` quits.

**Watching it listen.** Two probes keep listening after the sweep:
`dns-sd`, for Bonjour and Avahi announcements, and `arp`, for devices that
speak up later, such as one carrying an address from another network. In
the Under the hood pane, each of their rows is a wire: every message heard
flies in along it as a comet, towards the probe's name, beside a count of
everything heard so far. A quiet network leaves the wire empty.

**Devices that have gone.** A device the scan finished without hearing is
shown in red; Shoal never drops it by itself. Press `d` on it, or `D` for
all of them, and confirm. On a remembered network you can also forget it
there for good, so it is not listed as missing next visit. Every removal
is logged, and a device that answers again comes back.

**Following a scan in Wireshark.** Nothing is recorded unless you ask.
Press `w` (then `r` to rescan, so the whole process is captured, and `w`
again to stop), or start with `--pcap FILE` to record from start to quit.
Every packet Shoal sent and heard goes into a pcapng file, each with a
comment carrying Shoal's own reading of it, so Wireshark and Shoal can be
read side by side (`frame.comment` filters on it). ARP frames are exactly
as on the wire; the IP and UDP headers of Shoal's DNS, mDNS, NetBIOS and
ping traffic are rebuilt, since the operating system writes the real ones,
and the file says so. A capture lists every address and name on the
network, so check it before sharing it.

**Large networks.** Shoal sweeps up to a `/22` without asking. On anything
larger, up to a `/16`, it stops before sending anything, explains what a
full sweep costs (time, a broadcast every device on the segment must
handle, a full ARP table, and the chance that a switch or IDS treats it as
an attack), and lets you choose: the whole subnet at 500 requests a second
with no retry pass (about two minutes for a `/16`; `--rate` changes it),
only the `/24` around this machine, or listening only.
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
| `neigh` | Reads the OS's ARP cache after nudging each address. Scans use `arp`; this one runs on its own, to compare |
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

This is **release 2** (v2.0.0). New since release 1:

- **Windows**, with the same features as macOS and Linux, through Npcap.
- **Wireshark export**: `w` or `--pcap FILE` records every packet Shoal's
  probes send and hear as a pcapng file, each packet carrying Shoal's
  reading of it as a comment.
- **Interfaces and large networks**: `i` switches to any interface or
  address this machine has, and above a `/22` Shoal asks before sweeping.
- **Removing devices** that have stopped answering, with `d` and `D`.
- **Listeners you can watch**: every message `dns-sd` and `arp` hear flies
  in along their rows in Under the hood, as a comet.
- **Six new dark themes.**
- **Installing**: Homebrew, `.deb`, `.rpm` and Arch packages, and an
  install script that can be checked. Shoal now always scans with full
  network access, set up once with `shoal --setup`, and `shoal --upgrade`
  updates it however it was installed.

Next: optional modules such as LLDP, which would say which switch port and
VLAN you are plugged into.

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

Six of the themes are Shoal's own adaptations of other projects' palettes,
all MIT-licensed: **kanagawa-wave** from
[kanagawa.nvim](https://github.com/rebelot/kanagawa.nvim) by Tommaso Laurenzi,
**ayu-mirage** from [ayu](https://github.com/ayu-theme/ayu-colors) by Konstantin Pschera,
**oxocarbon** from [oxocarbon.nvim](https://github.com/nyoom-engineering/oxocarbon.nvim)
by Riccardo Mazzarini, **vesper** from [Vesper](https://github.com/raunofreiberg/vesper)
by Rauno Freiberg, **moonfly** from
[vim-moonfly-colors](https://github.com/bluz71/vim-moonfly-colors) by its authors, and
**synthwave-84** from [SynthWave '84](https://github.com/robb0wen/synthwave-vscode)
by Robb Owen.

## Licence

Shoal is released under the [MIT licence](LICENSE): use it, change it and
pass it on, including commercially, as long as the copyright notice goes
with it. It comes with no warranty. Every library compiled into it is
under an MIT or BSD licence; the texts are in
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) and in every release
archive.
