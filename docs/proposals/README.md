# Proposals

Designs for work we'd like done but haven't committed to a timeline for.
Each proposal is precise enough that a contributor (human or AI) can
implement it end-to-end without needing to re-derive the design.

These describe the `mpd-virt-macos` binary's architecture; the
in-VM `mpd` binary's proposals (if any) live in
[the main mpd repo](https://github.com/mutms/mpd).

> **Note.** Proposals here are design records, not current
> documentation. Several predate the removal of WireGuard (2026-07-20)
> and still describe a tunnel; mpd has no tunnel — reachability is a
> static route per VM. Treat networking details in older proposals as
> historical.

## Index

- [`macos-host-state.md`](macos-host-state.md) — State model for the
  macOS host: `~/.mpd-virt/conf/` for persistent identity,
  `~/.mpd-virt/<octet>/` for per-VM bookkeeping, plus the host-side
  threat model. Implemented; kept for the rationale.

Per-VM addressing (zones like `222.mpd.test`, subnets like
`10.163.222.0/24`, reachability via a static route rather than a
tunnel) shipped on 2026-07-20 and its proposal has been removed — the
model is documented where it is implemented: this repo's `README.md`
and `MpdVirt.Net`, and the mpd repo's `docs/NETWORKING.md`.

Per-VM signing CAs (each VM gets an intermediate constrained to its own
zone; the root private key never leaves the Mac) and LAN service
certificates (`forge.mpd.test` and friends, with their names published
into every VM's resolver) shipped on 2026-07-25. Same treatment: the
design lives where it is implemented — this repo's `README.md`,
[`docs/LAN_SERVERS.md`](../LAN_SERVERS.md), `MpdVirt.CA` /
`MpdVirt.Server`, and the mpd repo's `docs/SECURITY.md`.
- [`mpd-virt.md`](mpd-virt.md) — `mpd-virt`'s verb surface, sudo-recipe
  UX, VM identity model (octet as canonical key), and the
  Parallels-Desktop-Pro backend specifics.
- [`utm-backend.md`](utm-backend.md) — **high priority.** Second backend
  for macOS: UTM (free, native AVF on Apple Silicon). Removes the
  paid-Parallels-license barrier for evaluation. Simplified by the in-VM
  `bootstrap/` chain being hypervisor-agnostic; UTM platform existed in
  git history before the `mpd-virt` split — lift as reference.
- [`sandbox-takeover-and-ca-refresh.md`](sandbox-takeover-and-ca-refresh.md) —
  One mechanism, two use cases: adopting an existing `mpd-sandbox` VM
  as a managed `mpd-<NNN>` VM, and rotating the local CA before its
  ~1-year expiry. Both share a `mpd-virt refresh-trust <vm>` primitive
  plus a new in-VM `mpd --vm-refresh-trust` verb. CA expiry is a fixed
  deadline; schedule before the first user hits it.
- [`pluggable-backends-and-adopt.md`](pluggable-backends-and-adopt.md) —
  **read first if you're starting `mpd-virt` implementation.** The
  architectural shape that ties the other proposals together: backends
  are reduced to a single `provision(octet, username, sshPubKey) → ip`
  call, after which a shared `adopt(ip, octet, username)` core does
  every Mac-side step. Doubles as the dev wedge — `mpd-virt adopt
  <ip>` works against any reachable VM, so the whole Mac side is
  testable before the first backend is written.
