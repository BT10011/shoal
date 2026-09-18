# rogue — addresses the subnet does not explain

`rogue` is an enricher that sends nothing. It looks at every address the other
probes have attributed to a device and says, in words, when the scanning
interface's subnet cannot account for it.

## Why it exists

Its home is the AV-over-IP venue. Equipment moves between sites, and a box
arrives still carrying a static address from the last one, or with no
configuration at all and falls back to a link-local address. It never
registers properly, and the question on the floor is always *which box*. A
device on the wrong subnet still speaks on the segment it is plugged into —
it broadcasts ARP for its old gateway, announces itself when it links up, and
multicasts mDNS, Dante, NDI or SSDP from whatever address it has — and every
one of those frames carries its address. shoal listens for them; `rogue`
points at them.

## What it flags

| Flag | Condition | What it usually means |
|---|---|---|
| `link-local-ip` | an IPv4 address in `169.254.0.0/16` (RFC 3927) | The device asked for DHCP and got no answer, so it made an address up. A DHCP server that is down or absent on this VLAN, a cable in the wrong socket, or a device that was never configured. |
| `off-subnet-ip` | an IPv4 address outside the scanning interface's subnet | A static address left over from another network or venue, a lease from a different VLAN, occasionally a second uplink. Nothing on this subnet will route to it. |

A device seen only at layer 2 — a MAC with no address, such as one still
probing for an address — is not flagged, because a flag cannot be withdrawn
when the address arrives a second later. The table shows it as a row with an
empty IP cell and the details pane says "seen at layer 2 only".

Every flag's method states the reasoning in full and then names the probe
that learned the address and how, so the chain from packet to conclusion is
on screen: *"169.254.37.12 is inside 169.254.0.0/16, the range a host assigns
itself when no DHCP server answers (RFC 3927); this interface's subnet is
172.16.10.0/24. … Learned from arp: gratuitous ARP from 169.254.37.12
overheard on enp0 …"*.

Flags are never retracted. A device that was on the wrong address at 12:01
is worth remembering as such after the technician has fixed it; the newer,
correct address shows in the table because it is the most recent claim.

## Where the addresses come from

- The ARP sweep's frame listener decodes every ARP frame on the interface,
  including senders outside the subnet and RFC 5227 probes from 0.0.0.0
  (see [arp.md](arp.md)). A continuous ARP listener keeps doing so after the
  sweep has finished, because AV gear speaks sporadically.
- The mDNS listener records the source address of everything it hears
  (see [mdns.md](mdns.md)).
- The kernel neighbour table, in unprivileged mode (see [neigh.md](neigh.md)).

## When a device never speaks

A box with a static address that sits idle may never send an ARP frame for
the listener to hear. `--also 192.168.1.0/24` asks every address in a range
you name — the venue's usual subnet, say — with ARP on this segment, and a
device holding one of those addresses must answer. See the `--also` section
of [arp.md](arp.md).

## The one thing it cannot do

All of this is layer 2. The technician must be on the same VLAN or switch
segment as the device; nothing crosses a router. When a box that "must be
there" does not show, that is the first thing to check.

A second, smaller limit on Linux: with strict reverse-path filtering
(`net.ipv4.conf.<iface>.rp_filter = 1`) the kernel drops IP packets whose
source address it has no route back to via that interface, and a
`169.254.x.x` or foreign-subnet sender is exactly that, so its mDNS never
reaches the listener. ARP is read from a raw socket below that check and is
unaffected, which is why the ARP listener is the dependable path. Loose mode
(`2`, the systemd default) lets them through.

## Running it

```
shoal probe rogue 169.254.37.12               # judged against the default interface's subnet
shoal probe rogue 192.168.1.50 172.16.10.0/24  # against a subnet given explicitly
```

In the TUI, `/link-local` or `/off-subnet` filters the table to the flagged
devices, and the FLAGS column shows the badge on every row.
