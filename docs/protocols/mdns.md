# mdns — asking the device what it calls itself

`mdns` is an **enricher**: it runs whenever a device's `ip` field first appears
or changes, and asks the device directly — over multicast — for its name.

## DNS with nobody in charge

Unicast DNS has servers: you ask a resolver, it answers for names it was
configured with. Multicast DNS (RFC 6762, what Apple ships as **Bonjour** and
Linux as **avahi**) has none. The question goes to the group address
**224.0.0.251:5353**, every responder on the segment hears it, and each host
answers only for **itself**. Names live under the `.local` pseudo-TLD.

Two consequences matter for shoal:

- **An answer proves locality.** 224.0.0.251 is link-local; no router forwards
  it. If something answers, it is on this network segment.
- **mDNS knows the devices DNS does not.** Your router's DHCP server rarely
  writes PTR records for its clients, so `rdns` comes back empty for laptops
  and phones. Those devices publish their own names over mDNS instead. This is
  the difference between shoal showing a blank hostname column and showing
  `laptop.local`.

## The question shoal asks

The same reverse question `rdns` asks, sent to the group instead of a
resolver: *who is `42.1.168.192.in-addr.arpa`?* Reverse lookups are the
right shape here because shoal starts from an address — it has just learned
one from ARP — and does not yet know any name to ask about.

The query goes out from an **ephemeral port**, not 5353. That makes it a
**one-shot query**: responders send the answer straight back to that port
instead of multicasting it to the whole network (RFC 6762 §6.7), so shoal does
not need to join the multicast group, and the reply cannot be confused with
the ordinary Bonjour chatter that `mDNSResponder` is already handling. It also
means the query ID is echoed back, so an unmatched reply can be discarded.

Neither the recursion-desired bit (there is nothing to recurse) nor the QU bit
(§5.4, "answer me directly") is set; the ephemeral source port already says
what the QU bit would.

## An answer, taken apart

`testdata/reply-ptr.hex` has the shape of a reply from a laptop:

```
flags 0x8400          response, authoritative — in mDNS everyone is
                      authoritative, because nobody answers for anyone else
answer      42.1.168.192.in-addr.arpa.       PTR ttl 10  laptop.local.
additional  laptop._device-info._tcp.local. TXT ttl 10  model=Example1,1
```

The additional record was never asked for. Responders volunteer what they
expect the querier to want next — here, the hardware model. Shoal logs every
section it receives, and for now emits only the name; `_device-info` is
exactly the sort of evidence the Phase 5 `classify` probe will want.

## Confidence and TTL

Names are emitted at confidence **0.9**, above `rdns`'s 0.7. The device is
speaking for itself rather than repeating what an administrator once typed
into a DHCP server, so when the two disagree the `.local` name wins — and both
stay visible in the detail view, which is the point.

That reply's TTL is **10 seconds**. mDNS keeps reverse records short because a
genuinely interested querier is expected to keep asking. Shoal asks once per
device, so it holds the name for five minutes and says so; the method string
always reports the true TTL.

## Who actually answered

Usually the device itself, and the method says so. But a **Bonjour Sleep
Proxy** — typically an always-on Apple TV or HomePod — registers on behalf of
devices that have gone to sleep and answers for them. Shoal accepts that
answer and names both parties:

```
answered by 192.168.1.5 on behalf of 192.168.1.42 (a proxy responder, e.g. a Bonjour Sleep Proxy)
```

Silence is not an error. Most non-Apple, non-avahi devices simply do not run a
responder, and the log says that rather than reporting a failure.

## Listening instead of asking

Bonjour networks talk to themselves constantly: devices announce when they
join, when they wake, when a service changes, and they answer each other's
queries in the open. The listener is a **discoverer** that sends nothing at
all and writes down what passes.

Port 5353 is already held by the system responder — `mDNSResponder` on macOS,
`avahi-daemon` on most Linux systems — so the socket sets **SO_REUSEADDR** and
**SO_REUSEPORT** before binding to the group address, then joins
224.0.0.251 on the scanning interface. Those two options are what let several
processes share a multicast port, and they are why shoal can watch the
conversation without disturbing the responder having it.

What each overheard message is worth:

| Heard | Emitted |
|---|---|
| any message at all | `ip` — something at that address is speaking mDNS |
| `A` record whose address **is the sender's** | `hostname`, confidence 0.9 |
| `PTR` under `_services._dns-sd._udp.local` | `service` — the sender listing the types it offers |
| `SRV` whose target is a name **the sender has claimed** | `service`, with the host and port in the method |
| `PTR` from a type to an instance | nothing — see below |
| a query | `ip` only; a question reveals no facts |

### Who offers a service

Attribution is the one genuinely hard part, and getting it wrong is worse
than missing a service. Devices relay each other's records. Watching a real
network, a phone was seen announcing

```
_companion-link._tcp.local  PTR  laptop._companion-link._tcp.local
```

— the **laptop's** instance, not its own. Crediting the sender would have put
the laptop's service on the phone. So a bare type→instance PTR is logged and
nothing is emitted. The `SRV` record is what settles it, because it names the host that
actually serves the instance; shoal credits the service only when that host is
a name the sender has published an address record for. The listener remembers
those names for the length of the run, so an address in one packet ties up a
service in the next.

The event log says what it declined and why, so a missing service is
explained rather than silently absent.

## Running it

```
shoal probe mdns 192.168.1.42           # ask one device for its name
shoal probe mdns 192.168.1.42 -wait 3s  # wait longer for a slow responder
shoal probe mdns                          # listen; ^C to stop
shoal probe mdns -for 30s                 # listen for half a minute
```

A quiet network stays quiet: announcements are triggered by devices joining,
waking or being browsed. Running `dns-sd -B _services._dns-sd._udp` in another
terminal will stir up traffic to watch.

## Limits, for now

- **No active service discovery yet.** Shoal never asks
  `_services._dns-sd._udp.local` itself; it credits the answers it overhears
  when another device asks. Asking directly would fill the `service` field on
  a quiet network.
- **TXT records are logged, not read.** `_device-info` already carries the
  hardware model, which is what Phase 5's `classify` will want.
- **The outgoing interface is the host's default.** On a multi-homed machine —
  a VPN up, say — the query follows the default route rather than the
  interface shoal is scanning. The listener will have to bind the interface
  explicitly, and this probe should follow.
- **IPv4 only**, matching the rest of shoal.
- **No known-answer suppression or duplicate-question suppression.** Both are
  politeness features of continuous querying; a single one-shot query per
  device does not need them.
