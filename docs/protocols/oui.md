# oui — vendor from the MAC address

`oui` is an **enricher**: it runs whenever a device's `mac` field first
appears or changes, and it never touches the network.

## What an OUI is

A 48-bit MAC address is two halves. The first 24 bits — the **OUI**
(Organizationally Unique Identifier), e.g. `00:11:32` — are bought from the
IEEE by a manufacturer; the last 24 bits are chosen by that manufacturer per
device. The IEEE publishes who owns each prefix in the **MA-L** ("MAC Address
Block Large") registry, so the prefix alone reveals the maker of the network
interface: `00:11:32` → Synology, `B8:27:EB` → Raspberry Pi Foundation.

Two bits in the first octet change the meaning:

| Bit | Name | Meaning |
|---|---|---|
| `0x01` | I/G | 1 = multicast/group address, not a host |
| `0x02` | U/L | 1 = **locally administered**: software chose the address, no vendor owns it |

Phones with "private Wi-Fi address" enabled, VMs, containers and some
randomising laptops all set the U/L bit. Looking those prefixes up would give
a wrong or missing vendor, so `oui` reports the `locally-administered-mac`
flag instead and explains why.

## What shoal does

1. Takes the device's resolved `mac` (or its key when the key is a MAC).
2. If the U/L bit is set → emits `flag = locally-administered-mac`, confidence 1.0, with the octet value in the method, and stops.
3. Otherwise looks the prefix up in the **embedded** registry and reports the
   lookup as an `info` event either way.
4. On a hit → emits `vendor`, confidence **0.9**. Not 1.0 because the registry
   says who bought the prefix, not who built the device (white-label hardware,
   spoofed or cloned addresses), and the copy is a snapshot.

Every method string names the prefix, the organisation and the registry
snapshot date, e.g.
`IEEE MA-L registry assigns prefix 00:11:32 to Synology Incorporated (embedded copy: IEEE MA-L registry, …, fetched 2026-09-17)`.

## The data

`data/oui.csv` is the IEEE MA-L registry trimmed to `prefix,organisation`
(~40 000 rows, ~1.3 MB) and compiled into the binary with `go:embed`, so a
copied `shoal` binary works offline with no data files. Refresh it with:

```
go generate ./data
```

which downloads https://standards-oui.ieee.org/oui/oui.csv and rewrites the
file with today's date in its header line.

Only MA-L (24-bit) blocks are included. The IEEE also sells 28-bit (MA-M) and
36-bit (MA-S) blocks to small vendors; those addresses show as "not in the
registry" for now.

## Running it

```
shoal probe oui 00:11:32:aa:bb:cc    # vendor lookup, with the reasoning
shoal probe oui da:3b:91:0c:44:e2    # locally administered → flag, no lookup
```
