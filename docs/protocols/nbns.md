# nbns — the name Windows machines answer to

`nbns` is an **enricher**: it runs whenever a device's `ip` field first appears
or changes, and asks the host for its **NetBIOS name table**. It is the same
question `nbtscan` and `nmblookup -A` ask, and it names the machines that
answer neither mDNS nor a reverse DNS lookup — which, on most networks, means
the Windows boxes, the NAS units and anything running Samba.

## Why a protocol this old is still worth asking

NetBIOS over TCP/IP dates from the 1980s and has been formally superseded more
than once, yet Windows still registers names through it by default and Samba
still answers. It fills the gap the other two name probes leave:

| Probe | Knows about |
|---|---|
| `rdns` | whatever the network's administrator entered into DNS |
| `mdns` | Apple devices, modern printers, anything running avahi |
| `nbns` | Windows, Samba, NAS appliances, older embedded gear |

## The question

One UDP datagram to port 137, a **node status request**, asking about the
wildcard name `*` — "whatever you are". The message reuses DNS's format
entirely (RFC 1002 borrowed it), so the header, question and answer sections
look familiar, with type `0x0021` (NBSTAT) in place of a DNS record type.

The name itself is encoded in NetBIOS's own peculiar way: the 16-byte name is
padded, then **every byte becomes two letters**, its high nibble and its low
nibble each added to `'A'`. So `*` (`0x2a`) becomes `CK` and each padding null
becomes `AA`:

```
20 434b 41414141414141414141414141414141414141414141414141414141 00
|  "CK"  ...thirty A's...                                        root
|
length: 32 bytes of encoded name
```

That run of A's is how you recognise a NetBIOS packet at a glance in a
capture. The encoding exists because NetBIOS names may contain bytes that are
not legal in DNS labels.

Shoal sends from an **ephemeral port** rather than from 137 as Windows does,
because binding a port below 1024 needs privileges this probe should not
require. Nodes answer whichever port asked.

## What comes back

A table, because a machine registers its name once for **each service it
offers**. The suffix byte says which:

| Suffix | Meaning |
|---|---|
| `<00>` | workstation (unique), or the domain/workgroup (group) |
| `<03>` | messenger |
| `<1b>` | domain master browser |
| `<1c>` | domain controllers |
| `<1d>` | master browser |
| `<1e>` | browser elections |
| `<20>` | file server — this host offers SMB shares |

Each entry also carries a **group** bit. Unique means one machine holds the
name; group means several may, which is how a workgroup or domain is
represented. A typical NAS answers:

```
NAS01     <00> workstation,         unique
NAS01     <20> file server,         unique
WORKGROUP <00> domain or workgroup, group
WORKGROUP <1e> browser elections,   group
adapter 00:11:32:7f:a2:c4
```

Shoal emits the **unique `<00>` name** as `hostname`, and names the workgroup
in the method string. Everything else is logged rather than emitted; the
`<20>` file-server registration is exactly the evidence Phase 5's `classify`
will want.

## The adapter address

After the names, a node reports its own MAC. Shoal emits it as `mac` at
confidence **0.9** — as direct as an ARP reply, but a claim rather than
something observed on the wire, and the method says so. Samba sends six zero
bytes there, which shoal treats as "not answered" rather than as an address.

This is genuinely useful: a device found through the unprivileged `neigh` path
or overheard by the mDNS listener may have no MAC yet, and this supplies one.
If it disagrees with what ARP saw, the detail view shows both.

## Confidence

Names are emitted at **0.8**, between mDNS's 0.9 and reverse DNS's 0.7. Like
mDNS it is the device answering for itself, but a NetBIOS name is a legacy
one: at most 15 bytes, upper-cased, and often a truncation of the real
hostname. `WORKSTATION-04` is the machine's own word for itself; it is not
necessarily what its owner calls it.

Silence is ordinary, and reported as such. A machine that is not Windows and
is not running Samba has no reason to answer, and plenty that would are behind
a firewall that drops port 137.

## Running it

```
shoal probe nbns 192.168.1.20              # ask one host for its name table
shoal probe nbns -timeout 3s 192.168.1.20  # be patient with a slow host
shoal probe nbns -port 13137 127.0.0.1     # ask a responder you control, for testing
```

Flags come before the address, as Go's flag package requires.

## Limits, for now

- **IPv4 only.** NetBIOS over TCP/IP never gained an IPv6 form.
- **Node status only.** Shoal does not register names, query by name, or ask
  the master browser for the network's host list — all of which would be
  reaching well past "explain this device".
- **No SMB.** Share enumeration means negotiating a session on port 445, which
  belongs to the opt-in `ports` work in Phase 5, if anywhere.
- **The name table is what the host claims.** Nothing here is verified against
  another source; that is what the detail view's side-by-side is for.
