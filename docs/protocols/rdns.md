# rdns — the name DNS has for an address

`rdns` is an **enricher**: it runs whenever a device's `ip` field first appears
or changes, and asks a DNS resolver what name that address has.

## Forward and reverse

Ordinary DNS goes name → address: "what is `synology.lan`?" → `192.168.1.20`.
Reverse DNS goes the other way, and it is not a separate database — it is the
same DNS, asked about a name built out of the address:

```
192.168.1.20   →   20.1.168.192.in-addr.arpa
```

The octets are reversed because DNS names get more specific from **right to
left** (`lan` contains `synology`), while IPv4 addresses get more specific from
**left to right** (`192.168.1.` contains `.20`). Reversing them lets the owner
of `192.168.0.0/16` be delegated `168.192.in-addr.arpa` and hand out names
below it, exactly like an ordinary zone. Under that name lives a **PTR** record —
a pointer to a name.

## Why shoal builds the packets itself

Go's `net.LookupAddr` would do this in one line, but it hands the work to the
operating system and returns only a string. Shoal would then have to display
a hostname with no honest provenance: no resolver, no timing, no TTL, no
packet. So the probe builds the DNS message, sends it to one resolver it can
name, and parses the reply itself, using `golang.org/x/net/dns/dnsmessage`.

A query is 43 bytes:

```
d233  0100  0001 0000 0000 0000      id, flags (recursion desired), 1 question
01 31 03 323230 02 3136 03 313732    "1" "220" "16" "172"
07 696e2d61646472 04 61727061 00     "in-addr" "arpa" root
000c 0001                            type PTR (12), class IN (1)
```

The reply repeats that question and adds an answer section, usually with the
question's name compressed to the two bytes `c0 0c` — "the name already at
offset 12 of this message".

## Which resolver gets asked

Shoal reads `nameserver` lines from `/etc/resolv.conf` **in order** (macOS
keeps that file in step with whatever configd chose for the primary
interface). If the file names none, it tries the default gateway, since on a
home network the router is usually the resolver too. Every answer records
which of them replied, so two resolvers disagreeing about a device is visible
rather than hidden.

Resolvers are asked one at a time and the probe stops at the **first definite
answer**:

| Reply | Meaning | What shoal does |
|---|---|---|
| `NOERROR` + PTR | the address has a name | emit `hostname`, stop |
| `NOERROR`, no answer | the zone exists, this address has no name | stop, say so |
| `NXDOMAIN` | no such name at all | stop, say so |
| `SERVFAIL`, `REFUSED` | this resolver could not or would not answer | log it, try the next |
| no reply within 2s | silent or unreachable | log it, try the next |

A reply whose 16-bit query ID does not match the query is discarded and
reported — that mismatch is what an off-path forgery looks like.

## Confidence and TTL

Observations are emitted with confidence **0.7**, below mDNS's 0.9. A PTR
record says what the network's administrator wrote down, which is often stale,
frequently generic (`dhcp-126.example.net`) and sometimes never set at all;
mDNS is the device answering for itself.

The record's DNS TTL becomes the observation's TTL, so a name expires from the
display exactly when DNS says it stops being valid. TTLs below one minute are
held for a minute so the hostname column cannot flicker; the method string
always reports the true TTL.

## Running it

```
shoal probe rdns 192.168.1.1                    # ask the system's resolvers
shoal probe rdns 192.168.1.20 -resolver 1.1.1.1 # ask one specific resolver
shoal probe rdns 8.8.8.8 -timeout 5s
```

Output names the question, every resolver in the order it will be tried, each
packet sent and received, and the fact that came out:

```
question   1.1.168.192.in-addr.arpa.
resolver 1 192.168.1.1:53       nameserver on line 16 of /etc/resolv.conf

rdns sent      PTR? 1.1.168.192.in-addr.arpa. → 192.168.1.1:53 (id 0xd233, 43 bytes, …)
rdns received  192.168.1.1:53 in 2.9ms: NOERROR, PTR rtr-01 (TTL 1h0m0s)
```

## Limits

- IPv4 only. `ip6.arpa` lookups are not implemented; shoal has no IPv6
  discovery yet, so nothing would ask for one.
- UDP only, and a truncated reply (TC set) is reported rather than retried
  over TCP. A PTR answer that needs more than 1232 bytes is vanishingly rare.
- No DNSSEC validation. The probe reports what the resolver said, not whether
  it can be proven.
