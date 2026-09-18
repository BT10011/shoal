# CLAUDE.md

## Start here
Read `PROJECT_PLAN.md` in full before doing any work. It is the source of truth for scope, architecture, build order and conventions. If this file and the plan ever disagree, the plan wins.

**Current phase: Phase 3a — Rogue and off-subnet devices.** (Update this line as phases complete.) Passive detection done 2026-09-18 (ARP listener, `rogue` enricher, FLAGS column); the opt-in active follow-ups (`--check-off-subnet`, `--also <cidr>`) await the maintainer's go-ahead. The plan's Phase 3a section carries the purpose (AV-over-IP venues: find the box with the stale or link-local address) and the design. Phase 4 — History follows.
Phase 0 completed 2026-09-16: `shoal --demo` works end to end.
Phase 1 completed 2026-09-17: `shoal` scans for real (ARP sweep on macOS/Linux, unprivileged `neigh` fallback, vendors from the embedded IEEE registry).
Phase 2 completed 2026-09-17: names and latency — `rdns`, `mdns` (reverse queries + passive listener), `nbns` and `icmp`. Two optional pieces were deferred; see the end of Phase 2 in the plan.
Phase 3 completed 2026-09-18: sort, filter, hex view, freshness marks, rescan/cancel (scans are now an engine concept), key bar, live theme picker, in-app manual, tabbed layout for small terminals. Decisions are recorded at the end of Phase 3 in the plan.
Phase 3a is an optional add-on for devices the subnet does not explain; it can be skipped.

## What we are building
A terminal-based, good-looking LAN discovery tool (Go, Bubble Tea, Lipgloss, TideUI) that shows live updates and progress, and lets the user understand what is happening under the hood during discovery. Every displayed value must show how it was discovered.

## What we are NOT building
- NOT a replacement for Nmap: no full port scanning, OS fingerprinting, scripting engine, vulnerability detection or stealth features.
- NOT a clone or replacement for LanScan.
- Scope test: does the change help the user see or understand how a device was discovered or identified? If not, don't build it. Ask first.

## Rules
- Stay inside the current phase. Do not implement features from later phases.
- Work in small, reviewable steps. After each step, `go build ./...`, `go vet ./...` and `go test -race ./...` must pass.
- `internal/model` has no I/O. Only `internal/store` mutates device state. The UI never touches engine internals; it talks to the engine via messages.
- Probes emit `Observation`s (facts, with meaningful `Source` and human-readable `Method`) and `ProbeEvent`s (what is being sent and received, plus progress). Never write device fields directly.
- UI updates from the engine are batched (about 10 per second at most). The UI must never block.
- Every new probe gets its own package under `internal/probe/`, implements `Discoverer` or `Enricher`, has fixture-based tests, a `shoal probe <name>` subcommand, and a `docs/protocols/<name>.md`.
- Prefer pure-Go dependencies. Ask before adding cgo or large dependencies.
- Pin Bubble Tea, Bubbles and Lipgloss to the versions TideUI is compatible with.
- Active probing is rate-limited and limited to the local subnet by default. Port checks are opt-in.
- If something isn't answered in `PROJECT_PLAN.md`, ask rather than guess, then record the decision in the plan.

## Commands
```
go run ./cmd/shoal --demo      # TUI with fake data, no root or network needed
go test -race ./...
go vet ./...
make setcap                    # Linux: grant cap_net_raw to the built binary (Phase 1+)
```
