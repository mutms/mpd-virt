# Proposal: pluggable backends + `adopt` as the shared core

A small architectural reframing for `mpd-virt` that lets every
hypervisor backend (Parallels today, UTM next, libvirt/Hyper-V later)
share one Mac-side implementation, and that turns
[`sandbox-takeover-and-ca-refresh.md`](sandbox-takeover-and-ca-refresh.md)
into the natural development wedge for the whole binary.

## The shape

A backend is anything that can deliver a bootable Debian Trixie VM
with one SSH key authorized. After that, **every backend hands off the
same tuple** to a single shared post-provisioning flow:

```swift
// Per-backend protocol — the only thing each backend implements.
protocol Backend {
    func provision(octet: Int,
                   username: String,
                   sshPubKey: String) throws -> ProvisionResult
}

struct ProvisionResult {
    let ip: String       // the VM's IP, reachable from the Mac
    // ...whatever else the backend wants to record (UUID, etc.)
}

// Shared core — runs unchanged regardless of backend.
func adopt(ip: String, octet: Int, username: String) throws { … }
```

`provision()` is the only per-backend code. Everything that comes
after — host route, DNS resolver, CA trust, WireGuard keypair + push,
in-VM `mpd --vm-setup`, `~/.ssh/config` block, Desktop shortcut — lives
in `adopt()` and is identical across backends.

## What `adopt(ip, octet, username)` does

The post-provisioning core. Independent of how the VM came into
existence:

1. **Host networking** — add route to `10.163.0.0/24`, write
   `/etc/resolver/mpd.test`, import the mpd CA into the System
   keychain (sudo-recipe UX from [`mpd-virt.md`](mpd-virt.md)).
2. **SSH bootstrap into the VM** — ensure dev pubkey authorized,
   rebuild `bin/mpd` if needed, upload host CA into
   `/var/lib/mpd/conf/caroot/`, run `mpd --vm-setup`.
3. **WireGuard** — generate keypair on the Mac, push
   `mpd0.conf` into `/var/lib/mpd/conf/wireguard/`, kick
   `wg-quick@mpd0` on the VM, install matching tunnel on the Mac via
   WireGuard.app.
4. **Mac-side conveniences** — write the managed `Host mpd-<NNN>`
   block to `~/.ssh/config`, drop `~/Desktop/mpd VM.command`, update
   `~/.mpd-virt/<octet>/env` and `current.env`.

These are the same steps the Parallels bash flow runs today between
`prlctl clone` and "demo site loads in Safari." They're also exactly
what [`sandbox-takeover-and-ca-refresh.md`](sandbox-takeover-and-ca-refresh.md)
wants to run against a VM that already exists. Same code, three
entry points.

## Three product surfaces, one core

| Entry point | What `provision()` does | What `adopt()` does |
|---|---|---|
| `mpd-virt create <octet>` | Backend clones / cloud-inits a fresh VM, returns its IP | Full adopt flow |
| `mpd-virt adopt <ip> --octet <NNN> --user <name>` | No-op (VM already exists) | Full adopt flow |
| `mpd-virt refresh-trust <vm>` | No-op (VM already known) | CA + WG + cert subset of the adopt flow |

`adopt` is the product. `create` is `adopt` with a backend-supplied
VM in front of it. `refresh-trust` is the subset of `adopt` that
rotates trust material without touching everything else.

## The IP-only test wedge

`adopt(ip, octet, username)` doesn't care how the VM got to that IP.
Point it at **any** reachable Debian Trixie VM (e.g. the in-VM
`mpd-sandbox` we already have at `10.211.55.223`) and the entire
Mac-side surface area becomes testable without writing a single line
of Parallels code:

```
mpd-virt adopt 10.211.55.223 --octet 223 --user skodak
```

This means the implementation order is:

1. **Write `adopt()` first.** Validate against the existing sandbox
   VM. Iterate until host route + DNS + CA + WG + SSH config + Desktop
   shortcut all work end-to-end.
2. **Wrap it in `mpd-virt adopt`** (sandbox-takeover product surface
   ships immediately as a side-effect).
3. **Then write the Parallels `provision()`** as a thin module that
   ends with "and here's the IP." `mpd-virt create` falls out for
   free.
4. **Then UTM `provision()`** (see [`utm-backend.md`](utm-backend.md)).

Steps 1–2 alone deliver real user value (sandbox-takeover) and
exercise everything Parallels-specific code would later depend on.
By the time step 3 runs, the only unknown is whatever's
genuinely Parallels-specific.

## What the backend handoff contract isn't

- **Not generic VM management.** Backends don't need to expose
  snapshots, network re-configuration, GUI hooks, or arbitrary CLI
  affordances — those are the backend's own UI (Parallels Desktop,
  UTM.app, virt-manager). `mpd-virt` only cares about
  create / start / stop / delete / "give me the IP."
- **Not a runtime-pluggable interface.** Compile-time backend
  selection only (matches [`mpd-virt.md`](mpd-virt.md) — one binary,
  one backend). The protocol is for code organization and testability,
  not for swap-at-runtime flexibility.
- **Not opinionated about how the VM gets its IP.** Each backend
  picks: Parallels uses DHCP-then-static-pin (today's flow), UTM
  uses a cloud-init static IP, libvirt uses its own DHCP. As long
  as `provision()` returns a reachable IP, the core doesn't care.

## Relationship to other proposals

- [`mpd-virt.md`](mpd-virt.md) — defines the verb surface and the
  octet-keyed identity model this proposal slots into. Add a
  `mpd-virt adopt` verb to the table there.
- [`utm-backend.md`](utm-backend.md) — becomes a *much* smaller
  proposal once `adopt()` exists: just "implement `Backend` for
  UTM, return the IP." The cloud-init seed-ISO work it describes
  is purely inside `provision()`.
- [`sandbox-takeover-and-ca-refresh.md`](sandbox-takeover-and-ca-refresh.md)
  — implemented as `mpd-virt adopt` and `mpd-virt refresh-trust`
  on top of the same core. The "one mechanism, two use cases"
  framing in that proposal becomes "one mechanism, three product
  surfaces" once `create` is added.
- [`macos-host-state.md`](macos-host-state.md)
  — `adopt()` is the writer of `~/.mpd-virt/<octet>/env`,
  `current.env`, and the host-side WG config that proposal defines.

## Acceptance

- `mpd-virt adopt 10.211.55.223 --octet 223 --user skodak` works
  against the existing in-repo sandbox VM, end to end: host route +
  resolver + CA trust + WG tunnel + SSH config + `https://mpd.test/`
  loads from Mac Safari.
- The Parallels backend lands as a `provision()` module of ≤200 LOC
  (everything bigger means it leaked logic that belongs in `adopt`).
- `mpd-virt refresh-trust <vm>` reuses ≥80 % of the `adopt` code
  path (no parallel reimplementation of cert/WG plumbing).
