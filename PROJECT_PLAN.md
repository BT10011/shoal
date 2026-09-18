# Shoal — Project Plan

> Working name: **shoal** (placeholder; a shoal is a cluster of fish in shallow water — a local group).
> Language: **Go**. UI: **Bubble Tea + Lipgloss + TideUI**.
> This document is the source of truth for scope, architecture and build order.
> Claude Code: read this whole file before making changes, and follow "Working agreement" at the bottom.

---

## 1. What this project is

A **terminal-based, good-looking LAN discovery tool that shows its work.**

It discovers devices on the local subnet, enriches them live, and — as its defining feature — lets the user **watch and understand what is happening under the hood** while discovery runs:

- which packets / queries are being sent right now,
- which protocol produced each piece of information,
- how confident that information is, when it arrived and when it expires,
- where sources agree or conflict.

The value is **transparency and learning**: seeing how ARP, DNS, mDNS/Bonjour, NetBIOS, ICMP and friends fit together to build a picture of a network, with live updates and visible progress in a polished TUI.

### Target screen (conceptual)

```
┌ Devices ─────────────────────────────────────────────────┐┌ Details: 192.168.1.20 ─────────────┐
│ IP             MAC                HOSTNAME   VENDOR   RTT ││ hostname  synology.local            │
│ 192.168.1.1    AA:BB:CC:..        router     MikroTik 1ms ││   ← mDNS A answer        conf 0.9   │
│ 192.168.1.20   00:11:32:..        synology   Synology 2ms ││   ← PTR via 192.168.1.1  conf 0.7   │
│ 192.168.1.42   3C:22:FB:..        macbook    Apple    4ms ││ vendor    Synology Inc.             │
│                                                           ││   ← OUI 00:11:32 (IEEE)             │
├ Scan ─────────────────────────────────────────────────────┤├ Event log ─────────────────────────┤
│ ARP sweep ██████████████░░░░░░ 181/254   mDNS: listening  ││ 12:01:03 ARP who-has 192.168.1.57  │
│ Enrich: rdns 3 running · icmp 5 running · 41 queued       ││ 12:01:03 ARP reply .20 00:11:32:.. │
└───────────────────────────────────────────────────────────┘└────────────────────────────────────┘
 [r] Rescan  [Enter] Details  [/] Filter  [s] Sort  [t] Theme  [?] Help  [q] Quit
```

---

## 2. What this project is NOT (explicit non-goals)

**This is NOT a replacement for Nmap. This is NOT a clone or replacement for LanScan.**

- No attempt at Nmap-style feature parity: no full port-range scanning, no OS fingerprinting engine, no NSE-style scripting, no vulnerability detection, no evasion/stealth/timing-template features.
- No attempt to copy LanScan's UI, feature set or behaviour. LanScan is inspiration for the *idea* (fast, friendly LAN overview), nothing more.
- Not a security/pentest tool. Port checks are a small, curated, opt-in list used to *explain* what a device is.
- Not a network monitoring/alerting daemon (history exists to explain change, not to page anyone).
- Not a GUI or web app.

**Scope test for any new feature:** *Does it help the user see or understand how a device was discovered or identified?* If not, it probably doesn't belong. When in doubt, point users to Nmap/Wireshark for depth instead of re-implementing them.

---

## 3. Design principles

1. **Show your work.** Every displayed value carries provenance (source, method, time, confidence, optional raw packet). Nothing appears "by magic".
2. **Live, never blocking.** The table fills in progressively; progress is always visible; the UI never freezes waiting on a probe.
3. **Observations, not overwrites.** Probes emit evidence; the store resolves what to display. Conflicts are surfaced, not hidden.
4. **Modular probes.** New capabilities are new packages implementing a small interface. The core does not change.
5. **Polite by default.** Rate-limited, local-subnet only, active port checks opt-in.
6. **Least privilege.** Prefer capabilities (`setcap`) over running the TUI as root; offer an unprivileged degraded mode.
7. **Looks good.** Themed, calm, keyboard-first (visual inspiration: allisonhere's TideFTP / TideUI family).
8. **Learnable.** Each protocol probe has a matching doc in `docs/protocols/`.

---

## 4. Tech stack

| Concern | Choice | Notes |
|---|---|---|
| Language | Go (latest stable) | goroutines map naturally to probes; single static binary |
| TUI loop | `github.com/charmbracelet/bubbletea` **v1.3.10** | Pinned to the version TideUI v0.2.2 requires. Do not jump to v2 until TideUI does. |
| Widgets | `github.com/charmbracelet/bubbles` **v0.21.1** | Only `textinput`, for the Phase 3 filter; it pins the same Bubble Tea and Lipgloss versions as TideUI. Everything else (table, progress bars, log, manual) is plain strings through TideUI rows and styles, which keeps one theme and one line-width rule. Added 2026-09-18. |
| Styling | `github.com/charmbracelet/lipgloss` **v1.1.0** | Pinned to the version TideUI v0.2.2 requires. |
| Themed chrome | `github.com/allisonhere/tideui` **v0.2.2** | Panes, status bar, modals, theme picker. Licence verified 2026-09-16: **MIT**. Credit in README. Do not "Tide"-brand this project. |
| Routing table | `golang.org/x/net/route` | pure Go; reads the BSD/macOS routing socket to find the default gateway. Linux reads `/proc/net/route`. |
| Raw L2 (Linux) | `github.com/mdlayher/packet`, `github.com/mdlayher/arp` | pure Go, AF_PACKET |
| Raw L2 (macOS) | BPF via `/dev/bpf` (pure Go if feasible) or `github.com/gopacket/gopacket/pcap` | **TODO: verify** best pure-Go option on macOS; pcap needs cgo |
| Packet decode | `github.com/gopacket/gopacket` (maintained fork) | also used later for LLDP, 802.1Q, pcapng export |
| DNS / mDNS | `golang.org/x/net/dns/dnsmessage` | **Changed 2026-09-17** (was `github.com/miekg/dns`): `x/net` is already a dependency for the routing table, so this adds no new module, and its explicit pack/unpack suits a probe that must show the packet. Queries are hand-rolled either way, for full visibility (not a black-box zeroconf lib). Revisit for `mdns` if SRV/TXT parsing proves painful; the probe logic does not depend on the choice. |
| ICMP | `golang.org/x/net/icmp` + `golang.org/x/net/ipv4` | **Changed 2026-09-17** (was `github.com/prometheus-community/pro-bing`): same reasoning as the DNS row — `x/net` is already a dependency, and building the echo request by hand is what lets the probe show the id, sequence and socket kind. pro-bing would also hide which socket type it fell back to, which is exactly the privilege detail shoal wants to display. |
| Persistence | `modernc.org/sqlite` | pure Go, no cgo |
| Config | TOML (`github.com/pelletier/go-toml/v2`) | later phase |

Avoid cgo where possible to keep cross-compilation easy.

---

## 5. Architecture

```
            ┌───────────────── Discoverers ─────────────────┐
            │ arp-sweep   neigh-table   mdns-listen   fake  │   (later: lldp, pcap, dhcp-sniff)
            └───────────────────────┬───────────────────────┘
                                    │ Observations + ProbeEvents
                                    ▼
   ┌──────────────┐          ┌──────────────┐      DeviceUpdated / ProbeEvent / Progress
   │  Scheduler   │◄─────────│    Store     │───────────────────────────────┐
   │ (enrichers,  │ triggers │ single writer│                               ▼
   │ worker pools)│─────────►│  goroutine   │──► SQLite history      UI bridge (batch ~10 Hz)
   └──────────────┘          └──────────────┘                               │ program.Send
     oui  rdns  mdns-query  nbns  icmp  ports  classify                     ▼
                                                                    Bubble Tea model
```

### 5.1 Components

- **netif** — choose interface, detect subnet, gateway, own IP/MAC.
- **model** — `Device`, `Observation`, `Field`, resolution rules. No I/O.
- **store** — authoritative in-memory state; the *only* writer. Applies observations, detects conflicts/changes, publishes events. Persists history to SQLite.
- **engine** — probe registry, scheduler, per-probe concurrency limits, rate limiting, cancellation via `context`.
- **probe/\*** — one package per technique.
- **ui** — Bubble Tea app. Reads snapshots; never mutates engine state directly. Sends commands (rescan, cancel, toggle probe) to the engine.
- **UI bridge** — goroutine that coalesces store events and calls `program.Send(...)` at most ~10×/s to avoid render storms.

### 5.2 Core data model

```go
// internal/model

type Field string

const (
    FieldIP       Field = "ip"
    FieldMAC      Field = "mac"
    FieldHostname Field = "hostname"
    FieldVendor   Field = "vendor"
    FieldLatency  Field = "latency"
    FieldService  Field = "service"      // mDNS service instance
    FieldPort     Field = "port"
    FieldType     Field = "device_type"
    FieldFlag     Field = "flag"         // e.g. "locally-administered-mac", "duplicate-ip"
)

type Observation struct {
    DeviceKey  string        // MAC for on-link hosts; IP otherwise
    Field      Field
    Value      string
    Source     string        // "arp", "neigh", "mdns", "rdns", "nbns", "oui", "icmp", "ports", "classify"
    Method     string        // human explanation: "ARP reply from 192.168.1.20 on en0"
    Confidence float32       // 0..1
    Raw        []byte        // optional raw packet/answer for the detail view
    At         time.Time
    TTL        time.Duration // 0 = no expiry
}

type Device struct {
    Key       string
    Facts     map[Field][]Observation
    FirstSeen time.Time
    LastSeen  time.Time
}

// Resolved returns the display value for a field:
// unexpired only → highest confidence → source priority → newest.
func (d *Device) Resolved(f Field) (Observation, bool)
```

### 5.3 Probe interfaces

```go
// internal/engine

type Emit func(model.Observation)
type Report func(ProbeEvent) // "sent ARP who-has 192.168.1.57", progress ticks, errors

type Discoverer interface {
    Name() string
    Run(ctx context.Context, iface netif.Interface, emit Emit, report Report) error
}

type Enricher interface {
    Name() string
    Triggers() []model.Field  // run when these fields first appear/change
    Concurrency() int
    Enrich(ctx context.Context, d model.DeviceSnapshot, emit Emit, report Report) error
}

type ProbeEvent struct {
    Probe   string
    Kind    string   // "sent", "received", "progress", "error", "info"
    Target  string
    Message string
    Done, Total int  // for progress
    At      time.Time
}
```

`ProbeEvent`s power the event log and progress panel — this is how "under the hood" is made visible. They are UI/learning data, separate from `Observation`s (which are facts about devices).

### 5.4 Diagnostics that fall out of the model

- One IP claimed by multiple MACs → `duplicate-ip` flag (misconfig or ARP spoofing).
- MAC with locally-administered bit (`first octet & 0x02`) → randomized/private MAC; vendor lookup will not apply.
- Hostname disagreement between mDNS / PTR / NBNS → shown side by side.
- IP change for known MAC, device not seen this scan → change events (Phase 4).
- Address outside the interface's subnet, or a `169.254.x.x` self-assigned one → the device does not belong to this network (optional Phase 3a).

---

## 6. Repository layout

```
shoal/
├── cmd/shoal/main.go            # flags, privilege check, wiring
├── internal/
│   ├── netif/
│   ├── model/
│   ├── store/
│   │   ├── memory.go
│   │   └── sqlite.go
│   ├── engine/
│   ├── probe/
│   │   ├── fake/                # scripted devices for --demo
│   │   ├── arp/                 # arp.go, sender_linux.go, sender_darwin.go
│   │   ├── neigh/               # kernel neighbor table (unprivileged)
│   │   ├── oui/                 # go:embed IEEE registry + LAA check
│   │   ├── rdns/
│   │   ├── mdns/
│   │   ├── nbns/
│   │   ├── icmp/
│   │   ├── ports/
│   │   └── classify/
│   └── ui/
│       ├── app.go  table.go  detail.go  log.go  progress.go
│       ├── filter.go  keys.go  theme.go  bridge.go
├── data/oui.csv                 # refreshed via `go generate`
├── docs/protocols/              # arp.md, mdns.md, dns.md, nbns.md, icmp.md, ...
├── Makefile                     # build, test, race, setcap, gen-oui, demo
├── CLAUDE.md                    # short pointer to this plan + conventions
├── PROJECT_PLAN.md              # this file
└── go.mod
```

Every probe must also be runnable standalone for debugging and learning:

```
shoal                      # full TUI
shoal --demo               # TUI with fake discoverer; no root, no network
shoal probe arp  en0       # plain stdout output of one probe
shoal probe mdns
shoal probe oui 00:11:32:AA:BB:CC
```

---

## 7. Phased build plan

Each phase ends with working, tested, demoable software. Do not start a phase until the previous one meets its acceptance criteria.

### Phase 0 — Skeleton & demo mode ✅ *completed 2026-09-16*
- `netif`: detect interface, subnet, gateway, own IP/MAC; `shoal iface` prints them.
- `model` + `store` (in-memory) with unit tests for `Resolved()`, TTL expiry, conflict detection.
- `engine` with registry, scheduler and the `fake` discoverer/enrichers emitting realistic, staggered observations and probe events.
- TUI: device table, progress panel, event log, status/keys bar, using TideUI layout + theme.
- UI bridge with batching.

**Done when:** `shoal --demo` shows devices appearing and filling in live, progress moves, log scrolls, `q` quits cleanly; `go test -race ./...` passes.

### Phase 1 — Layer 2 discovery ✅ *completed 2026-09-17*
- Active ARP sweep of the subnet (rate-limited, one retry for silent hosts), emitting `sent`/`received` events.
- Unprivileged fallback: nudge IPs (e.g. UDP to a closed port) to populate the kernel neighbor cache, then read it (Linux: netlink or `/proc/net/arp`; macOS: routing sysctl). Label clearly as a different, less direct method.
- `oui` enricher (embedded IEEE data) + locally-administered MAC flag.
- Privilege detection with a clear message; `make setcap` for Linux (`cap_net_raw`).
- `docs/protocols/arp.md`.

**Done when:** a real /24 sweep completes in a few seconds with IP, MAC, vendor, and the detail view can say *how* each was learned.

*Outcome:* `shoal` sweeps a /24 in ~7 s (253 requests at 100/s, a 1 s settle, a retry pass for silent addresses, another settle); devices appear as they answer. Raw access uses `/dev/bpf` on macOS and `AF_PACKET` on Linux; when it is refused shoal falls back to `neigh` automatically and says so in the status bar. Modern FreeBSD/OpenBSD/NetBSD build but have no neighbour-cache reader (they moved ARP out of the routing table); `shoal probe arp` still works on FreeBSD.

### Phase 2 — Names & latency ✅ *completed 2026-09-17*

All four probes are done. Names now come from three independent sources that
cover different halves of a network — DNS knows what the router was told,
mDNS knows what Apple and avahi devices call themselves, NetBIOS knows the
Windows and Samba machines — and the RTT column is live.

- ✅ `rdns`: PTR lookups (show which resolver answered). *Done 2026-09-17:* asks `/etc/resolv.conf` resolvers in order (gateway as fallback), stops at the first definite answer, emits `hostname` at confidence 0.7 with the record's DNS TTL, and discards replies whose query ID does not match. IPv4 and UDP only.
- ✅ `mdns`: reverse queries **and** the passive listener.
  - *Reverse queries done 2026-09-17:* one-shot reverse PTR per device from an ephemeral port, `hostname` at confidence 0.9, proxy responders named, every section logged. Shared question-building lives in `internal/dnswire`.
  - *Passive listener done 2026-09-17:* binds 5353 with `SO_REUSEADDR`/`SO_REUSEPORT` alongside the system responder, joins the group on the scanning interface, and emits `ip`, `hostname` (from A records the sender claims) and `service`. A service is credited only when the sender's own records tie it to itself — see the attribution rule in `docs/protocols/mdns.md`, written after a live run showed a phone announcing a laptop's instance.
  - ⬜ Active service discovery: shoal never asks `_services._dns-sd._udp.local` itself, so on a quiet network the `service` field fills only when another device browses.
- ✅ `icmp`: RTT. *Done 2026-09-17:* three echo requests 100 ms apart, best of three, over an unprivileged datagram socket where possible and a raw socket otherwise, with the socket kind shown. Replies are matched by sequence and payload because the kernel rewrites the id. Recording the ARP RTT to contrast L2 with L3 is still open — the sweep does not keep per-address timings yet.
- ✅ `nbns`: NetBIOS node status (UDP 137). *Done 2026-09-17:* one node status request for the wildcard name from an ephemeral port (no privileges), decoding the whole name table. Emits the unique `<00>` name as `hostname` at confidence 0.8 and the adapter address the node reports as `mac` at 0.9, naming the workgroup and every registered service in the log. Samba's all-zero adapter address is treated as unanswered.
- ✅ Docs for each probe: `rdns.md`, `mdns.md`, `icmp.md`, `nbns.md`.

**Supporting work done in this phase:**
- `internal/dnswire` — the question-building shared by `rdns` and `mdns` (see §4).
- `store` reconciles keys: an observation keyed by a bare IP is routed to whichever device claims that address, and a MAC-keyed device absorbs an IP-keyed one when it claims its address, keeping an alias and publishing `EventDeviceMerged`. This is what lets a passive probe, which knows only a source address, contribute to a device ARP found by MAC (see §11).
- The progress panel shows a plain count for a probe that listens rather than sweeps, since there is no total to count towards.
- **Test fixtures are synthetic.** Probes were developed against live captures, but every fixture committed here is hand-built: no real MAC addresses, hostnames, hardware models or network addresses.

**Done when:** hostnames and mDNS services populate live, with conflicting names shown side by side in details. *(Met: names and services populate live from three sources. The side-by-side conflict view is Phase 3's detail pane — the store already keeps every competing observation, so it is a rendering job, not a data one.)*

**Deferred out of this phase, to pick up whenever it is worth it:**
- Active mDNS service discovery — shoal never asks `_services._dns-sd._udp.local` itself, so on a quiet network `service` fills only when another device browses.
- ARP round trips, to contrast a layer 2 answer with ICMP's layer 3 one. The sweep keeps no per-address timings yet.

### Phase 3 — Diagnostic UI polish ✅ *completed 2026-09-18*
- ✅ Sorting (`s` cycle key, `S` reverse), `/` live filter (substring; glob if `*?[` present).
- ✅ Detail view: facts grouped by field → source, method, age, TTL, confidence; `x` toggles raw packet/hex view.
- ✅ Freshness colouring (recent / stale / not answering).
- ✅ Rescan (`r`), cancel scan (`c`).
- ✅ Responsive layout for small terminals (tabbed mode).
- ✅ The three chrome features below. They are what make the TUI navigable without documentation, so treat them as part of the phase rather than polish to drop if time runs short.

*Outcome and decisions recorded during the phase:*

- **Scans are now a first-class engine concept.** `Engine.Rescan` cancels
  the running discoverers, waits for them to release their sockets, and
  runs every discoverer again under a fresh scan; it also re-enqueues every
  known device to every enricher whose trigger field it has, so names and
  round trips are refreshed and an expired DNS or mDNS name comes back.
  `Engine.Cancel` (the `c` **stop** key; the UI says "stopped" throughout)
  stops the discoverers and empties the enricher queues; a lookup already
  waiting on a reply finishes on its own timeout. It is silent once the
  scan has nothing running, so the key can be pressed freely. Its purpose
  for the user is to make the screen hold still. `Status` carries the scan
  number and start time. **Devices are never forgotten by
  a rescan** (principle 3: observations, not overwrites) — new answers sit
  beside the old with fresh timestamps, which is what makes freshness
  visible. Phase 4's "device missing" events should key off the same scan
  boundaries.
- **Freshness is judged on direct contact only.** `model.Direct(source)`
  names the probes that exchange packets with the device (arp, neigh, mdns,
  nbns, icmp, netif, ports); `Device.LastContact()` is the newest
  observation from one of them, expired or not. A resolver, the OUI table
  or the store answering says nothing about whether the device is there.
  The classes: *fresh* — contact since the current scan began; *stale* —
  not yet, scan still running (or cancelled/failed, which leave addresses
  unasked); *not answering* — the scan settled (every sweeping discoverer
  done and every enricher queue empty) without contact; *unheard* — nothing
  has ever exchanged packets with it. Marks: blank, `?`, `✗`, `-`; colours
  normal, muted, error, muted. The details pane states the reasoning in a
  sentence naming the probe and time.
- **Tabbed layout** below 80 columns or 16 rows. Tabs carry no hints (a
  hint would truncate the title in a third of the width); any pane header
  hint that does not fit next to its title is dropped, and the mode text
  moves to the first line of the hood pane instead.
- **Status bar** left: DEMO badge or interface + subnet (subnet dropped
  below 100 columns so the key bar keeps its main entries), device count or
  filter match count, scan label. Probe
  progress is no longer repeated there; the mode string ("arp sweep · icmp
  dgram") lives in the "Under the hood" header.
- **Key bar** drops whole entries, least important first (theme, details,
  sort, filter, move, rescan, stop, quit, help), and never truncates a
  word. Stop and rescan stay visible from 80 columns up; at 120 it keeps
  everything but theme and details. It changes with focus: details
  pane, filter editing and the log pane each show their own bindings.
- **The log can be read while the scan runs** (added 2026-09-18 after the
  first real scan). Tab reaches the "Under the hood" pane in every layout.
  It follows the newest event until ↑ pins it: the newest event is
  selected and shown in full, wrapped rather than cut off, followed by a
  line in words saying what kind of event it was and which address it
  concerned ("received from 192.168.1.20"). The usual keys move the
  selection, `g` goes to the oldest event, `G` follows again, as does
  stepping down onto the newest event; the header reads "pinned at
  <time>" meanwhile. The selection stays on the same event as new ones
  arrive and as the ring drops old ones (2000 kept). Events still carry no
  raw bytes; attaching the frame to `sent`/`received` events so `x` can
  show a hex dump in the log is the natural next step if wanted.
- **Enter** focuses the details pane (scroll it with the movement keys),
  **Esc** returns to the table or clears the filter, **Tab** cycles all
  three panes. The filter input keeps ↑↓ working on the table
  so a device can be picked while typing. The pattern is matched against
  the device key and every live value of every field, so services and flags
  filter too.
- **Theme choice lasts for the session** (`--theme` sets the initial one);
  persisting it waits for config (§4). The picker is TideUI's, rendered as
  a soft panel with the `shoal` prefix.
- **Help** is a written manual (`internal/ui/help.go`), wrapped and
  scrolled in-app, covering panes, the two probe kinds, columns, provenance,
  freshness, scans, filter and sort, keys, and where `docs/protocols/` is.
- **A listener's row carries a wave, not a bar** (2026-09-18). The passive
  mDNS probe never finishes, so a bar would either sit empty or lie. Its
  row reads "listening · N heard", a braille wave rolls away from the
  probe's name for as long as it runs (the UI ticks at 200 ms while one
  does) and goes flat when it stops. A discoverer is drawn this way when it
  has no total and its own message says "listening".
- **Enricher rows count answers, not work** (2026-09-18). "0 running · 0
  queued · 23 done" read as idle, and "done" never said whether anything
  came back. Each enricher may now declare the one field it exists to fill
  (`engine.Producer`: oui → vendor, rdns/mdns/nbns → hostname, icmp →
  latency, rogue → flag); a lookup counts as answered only when that field
  was emitted, so oui's flag for a random MAC is not a vendor. Rows read
  "23 asked · 2 named" ("answered" for icmp, "flagged" for rogue), with
  running, queued and failed only while non-zero, and are muted until the
  probe has produced something. Counts restart with each scan so they read
  against the devices on screen.
- **A finished sweep bar is solid in the theme's Unread colour** (2026-09-18),
  the green the log uses for packets received, so "done" reads at a glance.
- Hex view: 16, 8 or 4 bytes per line depending on pane width, capped at
  1 KiB per observation with a trailer saying how much was left out. Values
  with no raw packet (a table lookup, the store's own flags) say so.

#### Key bar — always visible

One line at the bottom listing the handful of bindings a new user needs, and
nothing more: move, details, filter, sort, theme, help, quit. It exists in
embryo already (`↑↓ move  tab pane  q quit`, added in Phase 0) and grows as
the keys land. It must not wrap at 80 columns — if it no longer fits, drop
bindings from it rather than wrapping, because the full list lives in the help
manual.

#### Theme picker — `t`

A modal list of the TideUI built-in themes. The important part is that it is a
**live preview, not a list of names**: moving the highlight re-renders the
whole TUI in that theme immediately, so the user sees the real thing against
real data.

- `↑/↓` or `j/k` — move the highlight, previewing as it goes
- `Enter` — keep the highlighted theme and close
- `Esc` — close and restore whatever theme was in use when the picker opened

The choice should survive a restart once config exists (§4); until then it
lasts for the session, and `--theme` still works.

#### Help — `?`

Not a key list: a **manual the user can read and learn from**, scrollable in
the same movement keys, closed with `Esc`, `?` or `q`. It should cover what
each pane shows, what a probe is and what the difference between a discoverer
and an enricher is, what every column means, how confidence, method and TTL
should be read, what each key does, and where `docs/protocols/` lives for
going deeper.

This is the in-app counterpart to the protocol docs, and it is what lets
someone learn how a network is discovered without leaving the terminal — which
is the point of the whole tool (§3.8). Worth writing properly rather than
generating from a key table.

**Done when:** a new user can pick any value on screen and discover exactly where it came from within two keypresses.

### Phase 3a — Rogue and off-subnet devices *(done 2026-09-18, except `--check-off-subnet`)*

**Why this phase matters more than "optional" suggests (the maintainer, 2026-09-18):**
shoal will be used most on AV-over-IP networks. Equipment goes in and out of
venues constantly, and devices get plugged in carrying an old static address,
a lease from another site, or nothing at all and fall back to link-local.
They never register properly and technicians struggle to find *which* box has
the wrong address. The goal is that a tech looks at the scan and says "there
it is: that PTZ camera is on 169.254.x.x", then goes and fixes it.

#### What counts as not belonging

| Condition | What it usually means |
|---|---|
| **IPv4 link-local**, `169.254.0.0/16` (RFC 3927) | The device asked for DHCP, got no answer, and configured itself. A dead DHCP server, a wrong VLAN, a cable in the wrong socket, or a device that was never configured. |
| **Off-subnet IPv4** — an address outside the scanning interface's subnet | A static address left over from another network or venue, a device moved between sites, a second uplink, a misconfigured VLAN. |
| **On-link but silent** — a MAC seen in frames that claims no IP | Present at layer 2 only. A device still probing for an address, passive taps, bridges, and devices mid-boot look like this. |
| ~~IPv6 link-local only~~ | *Deferred:* the mDNS listener is IPv4-only, so shoal has no IPv6 evidence yet. |

#### How it is found — passively, which is what a venue floor needs

A device with the wrong address still talks at layer 2 on the segment the
tech is plugged into, and none of it needs a matching subnet to be heard:

- It broadcasts ARP requests for its old gateway whenever it tries to send
  anything, and gratuitous ARP and RFC 5227 probes when it links up. The
  sweep's frame listener already decodes every ARP frame on the interface,
  but until this phase it **discarded** senders outside the subnet and
  probes from 0.0.0.0 — exactly the frames this phase is about. Now it
  keeps them, with a method saying the address is not in our subnet.
- AV gear multicasts constantly (mDNS/Bonjour, Dante, NDI, SSDP), and the
  mDNS listener records the source address of everything it hears.
- The kernel neighbour table holds entries for addresses shoal never probed.

**Decision:** the sweep only listens for the seven seconds it runs, and AV
gear ARPs sporadically, so a **continuous ARP listener** discoverer runs
next to the mDNS listener whenever raw access is available. It shares the
sweep's frame decoder and connection, sends nothing, and shows as a wave in
the hood pane. It reports as `arp`, like the sweep, so its facts carry the
same source, priority and freshness weight; the method strings say
"overheard".

**Caveat to state in the docs and the help:** all of this is layer 2. The
tech must be on the same VLAN or switch segment as the device; nothing
crosses a router. That is also the honest answer to "why can't shoal see it".

#### Shape

- A `rogue` **enricher** in its own package, triggered on `ip`, holding the
  interface's subnet. It performs no I/O: it compares every live address
  against what the subnet allows and emits `flag` observations,
  `link-local-ip` and `off-subnet-ip`, each with a `Method` that states the
  reasoning in full, e.g. *"169.254.11.8 is inside 169.254.0.0/16, which a
  host assigns itself when no DHCP server answers; this interface's subnet
  is 172.16.10.0/24"*. Flags are never retracted (the model has no
  retraction), which is right for these: a device that was on the wrong
  address at 12:01 is worth remembering after the tech fixes it.
- "No IP" is **derived in the UI** rather than stored as a flag, for the
  same reason: a flag could not be withdrawn when the address arrives a
  second later. A row with an empty IP cell and a sentence in the details
  pane ("seen at layer 2 only") says it.
- UI: a **FLAGS column** in the table (kept in preference to MAC and RTT
  when space is short), `/` filtering by flag (already works: the filter
  matches every value), and the details pane explaining what the flag
  means and which probe saw the address, in the flag's own method line.
  Nothing blinks red; the tone stays diagnostic.
- The demo cast gains a PTZ camera on a link-local address and a device
  with a stale static address from another site, overheard by the fake
  sweep, so `shoal --demo` shows the feature.
- `shoal probe rogue <ip> [subnet]` and `docs/protocols/rogue.md`.

#### Second step, opt-in *(`--also` approved by the maintainer and built 2026-09-18)*

An idle device with a static address may never ARP, so passive listening
can miss it. Two active follow-ups, both off by default because §9 forbids
sending outside the local subnet unprompted, and both rate-limited:

- `--check-off-subnet`: one unicast ARP who-has to an overheard off-subnet
  address to confirm it is live now.
- `--also <cidr>`: sweep an extra range with ARP, e.g. the venue's usual
  `192.168.1.0/24`, on the local segment. ARP answers regardless of the
  sender's subnet, so this finds stale static devices that are otherwise
  silent. Refuses ranges above the `--max-hosts` limit like the main sweep.

#### Still not a security tool (§2)

Shoal reports what it saw and why the subnet does not explain it. It does not
alert, block, score, accuse, or guess at intent — an unexpected address is far
more often a misconfiguration than an intruder, and saying so plainly is more
useful than a warning icon. Anyone who needs to go further should be pointed
at Nmap and Wireshark.

**Done when:** a device with a `169.254.x.x` address, or one outside the
scanning subnet, is flagged in the table within moments of it speaking on
the segment, and the detail view explains in plain words what that means and
which probe saw it.

*Outcome, 2026-09-18 (passive part):* the sweep's frame listener keeps every
ARP frame and explains the unusual ones (off-subnet senders, gratuitous
announcements, RFC 5227 probes from 0.0.0.0, which record the MAC only);
`arp.Listener` keeps listening after the sweep whenever raw access is
available, shown as a second `arp` row with a wave, and stands aside while
the sweep is running (`Sweep.Sweeping`) so no frame is logged twice; `internal/probe/rogue`
judges every live address and emits `link-local-ip` and `off-subnet-ip`
with the full reasoning and the provenance of the address; the table has a
FLAGS column (badges `link-local`, `off-subnet`, `dup-ip`, `self`,
`rand-mac`, wrong addresses first) that outlives VENDOR when space is short,
`/link-local` filters, the details pane says "seen at layer 2 only" for a
MAC with no address, and the manual has a "Devices that do not belong"
section with the same-segment caveat. The demo cast gained a link-local
Sony PTZ camera and an off-subnet Dante box, overheard part-way through the
sweep. `shoal probe rogue <ip> [subnet]` and `docs/protocols/rogue.md`
exist. The sidebar ratio went from 0.56 to 0.6 so RTT still fits beside the
new column at 120 columns. An end-to-end UI test runs the real engine over
the demo cast and checks both boxes are flagged, filterable and explained.
*`--also`, 2026-09-18:* `arp.Options.Also` adds ranges to the sweep's
passes and retry, on this segment only. Decisions: the extra requests are
**RFC 5227 probes (sender 0.0.0.0)** — they claim no address on the foreign
network, plant nothing in ARP caches, must be answered by the holder, and
bypass Linux's reverse-path check on ARP requests from off-subnet senders.
Never sender = target (it would look like an address conflict and could
make a link-local device give its address up). Replies addressed to our MAC
with target 0.0.0.0 are recorded at 1.0, "answering our probe". Each range
is refused above `--max-hosts`; addresses inside the subnet are not asked
twice; a bare address means /32. `--also` without raw access is an error at
startup, not a silent downgrade. The hood's mode reads "· also <cidr>".

*Presentation, 2026-09-18:* the ARP listener is drawn as a continuation
line (`└`) under the sweep's bar rather than a second `arp` row, since it is
the same probe carrying on: flat and "listens once the sweep ends" while the
sweep runs, then a wave and "still listening · N heard". The maintainer read two `arp`
rows as a bug on the first real run.

Not done: `--check-off-subnet`, a single unicast ARP to confirm an
overheard off-subnet address is live now. `--also` covers the case that
motivated it (the device that never speaks); revisit only if wanted.

### Phase 4 — History
- SQLite persistence keyed by network identity (gateway MAC + subnet) and device MAC.
- First seen / last seen columns; "new since last scan" badge.
- Change events: IP changed, hostname changed, device missing, duplicate IP.

### Phase 5 — Services & device type
- `ports`: small curated, opt-in TCP connect list (e.g. 22, 53, 80, 443, 445, 548, 554, 631, 5000, 5001, 8080, 9100), rate-limited. **Not** a general port scanner.
- `classify`: explainable rules combining vendor + mDNS service types (`_airplay._tcp`, `_ipp._tcp`, `_googlecast._tcp`, `_smb._tcp`, `_hap._tcp`, …) + ports. `Method` must state the reasoning, e.g. `vendor Synology + _smb._tcp + port 5000 → NAS`.

### Phase 6 — Advanced modules (each optional, each its own package)
- **LLDP/CDP** passive listener (ethertype 0x88CC) → switch name, port, VLAN.
- **VLAN hints** from 802.1Q tags in captured frames (best-effort; many NICs strip tags — say so in the UI).
- **AV/pro-network services** via the mDNS pipeline and listeners: Dante (`_netaudio-*._udp`), NDI (`_ndi._tcp`), AES67/SAP (239.255.255.255:9875), PTP hints (UDP 319/320). **TODO: verify** specifics when implementing.
- **pcapng export** of all packets the probes sent/received, so a scan can be opened in Wireshark next to the TUI's interpretation.

---

## 8. UI specification (initial)

- **Layout:** device table (main, widest) · details pane · scan/progress panel · event log · bottom key bar. Use a TideUI layout mode; fall back to tabbed on small terminals.
- **Default columns:** IP · MAC · Hostname · Vendor · RTT · (later) Type · First seen · Last seen. Column set configurable later.
- **Keys:** `↑/↓ j/k` move · `PgUp/PgDn g G` page and ends · `Enter` focus details · `Tab` next pane · `/` filter · `Esc` back/clear/close · `s`/`S` sort · `x` raw packets · `r` rescan · `c` stop · `G` follow the log again · `t` theme · `?` help · `q` quit.
- **Key bar:** always visible, one line, the basic bindings only; never wraps at 80 columns.
- **Theme picker (`t`):** previews each theme live as the highlight moves; `Enter` commits, `Esc` restores the previous theme.
- **Help (`?`):** a scrollable manual, not a key list — panes, probes, columns, provenance, keys, and a pointer to `docs/protocols/`.
- **Progress panel:** per-discoverer progress bars; per-enricher running/queued counts.
- **Event log:** timestamped `ProbeEvent`s, filterable by probe; capped ring buffer.
- **Rules:** UI never blocks; all engine communication via messages; rendering throttled.

---

## 9. Safety, privileges & etiquette

- Only scan networks you own or are authorized to test; say so in README and `--help`.
- Default to local subnet only; refuse ranges larger than a configurable limit (e.g. /22) without an explicit flag.
- Rate-limit all active probes; port checks off by default.
- Linux: `setcap cap_net_raw+ep` on the binary. macOS: BPF device access. Never require running the whole TUI as root if avoidable.
- Unprivileged mode must still work (neighbor table + mDNS + DNS + ICMP where permitted), clearly labelled as reduced.

---

## 10. Testing

- `model`/`store`: pure unit tests (resolution, TTL, conflicts, change detection).
- Probes: parse/serialize tests against captured packet fixtures in `testdata/`; network I/O behind small interfaces so logic is testable without sockets.
- Engine: tests using the `fake` probes, run with `-race`.
- UI: golden-file tests of `View()` output at fixed sizes where practical; `--demo` for manual checks.
- Optional, tagged LAN integration tests (`go test -tags lan ./...`), never required for CI.

---

## 11. Open questions (resolve before/during Phase 1)

- ~~Primary dev/run OS (Linux vs macOS) → decides which raw-socket backend is written first.~~ **Resolved 2026-09-16:** The maintainer runs both macOS and Linux. Write the Darwin (BPF) ARP backend first, then Linux (`AF_PACKET`); both are first-class targets and must keep building.
- ~~TideUI license and compatible Bubble Tea/Lipgloss versions.~~ **Resolved 2026-09-16:** MIT; TideUI v0.2.2 requires Bubble Tea v1.3.10 and Lipgloss v1.1.0 (see §4).
- ~~How should a passive probe's observations reach a device the store keys by MAC?~~ **Resolved 2026-09-17:** the store reconciles them, and probes stay simple. An observation keyed by a bare IP is routed to whichever device has claimed that address (`resolveKey`); when a MAC-keyed device later claims an address that already exists as its own IP-keyed device, the store folds that device in, keeps an alias so in-flight observations still land, and publishes `EventDeviceMerged`. Ambiguity is never guessed at: if two devices claim one address, the IP-keyed facts stay separate and the `duplicate-ip` flag stands. Phase 4 should key history off the surviving MAC.
- Final project name.
- Windows support: out of scope initially (would need Npcap).

---

## 12. Working agreement for Claude Code

- Read this file first. Stay inside the current phase; do not add features from later phases or outside the non-goals in §2.
- Work in small, reviewable steps; keep `go build ./...`, `go vet ./...` and `go test -race ./...` passing after each step.
- Keep `model` free of I/O; only `store` mutates device state; UI never touches engine internals.
- Every new probe: its own package, implements `Discoverer` or `Enricher`, emits `ProbeEvent`s for visibility, has fixture-based tests, a `shoal probe <name>` subcommand, and a `docs/protocols/<name>.md`.
- Every emitted `Observation` must have a meaningful `Source` and human-readable `Method`.
- Prefer pure-Go dependencies; ask before adding cgo or large dependencies.
- If a design question isn't answered here, ask rather than guess, and record the decision in this file.
