# icmp — how long the device takes to answer

`icmp` is an **enricher**: it runs whenever a device's `ip` field first appears
or changes, and fills the RTT column. It is the only probe that *measures* the
network rather than asking it a question.

## What an echo request is

ICMP is the control protocol that lives beside IP — it carries the errors and
diagnostics IP itself cannot. Two of its message types make up `ping`:

```
type 8  echo request     08 00 cksum id seq  payload
type 0  echo reply       00 00 cksum id seq  payload   (the same payload, echoed)
```

A host that implements IP is expected to echo the payload straight back, so
the round trip measures the network and the host's interrupt path, with almost
nothing in between. That makes it the cleanest latency number available, and a
reply also proves the address is alive at **layer 3** — ARP only proves
something answers for it at layer 2.

Shoal sends three requests, 100 ms apart, and reports the **best** of them.
The best is the most honest single number: a slow reply can be caused by the
host being busy, a retransmission or a queue somewhere, but nothing makes a
reply arrive faster than the path allows. All three are shown in the method
string so the spread stays visible.

## Privileges, and the two kinds of socket

Sending ICMP traditionally needs a raw socket and therefore root. Both of
shoal's platforms offer an unprivileged alternative, and shoal prefers it:

| Socket | How shoal opens it | Needs |
|---|---|---|
| **Unprivileged datagram** | `icmp.ListenPacket("udp4", …)` | macOS: nothing. Linux: the process's group inside `net.ipv4.ping_group_range` |
| **Raw** | `icmp.ListenPacket("ip4:icmp", …)` | root, or `cap_net_raw` via `make setcap` |

With a datagram socket the kernel owns the ICMP **id**: it rewrites the id on
the way out and demultiplexes replies back to the socket that sent them. Shoal
therefore never matches on the id. It matches on the **sequence number** and
on its own payload, the literal string `shoal probe`, which also keeps another
process's pings out of the results when running on a shared raw socket.

Which socket was used appears in every method string, and in the status bar:

```
ICMP echo reply from 192.168.1.1, best of 3 (1.7ms, 1.9ms, 13.3ms), over an unprivileged datagram socket
```

If neither socket can be opened, shoal says `no ICMP` in the status bar and
simply leaves the RTT column empty, rather than reporting a failure per
device.

## Silence is a configuration, not a failure

Plenty of devices are deliberately set not to answer pings — Windows hosts
with the firewall's default rules, printers in a locked-down mode, anything
behind a host firewall. Shoal logs that it asked and heard nothing, emits no
latency, and does not mark the device as failed. A `destination unreachable`
report from a *router* is logged too, naming who sent it.

## Running it

```
shoal probe icmp 192.168.1.1                        # three echoes, best of three
shoal probe icmp 192.168.1.20 -count 10 -interval 50ms
shoal probe icmp 192.168.1.99 -timeout 3s           # be patient with a slow host
```

## Limits, for now

- **IPv4 only**, matching the rest of shoal.
- **No ARP round trip to contrast with.** The plan notes recording the ARP RTT
  alongside this one, to show the difference between a layer 2 and a layer 3
  answer; the ARP sweep does not record per-address timings yet.
- **One socket per device.** Simple and isolated, and at LAN scale (bounded by
  the enricher's concurrency of 8) that costs nothing worth optimising.
- **No jitter or loss statistics.** Three samples cannot support them. Shoal
  reports what it measured and leaves `ping -c 100` to `ping`.
