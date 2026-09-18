# history — what changed since the last visit

`history` is a discoverer that sends nothing. It recognises the network,
puts back on screen what an earlier visit found, compares what this visit
finds against it, and writes the sightings down for next time.

## Recognising a network

A network is recognised by its **subnet and its gateway's MAC**. The subnet
alone would confuse every venue that uses `192.168.1.0/24` behind a router
at `.1`, which is a great many of them; the gateway's MAC tells them apart.
So history starts only once the sweep has heard the gateway, which it asks
first. A network with no gateway is keyed by its subnet alone. If the
gateway never answers, nothing is remembered for that visit, and the log
says why.

## What is remembered

For each network: its gateway's address, the first and last visit, and how
many visits. For each device on it, keyed by MAC: the last address, name
and vendor, when it was first and last seen, and which probe last heard it.
An empty value never overwrites a remembered one, first seen only moves
earlier and last seen only later.

Devices known only by an address, with no MAC, are not remembered: an
address can pass to another device, a MAC mostly does not. A phone's
private Wi-Fi address is usually fixed per network, so phones are
remembered too.

## What it says

When the network is recognised, every remembered device is emitted as
facts credited to `history` (confidence 0.5, dated when it was last heard,
with the date and probe in the method). The device appears as a row at
once. What happens next is the point:

| Situation | Shown as |
|---|---|
| Not seen on any earlier visit | flag `new-device`, badge `new` (never on a first visit, when everything is) |
| Answering at a different address | flag `ip-changed`, badge `new-ip`: "was at 192.168.1.51 when last heard on …" |
| Known by a different name | flag `name-changed`, badge `renamed`; only the first label counts and case does not, so `OFFICE-NAS` and `office-nas.local` are one name |
| Remembered, not heard, scan settled | freshness ✗, badge `missing`, and a `missing:` line in the log |

The history row in the hood pane reads e.g. `visit 5 · 23 known · 2 new ·
1 missing`.

## Memories are not evidence

Facts from history are shown, but treated differently from anything a
probe learns today:

- They lose every tie: confidence 0.5 and the lowest source priority.
- They never **conflict** with today's value. A different address or name
  is a change, labelled "(last visit)" in the details pane, not a
  disagreement.
- A remembered address is **not a claim** on it: another device may hold it
  today, so it neither raises `duplicate-ip` nor attracts facts keyed by
  that address.
- They never **prompt a probe**. Pinging or asking DNS about last week's
  address would attribute today's answer to the wrong device.
- They do count as **contact**, at the time they recall. That is what lets
  a remembered device read as "did not answer scan 1; last heard on an
  earlier visit, 3d ago".

## Where the file lives

| Platform | Default path |
|---|---|
| Linux, BSD | `$XDG_DATA_HOME/shoal/history.db`, else `~/.local/share/shoal/history.db` |
| macOS | `~/Library/Application Support/shoal/history.db` |
| Windows | `%LOCALAPPDATA%\shoal\history.db` |

The directory is created readable by the user only: the file lists the
devices on every network scanned. `--history PATH` moves it and
`--no-history` turns it off. If the file cannot be opened the scan runs
anyway, and the mode line says history is off and why. It is SQLite, via
`modernc.org/sqlite`, which is pure Go, so shoal still needs no C compiler.

## Running it

```
shoal probe history                   # every network remembered, and its devices
shoal probe history -history x.db     # a different file
```

The probe itself only runs during a scan, since it needs the gateway to
answer; this shows what it will compare against. `shoal --demo` seeds an
in-memory history with an invented visit three days earlier, so it shows a
missing projector, a printer on a new address, a renamed Raspberry Pi and
three new devices without touching the real file.
