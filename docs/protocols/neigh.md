# neigh — the kernel's own ARP table

`neigh` is a **discoverer**, and shoal's unprivileged fallback. It finds the
same devices as [`arp`](arp.md) without opening a raw socket, by asking the
operating system what it has already learned.

## Why it exists

Sending an ARP frame means writing a raw Ethernet frame, which needs root,
`cap_net_raw` or `/dev/bpf` access. But the kernel is already doing ARP
constantly for its own traffic, and it caches the results — that is the
neighbour cache (`arp -an`, `ip neigh`). Reading it needs no privileges at
all.

The catch: the cache only holds addresses the OS has had a reason to talk
to. So shoal gives it a reason.

## What shoal does

1. Emits this machine's own MAC and IP (source `netif`, flag `this-host`).
2. **Nudges** every host address in the subnet: one UDP datagram to port 9
   (discard), paced at 200/s. The datagram itself does not matter and is
   expected to be dropped or refused — what matters is that before the
   kernel can send it, it must resolve the destination MAC, so it performs
   an ARP exchange and caches the result.
3. Waits one second for those exchanges to finish.
4. Reads the cache and emits `mac` and `ip` at confidence **0.8** for every
   usable row.

Rows are skipped when they are on another interface, outside the scanned
subnet, this machine itself, or when the hardware address is all zeros
(the kernel asked and got no answer) or has the multicast bit set
(broadcast and multicast entries such as `224.0.0.251`).

## Why confidence 0.8, not 1.0

The `arp` probe *saw* the reply arrive and keeps the raw frame. Here shoal
only sees a table the OS filled in at some earlier point:

- an entry may be stale — the device may have left, or the address may have
  been reassigned since the kernel cached it;
- there is no raw packet to show in the detail view;
- entries marked permanent are static configuration, not an observation of
  a live host.

The method string says all of this, e.g.
`kernel neighbour cache on en0 says 10.0.0.1 is at 00:00:5e:00:53:02; shoal sent a UDP datagram to prompt the lookup. shoal did not see the ARP exchange itself`.

Because `arp` outranks `neigh` in source priority, running both shows the
directly observed value and keeps the cache entry visible underneath it.

## Where the table comes from

| Platform | Source |
|---|---|
| macOS / BSD | routing table via `sysctl NET_RT_FLAGS` with `RTF_LLINFO` — on BSD the ARP table *is* the routing table; each neighbour is a host route whose gateway is a link-layer address |
| Linux | `/proc/net/arp`, skipping rows without the `ATF_COM` (complete) flag |

## Running it

```
shoal probe neigh              # nudge the subnet, then read the cache
shoal probe neigh en1
shoal probe neigh -no-nudge    # read only what the OS already knew
shoal --unprivileged           # the TUI, using this probe instead of arp
```

`-no-nudge` sends nothing at all: it is the quietest thing shoal can do,
and useful for seeing what the machine already knew before you scanned.
Because it generates no traffic it also ignores the `-max-hosts` limit,
so it works on a large subnet where an active sweep would be refused.
