# Proposals

Designs precise enough that a contributor (human or AI) can implement them
end-to-end without re-deriving the design. These describe the `mpd-virt`
binary; the in-VM `mpd` binary's proposals live in
[the main mpd repo](https://github.com/mutms/mpd).

> **Pruned 2026-08-03.** Proposals that are fully implemented or superseded
> were deleted — the code and git history are the record, so pure design
> history earns no keep. What remains is only material that is **not yet
> built**. (Note: reachability today is the **mpd-proxy** WireGuard overlay +
> split DNS on the Mac, not a per-VM static route — treat any lingering
> static-route/`/etc/resolver`-per-VM wording in older kept proposals as
> historical.)

## Not-yet-built backends and features

- [`sandbox-takeover-and-ca-refresh.md`](sandbox-takeover-and-ca-refresh.md) —
  **CA rotation** is the unbuilt half: a shared `mpd-virt refresh-trust <vm>`
  primitive + a new in-VM `mpd --vm-refresh-trust` verb (re-import trust,
  regenerate service/project certs, restart the frontdoor). The root CA is a
  ~1-year fixed deadline every existing VM will hit; `ca.go` only *errors* on
  expiry today. (Sandbox→managed adoption is already covered by `takeover`.)
- [`apple-container-backend.md`](apple-container-backend.md) — mostly shipped;
  kept for the unimplemented spike findings: Podman-in-`container` cap-set
  narrowing (from `--cap-add ALL`), the `/proc/sys` remount-rw requirement,
  cgroup delegation limits, and the base-image conformance contract. Also the
  open host-key **TOFU pinning** gap (pin on the name, not the address).
- [`mpd-proxy-wireguard.md`](mpd-proxy-wireguard.md) — the shipped mpd-proxy
  architecture, kept for its unbuilt cross-platform design: the
  `HostIntegration` seam for Linux (netlink + systemd-resolved) and Windows
  (Wintun + NRPT), and a `native` hypervisor backend keyed on `GOOS`
  (KVM/Hyper-V/AVF) — the groundwork for the planned `mpd-virt-linux` /
  `mpd-virt-windows` siblings.

## Salvaged notes (extracted from pruned proposals)

- [`ssh-runtime-aliases.md`](ssh-runtime-aliases.md) — `mpd-<NNN>-php/node/util`
  SSH aliases via `ProxyJump` for IDE remote-dev (PHPStorm Gateway / VSCode
  Remote-SSH). **Now implemented** in `internal/sshconfig/sshconfig.go` (plus a
  `mpd-<NNN>-socks` backup alias); kept only for the in-VM runtime-hostname
  alignment notes, and prunable once those land. *(Salvaged from `mpd-virt.md`.)*
- [`threat-model-and-ca-backup.md`](threat-model-and-ca-backup.md) — the
  host-side threat model ("Mac is trust origin, VM disposable") and the open
  `export-identity`/`import-identity` CA-backup question. *(Salvaged from
  `macos-host-state.md`.)*
