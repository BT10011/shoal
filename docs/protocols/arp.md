# arp — who is on the link?

`arp` is a **discoverer**: it is how shoal finds devices in the first place.
It runs once per scan on the chosen interface.

## The protocol

IPv4 hosts on an Ethernet/Wi-Fi segment need each other's MAC addresses to
exchange frames. ARP (RFC 826) is the lookup: a host broadcasts

```
who-has 172.16.10.5? tell 172.16.10.9
```

to `ff:ff:ff:ff:ff:ff`, and the owner of `.5` answers with a unicast

```
172.16.10.5 is-at 3c:22:fb:9e:01:77
```

Every host must answer ARP for its own address or it cannot receive traffic
at all, which makes it the most reliable way to enumerate a subnet. It never
leaves the local link (no router forwards it), so it only ever finds devices on
the same segment as the scanning interface.

A frame is 14 bytes of Ethernet header plus 28 bytes of ARP:

| Offset | Field | Value |
|---|---|---|
| 0 | destination MAC | broadcast for requests |
| 6 | source MAC | our interface |
| 12 | ethertype | `0x0806` |
| 14 | hardware / protocol type | `1` (Ethernet) / `0x0800` (IPv4) |
| 18 | address lengths | 6 / 4 |
| 20 | opcode | `1` request, `2` reply |
| 22 | sender MAC, sender IP | the answering host, in a reply |
| 32 | target MAC, target IP | who was asked |

## What shoal does

1. Refuses subnets larger than `/22` (1022 hosts) unless the limit is raised.
2. Emits this machine's own MAC and IP (source `netif`, flag `this-host`) so
   the scanner appears in the table without being probed.
3. Sends one **who-has** per host address, paced at 100/s by default, and
   reports each as a `sent` event.
4. Listens the whole time. For every ARP frame from an address inside the
   subnet it emits `mac` and `ip`, keyed by MAC, with the raw frame attached:
   - a reply addressed to us → confidence **1.0**,
     "ARP reply from … answering our who-has";
   - a reply to someone else, or a request from another host → confidence
     **0.9**, "overheard on en0". The sender field of any ARP frame is the
     host's own statement of its address, so these are nearly as good.
5. Waits one second, then asks every address that stayed silent **once more**
   (sleeping devices, slow Wi-Fi clients), and waits again.
6. Reports `sweep complete: N of M addresses answered`.

Progress is reported per request; the bar's total grows when the retry pass
adds requests, which is honest about the extra work.

## How the frames get on the wire

| Platform | Backend | Privilege |
|---|---|---|
| macOS / BSD | `/dev/bpf*` (BPF), pure Go via `golang.org/x/sys/unix` | root, or membership of `access_bpf` (Wireshark's ChmodBPF sets this up) |
| Linux | `AF_PACKET` socket bound to the interface | root, or `cap_net_raw` on the binary via `make setcap` |

On BPF the kernel is asked to hand us only ARP frames (`BIOCSETF` with a
four-instruction filter on the ethertype), to return them immediately
(`BIOCIMMEDIATE`), to let us write the complete Ethernet header ourselves
(`BIOCSHDRCMPLT`) and not to echo our own transmissions (`BIOCSSEESENT 0`).

On Linux the socket's protocol is `ETH_P_ARP`, so the kernel delivers only
ARP frames and no userspace filter is needed; frames the kernel marks
`PACKET_OUTGOING` are ours and are ignored.

If raw access is refused, `shoal` says so and falls back to the
unprivileged [`neigh`](neigh.md) probe instead of failing.

## Running it

```
shoal probe arp              # sweep the default interface's subnet
shoal probe arp en1          # a specific interface
shoal probe arp -rate 20     # gentler pacing
shoal probe arp -no-retry
```

Only scan networks you own or are authorised to test. An ARP sweep is
harmless to hosts, but on a managed network it is visible to switches and
intrusion-detection systems.
