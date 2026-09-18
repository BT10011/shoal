# CLAUDE.md

## Start here
Read `PROJECT_PLAN.md` in full before doing any work. It is the source of truth for scope, architecture, build order and conventions. If this file and the plan ever disagree, the plan wins.

**Current phase: finishing release 1.** (Update this line as phases complete.) Release 1 is Phases 0–4 and 3a, the cut-down Phase 5 (Dante and NDI flags, a SERVICES column, a browse question per scan), a README, and release builds with install notes per OS. See "Release 1", "Planned future updates" (Windows first) and "Deferred to later releases" at the end of §7 in the plan. Phase 4 still to be confirmed by the maintainer across two real visits.
Phase 3a completed 2026-09-18: devices on the wrong subnet are found passively (ARP listener, `rogue` enricher, FLAGS column) and actively with the opt-in `--also <cidr>`; its purpose (AV-over-IP venues: find the box with the stale or link-local address) and decisions are in the plan. `--check-off-subnet` was left out as covered by `--also`.
Phase 0 completed 2026-09-16: `shoal --demo` works end to end.
Phase 1 completed 2026-09-17: `shoal` scans for real (ARP sweep on macOS/Linux, unprivileged `neigh` fallback, vendors from the embedded IEEE registry).
Phase 2 completed 2026-09-17: names and latency — `rdns`, `mdns` (reverse queries + passive listener), `nbns` and `icmp`. Two optional pieces were deferred; see the end of Phase 2 in the plan.
Phase 3 completed 2026-09-18: sort, filter, hex view, freshness marks, rescan/cancel (scans are now an engine concept), key bar, live theme picker, in-app manual, tabbed layout for small terminals. Decisions are recorded at the end of Phase 3 in the plan.
Phase 3a is an optional add-on for devices the subnet does not explain; it can be skipped.
Windows support is a decided target, deferred until the maintainer is on a Windows machine; the design is in the plan's §11. Do not run Wine on the Linux machine.

## What we are building
A terminal-based, good-looking LAN discovery tool (Go, Bubble Tea, Lipgloss, TideUI) that shows live updates and progress, and lets the user understand what is happening under the hood during discovery. Every displayed value must show how it was discovered.

## What we are NOT building
- NOT a replacement for Nmap: no full port scanning, OS fingerprinting, scripting engine, vulnerability detection or stealth features.
- NOT a clone or replacement for LanScan.
- Scope test: does the change help the user see or understand how a device was discovered or identified? If not, don't build it. Ask first.

## Rules
- **No personal or real network data in the repository, ever.** Not in code, tests, fixtures, docs, the plan, or commit messages. That means no real IP addresses or subnets, no real MAC addresses, no real hostnames or device names, no real makes of equipment on the maintainer's network, no email addresses, and no "on the maintainer's network the router did X" stories. Use invented values: subnets from RFC 1918 or RFC 5737 space that are not the maintainer's (172.16.10.0/24, 192.0.2.0/24, 198.51.100.0/24), MACs from the documentation range 00:00:5e:00:53:00–ff, invented names (office-nas, stage-laptop). Live scans may be run to verify behaviour, but only the conclusion goes into the repo, phrased generically. On 2026-09-18 the whole history had to be rewritten and force-pushed because this was broken; there is no second time.
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
make notices                   # regenerate THIRD_PARTY_NOTICES.md after changing dependencies
make release                   # vet, race tests, notices, then archives for every platform in dist/
gh release create vX.Y.Z dist/* --title "Shoal vX.Y.Z" --latest   # publish, after git tag vX.Y.Z and make release
make release RELEASE_REPO=BT10011/shoal-beta                        # a beta: installer aimed at the public download-only repo
shoal version                  # the version make stamps in (a plain go build says "dev")
```
