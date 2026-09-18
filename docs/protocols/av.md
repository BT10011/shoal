# av — Dante and NDI devices

`av` is an enricher that sends nothing. It reads what a device has said
about itself and, when that shows the device is part of a Dante or NDI
system, flags it and says why.

## Why only self-declared evidence

Guessing what a box is from its maker or its open ports goes wrong often
enough to undermine a tool whose point is showing its evidence: a vendor
names who made the network chip, not what the box does. So `av` flags a
device only on evidence the device itself published:

| Flag | Badge | Evidence |
|---|---|---|
| `dante-device` | `dante` | it announces a Dante service type over mDNS: `_netaudio-arc._udp` (audio routing), `_netaudio-cmc._udp` (control and monitoring), and their siblings, all beginning `_netaudio-` |
| `dante-device` | `dante` | its network interface is made by **Audinate**, from the embedded IEEE registry. This is the one vendor rule, and it is not a guess: Audinate makes only Dante modules, so an Audinate interface is a Dante interface |
| `ndi-device` | `ndi` | it announces `_ndi._tcp`, the type NDI devices are found by |

The method lists every piece of evidence and the probe that learned it,
e.g. *"a Dante audio-over-IP device: it announces _netaudio-arc._udp over
mDNS (learned by mdns); and its network interface is made by Audinate Pty L,
which makes only Dante modules (learned by oui)"*. Services from other makers,
open ports and anything else are not evidence.

## Making sure there is something to hear

Dante and NDI devices answer when asked but may not announce on their own,
so on a quiet network nothing would be heard. During a scan the `dns-sd`
listener asks once, and again a second later, which service types are on
offer, and who offers `_netaudio-arc._udp` and `_ndi._tcp` in particular
(see [mdns.md](mdns.md)). The answers are credited by the listener's usual
rules, which refuse to credit a router relaying another VLAN's mDNS with
the services it relays. That matters here: venue networks often put Dante
on its own VLAN behind exactly such a repeater, and badging the router as a
Dante device would be the confident wrong answer this probe exists to avoid.

## Limits

All of this is mDNS, so it is link-local: a Dante device on another VLAN is
seen only if a repeater carries its announcements across, and then it is
not credited to anything on this side. Dante devices in AES67 mode, and
NDI discovery servers, are not covered.

## Running it

```
shoal probe av                  # ask, listen for ten seconds, list what answered
shoal probe av -for 30s         # listen longer
```

Standalone there is no ARP sweep, so devices are named by address and the
Audinate rule cannot apply; in the TUI both kinds of evidence are used. Type
`/dante` or `/ndi` to see only those devices.
