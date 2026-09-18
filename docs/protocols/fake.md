# fake — demo mode probes

`shoal --demo` and `shoal probe fake` run the probes in `internal/probe/fake`.
They send **no packets**. Everything they report is scripted so that the UI,
store and engine can be exercised without root, a network, or a real LAN.

## What it simulates

| Probe | Pretends to be | Trigger | What it emits |
|---|---|---|---|
| `arp` (discoverer) | ARP sweep of `192.168.1.0/24` | — | `mac`, `ip` for each scripted device; one `sent` event per address, `received` per reply, `progress` 1..254 |
| `oui` | **not faked** — the real enricher runs offline against the embedded registry ([oui.md](oui.md)); the cast uses genuine vendor prefixes | `mac` | `vendor`, or the `locally-administered-mac` flag |
| `rdns` | PTR query to the router resolver | `ip` | `hostname` (confidence 0.7, TTL 5m) or NXDOMAIN |
| `mdns` | Multicast reverse PTR + DNS-SD enumeration | `ip` | `hostname` (confidence 0.9, TTL 2m) and `service` values |
| `icmp` | Echo request/reply | `ip` | `latency`; silent devices time out |

Every `Observation.Method` contains the word **demo**, e.g.
`simulated ARP reply from 192.168.1.20 (demo)`, so the provenance shown in the
UI is honest about being invented.

## What the cast is designed to show

- **Source priority**: the NAS has both a PTR name (`nas.lan`) and an mDNS name
  (`synology.local`); mDNS wins because it is more confident.
- **Conflict**: wherever the resolver and mDNS disagree (the printer says
  `printer.lan` vs `HP-LaserJet-M404.local`, the NAS `nas.lan` vs
  `synology.local`) the store raises a conflict and the UI shows both.
- **Duplicate IP**: two smart plugs both answer for `192.168.1.230`. The store
  flags both with `duplicate-ip`.
- **Randomised MAC**: the phone at `.101` has a locally-administered address, so
  no vendor can be looked up, and it drops ICMP.
- **Devices that do not belong** (Phase 3a): a Sony PTZ camera at
  `169.254.37.12` that got no DHCP answer and fell back to a link-local
  address, and an Audinate (Dante) box at `192.168.0.77` still carrying a
  static address from another venue. The sweep never asks them; part-way
  through it "overhears" their own ARP (the camera announcing itself, the
  Dante box asking for `192.168.0.1`), exactly as a real segment gives such
  devices away. The `rogue` enricher then flags them `link-local-ip` and
  `off-subnet-ip`. The camera still answers mDNS, as Bonjour works on
  link-local; neither answers ICMP, since nothing routes to them.
- **Timing**: requests are paced (default 15 ms per address, 150 ms lookups with
  ±50 % jitter) so progress and the event log are readable.

## A previous visit

`shoal --demo` also runs the real `history` probe over an in-memory history
seeded by `fake.SeedHistory` with an invented visit three days earlier: an
Epson projector at `.88` that is not in today's cast (missing), the printer
at `.51` (now `.50`, so new address), the Raspberry Pi as `octopi.local`
(now `raspberrypi.local`, so renamed), and no Chromecast, PTZ camera or
Dante box (new). The demo interface has its gateway at `.1`, whose MAC
identifies the network. The real history file is never touched.

## Running it

```
shoal probe fake                 # print events and observations to stdout
shoal probe fake -interval 2ms   # faster sweep
shoal --demo                     # the full TUI on fake data
```

## Options

`fake.Options{Devices, Interval, Latency, Seed}` — tests pass microsecond
timings and a fixed seed; `DefaultDevices()` is the demo cast.
