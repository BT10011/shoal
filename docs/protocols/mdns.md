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
queries in the open. The listener is a **discoverer** that writes down what
passes. In a scan it also asks one question, at the start of each scan and
again a second later as RFC 6762 §5.2 asks: every service type on offer
(`_services._dns-sd._udp.local`, RFC 6763 §9), plus Dante's
`_netaudio-cmc._udp`, `_netaudio-arc._udp` and NDI's `_ndi._tcp`, since not every embedded
responder answers the enumeration. It is sent from port 5353, so every
answer is multicast and the listener hears it like any announcement.
Without it, services appear only when something else, such as Dante
Controller, is browsing. `shoal probe dns-sd` alone sends nothing unless
given `-ask`. It appears in the hood pane and the log as
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
| `PTR` under `_services._dns-sd._udp.local` | `service` — the sender listing the types it offers, unless the sender is relaying (see below) |
| `SRV` whose target is a name **the sender has claimed** | `service`, with the host and port in the method |
| `SRV` whose target the sender has **not claimed yet** | held, and credited if the sender announces an address for that name later |
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

**Either order works.** Responders split long answers across messages, and
nothing requires the address to come first. An `SRV` whose target the sender
has not claimed *yet* is held rather than discarded, and credited the moment
the sender announces an address for that name — which is how a Dante or NDI
device that announces its service before its address is found at all. The
hold is not a way round the rule: the address still has to come from that
sender and still has to be the sender's own, and a sender later found to be
relaying loses everything it was holding.

The list of service types is the same problem one level up, but it does
**not** have the same answer. A router running an **mDNS repeater or
reflector** (MikroTik's repeater, Avahi's reflector, Ubiquiti's mDNS option)
answers "which services are here?" on behalf of devices on other VLANs, from
its own address. The first live run of the browse showed exactly that: a
gateway listing, as its own, the service types of devices on another VLAN.
Crediting that to the router would have been wrong, and on a venue network
where Dante sits on its own VLAN it would have badged the router as a Dante
device.

The first rule written for this was that a listed type is credited only once
the sender has **named its own address**. That rule was wrong, and a live
run measured how wrong: a responder answering the meta-query sends `PTR`
records and **nothing else** — there is no address record in the reply,
because there is no `SRV` target needing one. Over one twelve-second listen,
five devices listed eighteen service types between them and every single one
was held and never credited. Only two services got through in that time, and
only because those particular messages happened to be announcements carrying
`SRV` and `A` in the additional section. The rule was not being cautious; it
was discarding nearly everything.

So the list is now taken at its word, per RFC 6763 §9 — an answer under
`_services._dns-sd._udp.local` is the responder describing **itself** — and
the relay is caught by what actually distinguishes it:

> A sender is a relay once it carries an address record for an address that
> is neither its own nor on the network being scanned.

That is something a device speaking only for itself never does, and a
repeater does constantly: the same gateway was later seen carrying an `A`
record for a host on another subnet entirely. Once a sender is marked,
nothing it has listed is credited, anything it had already listed is dropped,
and the log names it when listening stops.

Only `A` records are read for this, because an IPv6 address cannot be
compared against the IPv4 address the message arrived from, and addresses
that are loopback, unspecified or link-local are ignored — a self-assigned
`169.254.x.x` is the `rogue` probe's business, not evidence of relaying.

A list is held for a short **settle** window (`DefaultSettle`, two seconds)
before it is credited, so that a relay has a chance to give itself away
first; a sender that names its own address is credited at once and does not
wait. The honest limit: a relay that lists services and then stays silent
about any foreign address for longer than the settle window is credited with
that list. It is a trade, made deliberately — the old rule avoided that case
by losing almost every real service — and the method on every observation
says which way it was credited, so the detail pane shows the difference.

The event log says what it declined and why, so a missing service is
explained rather than silently absent.

### Reading the names

shoal decodes mDNS messages itself instead of handing them to
`golang.org/x/net/dns/dnsmessage`, and the reason is one line in that
library:

```go
// Reject names containing dots.
// See issue golang/go#56246
```

A DNS-SD **service instance name is a single label of arbitrary UTF-8**
(RFC 6763 §4.1.1). A dot inside it is ordinary text, not a separator, and
NDI builds its source names from the machine's hostname — which on a Mac
ends `.LOCAL` — so a real NDI source announces itself as

```
STAGE-MBP.LOCAL (Scan Converter)._ndi._tcp.local
```

That name is legal, common, and rejected by that parser. Worse, `Unpack` is
all or nothing: one such name failed the **whole message**, so shoal threw
away every packet an NDI source sent, including the address records that
happened to share it. An NDI machine could sit on the network announcing
itself perfectly and never appear in the table. This was found with NDI Scan
Converter running on the same laptop as shoal, and `testdata/reply-ndi.hex`
is the shape that used to be lost.

The decoder in `message.go` follows compression pointers (bounded, so a
crafted message cannot loop), decodes `A`, `AAAA`, `PTR`, `SRV` and `TXT`,
and steps over any other type by its rdata length rather than failing. A dot
inside a label is kept rather than escaped as `\.`, which keeps the name
readable on screen and leaves `ServiceType` working; the cost is a
theoretical ambiguity between two names differing only in where a label
ends, which nothing here depends on.

### The machine shoal is running on

A responder does not answer multicast questions from its own host —
mDNSResponder and Avahi both ignore them. So a listener on the group hears
every device except the one it is running on, which is how a laptop running
NDI showed nothing while shoal sat on that same laptop.

shoal therefore asks this one device directly: a unicast question to
`127.0.0.1:5353`, carrying the same browse names, answered at once. Nothing
extra reaches the network, since it goes over loopback. The answers are
credited to the scanning interface's own address, and the method says the
question was asked rather than overheard, so the detail pane never pretends
this was heard like everything else.

One wrinkle worth knowing: asked over loopback, the responder gives its
address records as `127.0.0.1`, which is not the interface address, so an
`SRV` learned this way is held rather than credited. The service-type list
covers it, which is why both are asked for.

## Running it

```
shoal probe mdns 192.168.1.42           # ask one device for its name
shoal probe mdns 192.168.1.42 -wait 3s  # wait longer for a slow responder
shoal probe dns-sd                        # listen; ^C to stop
shoal probe dns-sd -for 30s               # listen for half a minute
shoal probe dns-sd -ask -for 10s          # ask which services are on offer, then listen
shoal probe av                            # ask, and point out Dante and NDI devices
shoal probe mdns                          # the same: with no address, mdns listens
```

A quiet network stays quiet: announcements are triggered by devices joining,
waking or being browsed. Running `dns-sd -B _services._dns-sd._udp` in another
terminal will stir up traffic to watch.

## Limits, for now

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
