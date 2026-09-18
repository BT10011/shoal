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

The query goes out from **port 5353**, the way every Bonjour and Avahi
querier asks, on the interface being scanned. A question from that port is
answered by **multicast** to the whole group (RFC 6762 §6), and that matters
more than it sounds:

- A question from any other port is a *one-shot* query, answered by unicast
  straight back to it (§6.7). A host firewall usually drops that answer: its
  connection tracking saw a packet go to `224.0.0.251` and does not
  recognise a reply arriving from the device's own address. Ubuntu's `ufw`,
  for one, allows multicast mDNS by default and silently drops the unicast
  reply, so on such a machine every one-shot question goes unanswered.
- A multicast answer is allowed wherever mDNS is allowed at all, on any OS.

Port 5353 is shared with the system's own responder (`mDNSResponder`,
`avahi-daemon`), so shoal takes care not to disturb it. Answers are read on
a socket bound to the **group address**, which only ever receives multicast,
so unicast traffic for the port stays with the responder. Each question is
sent from a second socket bound to port 5353 that exists only for that one
write, with multicast loopback off, so this host's own sockets, the listener
among them, do not hear shoal asking.

Because the group socket hears every mDNS message on the segment, an answer
is matched **by name**: only a message carrying a PTR for the name asked
about counts, and everything else passes silently. The query ID is **0**, as
RFC 6762 §18.1 asks of multicast questions, and multicast answers carry 0
too. Every host on the segment hears the answer, the `dns-sd` listener
included, which writes it down like any other announcement.

If a responder holds port 5353 without letting it be shared, shoal says so
in the log and falls back to a one-shot question from an ephemeral port,
matching the answer by its echoed query ID; the silence message then adds
that a firewall may have dropped the reply.

Neither the recursion-desired bit (there is nothing to recurse) nor the QU bit
(§5.4, "answer me directly") is set: the point is to be answered by
multicast.

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
genuinely interested querier is expected to keep asking. Shoal holds the name
for at least five minutes and says so; the method string always reports the
true TTL. When a name reaches 80% of that, the engine asks again, and once
more at 90%, as mDNS caches do (§5.2), so a name that is still true never
lapses and one that no longer answers expires. The hood row counts these as
renewed. Names the `dns-sd` listener heard are renewed the same way, by this
probe, since both are credited to `mdns`.

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
all and writes down what passes. It appears in the hood pane and the log as
**`dns-sd`**, after the DNS Service Discovery announcements (RFC 6763) that
make up most of what it hears, so it is not mistaken for this probe. Its
facts are still credited to `mdns`: they are mDNS records, and share this
probe's confidence, its weight as direct contact, and its renewal.

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
shoal probe dns-sd                        # listen; ^C to stop
shoal probe dns-sd -for 30s               # listen for half a minute
shoal probe mdns                          # the same: with no address, mdns listens
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
- **IPv4 only**, matching the rest of shoal.
- **No known-answer suppression or duplicate-question suppression.** Both are
  politeness features of continuous querying. Shoal asks once per device per
  scan and again only as a name nears expiry, which is gentler than a
  continuous querier; worth adding if renewal ever runs at scale.
- **No Windows.** Sharing port 5353 uses `SO_REUSEPORT`, which Windows lacks,
  and the rest of shoal needs raw sockets it does not offer without Npcap.
  Linux, macOS and the BSDs build and work.
