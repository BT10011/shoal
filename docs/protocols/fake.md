# fake — demo mode probes

`shoal --demo` and `shoal probe fake` run the probes in `internal/probe/fake`.
They send **no packets**. Everything they report is scripted so that the UI,
store and engine can be exercised without root, a network, or a real LAN.

## What it simulates

| Probe | Pretends to be | Trigger | What it emits |
|---|---|---|---|
| `arp` (discoverer) | ARP sweep of `192.168.1.0/24` | — | `mac`, `ip` for each scripted device; one `sent` event per address, `received` per reply, `progress` 1..254 |
| `oui` | IEEE OUI registry lookup | `mac` | `vendor`; or the `locally-administered-mac` flag when the first octet has bit `0x02` set |
| `rdns` | PTR query to the router resolver | `ip` | `hostname` (confidence 0.7, TTL 5m) or NXDOMAIN |
| `mdns` | Multicast reverse PTR + DNS-SD enumeration | `ip` | `hostname` (confidence 0.9, TTL 2m) and `service` values |
| `icmp` | Echo request/reply | `ip` | `latency`; silent devices time out |

Every `Observation.Method` contains the word **demo**, e.g.
`simulated ARP reply from 192.168.1.20 (demo)`, so the provenance shown in the
UI is honest about being invented.

## What the cast is designed to show

- **Source priority**: the NAS has both a PTR name (`nas.lan`) and an mDNS name
  (`synology.local`); mDNS wins because it is more confident.
- **Conflict**: the printer's resolver says `printer.lan` while mDNS says
  `HP-LaserJet-M404.local`. The store raises a conflict and the UI shows both.
- **Duplicate IP**: two smart plugs both answer for `192.168.1.230`. The store
  flags both with `duplicate-ip`.
- **Randomised MAC**: the phone at `.101` has a locally-administered address, so
  no vendor can be looked up, and it drops ICMP.
- **Timing**: requests are paced (default 15 ms per address, 150 ms lookups with
  ±50 % jitter) so progress and the event log are readable.

## Running it

```
shoal probe fake                 # print events and observations to stdout
shoal probe fake -interval 2ms   # faster sweep
shoal --demo                     # the full TUI on fake data
```

## Options

`fake.Options{Devices, Interval, Latency, Seed}` — tests pass microsecond
timings and a fixed seed; `DefaultDevices()` is the demo cast.
