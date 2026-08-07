# AI Agent Starting Point

Neutral bootstrap document for AI agents working in this repository — and the
detailed reference the terse `README.md` points at. `CLAUDE.md` imports this
file via `@AGENTS.md` so Claude Code, Codex, Aider, and other tools that read
AGENTS.md natively all see the same instructions.

## What mpd-virt is

`mpd-virt` is the macOS host-side orchestrator for
[mpd](https://github.com/mutms/mpd): it creates or adopts Debian Trixie VMs,
bootstraps them over SSH, installs the in-VM `mpd` platform, issues their
certificates from a local root CA, and wires host-side reachability. A single
Go binary built from `go/`; no Xcode, no Swift.

The family: **mpd** runs inside each VM (projects, runtime containers, DNS,
TLS); **mpd-virt** (this repo) is the host side and owns the root CA;
[mpd-proxy](https://github.com/mutms/mpd-proxy) is the optional root-only
WireGuard + split-DNS helper for transparent `*.mpd.test`;
[mudev](https://github.com/mutms/mudev) is the recipe library `mpd` builds
projects from.

## VM identity

`NNN` is the VM's id: a plain zero-padded identifier `001`–`254`
(`go/internal/vmid` is the single source of truth — ids carry no class or
range semantics). It keys the name `mpd-<NNN>`, the registry dir
`~/.mpd-virt/<NNN>/`, the container subnet `10.163.<NNN>.0/24`, the overlay
gateway `10.163.<NNN>.1`, and the DNS zone `<NNN>.mpd.test`. The box's own
address is found by name (or given to `takeover` explicitly), not a fixed
value. Several VMs are reachable at once — the bare `mpd.test` apex
deliberately does not resolve. Id `000` is the sandbox VM, which lives in the
main mpd repo's sandbox flow, not here.

## Backends

`--backend=<name>` is required on `takeover` and `create` — there is
deliberately no default. Every other verb reads the backend recorded in the
registry at adoption.

| Backend | Host | What it does |
|---|---|---|
| `generic` | anywhere | Adopt an **already-running** Debian box by IP — a cloud VM, bare metal. No power control (it stays up). The path for anything not on the laptop. |
| `parallels` | macOS laptop | Parallels Desktop Pro (`prlctl`): power on/off + find the box's current DHCP IP. |
| `container` | Apple-Silicon laptop | Native Apple `container`: power on/off + read the vmnet lease. `create` runs the [containers/apple](containers/apple) base image — build it first. |
| `utm` | Apple-Silicon laptop | UTM.app, driven via AppleScript (the App Store build ships no CLI). `create` downloads the ~200 MB Debian cloud image on first use, seeds cloud-init, and pins the VM to `192.168.64.<NNN>`; power on/off. |
| `proxmox` | a Proxmox host | An always-on Debian VM on a Proxmox host, adopted like `generic` — no power control, no `create`. Recorded so the registry knows the platform. |

The `proxmox` backend does not talk to the Proxmox API: you create the VM on
the Proxmox side however you like (mpd-virt does not automate the cloud-init —
doing it by hand is good for understanding the basics), then adopt it with
`takeover --backend=proxmox`. Takeover, `update`, and reachability all work as
for any other box; create/delete of the VM itself stay manual on the Proxmox
side.

## Verbs

| Verb | Args | Role |
|---|---|---|
| `takeover <NNN> [IP]` | `--backend= --username=` | Adopt a reachable Debian box: run the bootstrap over SSH, install `mpd`, push the CA + LAN service names, register it, write ssh-config, wire reachability, check CA trust. IP is resolved by name when omitted (parallels/container); pass it for `generic`. **Precondition:** the box must first be prepared with mpd's `setup/mpd-prepare-takeover.sh` (run on the box; converts it to systemd-resolved) — takeover refuses otherwise. Boxes made by `create` come prepared already. |
| `create <NNN>` | `--backend=<utm\|container> --image= --memory= --disk= --pubkey= --username=` | Provision a new local VM, then take it over. `--image` is the container base image (default `mpd-virt-container-apple`), `--memory` defaults to 10g, `--disk` (utm) to 80g, `--pubkey` to `~/.ssh/id_ed25519.pub`. For laptop-local backends only; Proxmox/cloud VMs are made by hand and adopted with `takeover`. |
| `start <NNN>` | `--username=` | Bring an adopted box into service: power it on (parallels/container/utm; a no-op for generic/proxmox), find its current IP, update the registry + ssh-config, refresh the LAN service names, register the mpd-proxy overlay, verify. |
| `stop <NNN>` | — | Detach from the overlay and power the box off (a no-op for generic/proxmox). |
| `update <NNN>` | `--username=` | Pull + rebuild `mpd` on the VM and re-run `mpd --vm-setup`, then re-wire reachability. Runs mpd's own `bootstrap/99-update.sh` over SSH. |
| `delete <NNN>` | `--keep-vm --yes` | Remove the VM and its registry entry (keeps the root CA). `--keep-vm` leaves the hypervisor VM in place. |
| `list` (`ls`) | `--json` | Registered VMs, with a live `:22` reachability probe. |
| `server …` | `add / list / delete / cert / sync` | Manage LAN service hosts (non-VM machines) and their certs — see [`docs/LAN_SERVERS.md`](docs/LAN_SERVERS.md). |
| `ca [export]` | `--path=` | Print the root CA's public certificate (to install in another host's trust store). |
| `uninstall` | `--yes` | Stop every box (kept, re-adoptable), wipe `~/.mpd-virt` **except the root CA**, strip ssh-config blocks, and report the follow-ups it won't do for you (mpd-proxy, keychain, binary). |

## Reachability: two tiers

The container subnet `10.163.<NNN>.0/24` inside each VM is **sealed from the
LAN**: an in-VM nftables firewall drops routing into it from everything except
the VM's own bridge and `wg0`. So the only things a VM exposes on its network
are `ssh` (tcp/22) and WireGuard (udp/51820), both cryptographically
authenticated — which is what makes it safe to run a VM anywhere reachable by
IP. The developer's Mac, though, reaches the *whole* subnet (project URLs are
served at container IPs): the WireGuard peer routes `10.163.<NNN>.0/24`, and
the SOCKS tier rides sshd on the VM. Two ways in, and mpd-virt sets up both:

- **Simple — SOCKS over SSH (start here).** `ssh -N mpd-<NNN>-socks` opens a
  SOCKS5 proxy on `127.0.0.1:1080`; point a dedicated browser at it (with remote
  DNS) and `*.mpd.test` resolves and serves through the VM. No `sudo`, no extra
  daemon, one VM at a time.
- **Advanced — WireGuard overlay
  ([mpd-proxy](https://github.com/mutms/mpd-proxy)).** A small privileged helper
  running one WireGuard tunnel + split DNS, so every `*.mpd.test` name resolves
  transparently for **every app** and **several VMs at once**. `sudo mpd-proxy
  up` once; the daily driver when you run VMs regularly.

Either way, trusting the mpd root CA makes `*.mpd.test` HTTPS work — in the
System Keychain (transparent, every app) or imported into that dedicated browser
(no `sudo`). `start`/`takeover` check Keychain trust and print the SOCKS
instructions whenever mpd-proxy isn't running.

## ssh-config

`takeover`/`start` write one managed block per VM into `~/.ssh/config`:

- `mpd-<NNN>` — the box itself.
- `mpd-<NNN>-runtime` — the unified runtime container, via `ProxyJump`
  through the box (works with or without the overlay, since the jump rides the
  box's sshd). IDEs (PhpStorm Gateway, VS Code Remote-SSH) use this directly.
- `mpd-<NNN>-socks` — `DynamicForward 1080`, the SOCKS tier above.

All ride plain SSH to the box, so they work even when mpd-proxy is down.

## Registry

One `~/.mpd-virt/<NNN>/env` file per adopted VM, shell-style:

```
MPD_VM_OCTET=141
MPD_VM_NAME=mpd-141
MPD_VM_BACKEND=generic        # generic | parallels | container | utm | proxmox
MPD_VM_IP=192.168.1.146
MPD_VM_USER=skodak
```

## State / secrets layout (on the Mac)

```
~/.mpd-virt/                       ← everything mpd-virt owns
├── conf/
│   ├── caroot/                    ← the root CA — SURVIVES `delete` and `uninstall`
│   │   ├── rootCA.pem             ← trust anchor; pushed to VMs, trusted in the Keychain
│   │   └── rootCA-key.pem         ← 0600. NEVER leaves this Mac
│   ├── lan-hosts                  ← rendered hosts(5) file pushed into VMs (takeover/create/start/update, `server sync`)
│   ├── cloud-images/              ← cached Debian cloud-image archive (utm `create`)
│   └── utm-staging/<name>/        ← disk + cidata seed while UTM imports them (transient)
├── servers/<name>/                ← LAN service hosts (see docs/LAN_SERVERS.md)
│   ├── env                        ← MPD_SERVER_{NAME,IP}
│   ├── cert.pem / key.pem         ← leaf signed directly by the root; key 0600
│   └── sans                       ← issued SAN list, so a re-issue reproduces it
└── <NNN>/                         ← per-VM, removed by `delete`
    ├── env                        ← registry entry (see Registry above)
    └── ca/
        ├── vmCA.pem               ← this VM's signing CA, signed by the root
        └── vmCA-key.pem           ← 0600. Pushed to the VM
```

Two environment overrides exist, mostly for tests and dry-runs:
`MPD_VIRT_ROOT` relocates `~/.mpd-virt`, and `MPD_VIRT_SSH_CONFIG` points the
managed blocks at a file other than `~/.ssh/config`.

Keeping the root CA across `delete` and `uninstall` is deliberate: a re-adopt
reuses the same trust anchor, so you never have to re-trust a fresh-fingerprint
CA. Delete `caroot/` by hand only when you truly want a new one. `~/.mpd/` is
**not** created on the host — that path is exclusively the in-VM runtime state
directory inside each mpd VM.

The certificate design (why there are two CAs, name constraints, what a
compromised VM can and cannot forge) and the trust model live in
[`docs/SECURITY.md`](docs/SECURITY.md).

## Code layout

The binary is Go, built from `go/` into `bin/mpd-virt` by `make build`:

- `go/cmd/mpd-virt/` — main; version is stamped via `-ldflags` in the Makefile
- `go/internal/cli/` — the cobra command tree; one file per verb
- `go/internal/backend/` — power and address through each backend's CLI
- `go/internal/ca/` — the local CA: root, per-VM intermediates, server leaves
- `go/internal/registry/` — which boxes are adopted (`~/.mpd-virt/<NNN>/env`)
- `go/internal/server/` — LAN service hosts and their certs (`server …`)
- `go/internal/sshconfig/` — the managed `~/.ssh/config` blocks
- `go/internal/proxy/` — client for a running mpd-proxy's control socket
- `go/internal/host/` — drives a box over SSH from the Mac
- `go/internal/paths/` — the host-side filesystem locations mpd-virt owns
- `go/internal/vmid/` — id parsing and everything derived from `NNN`
- `go/internal/exec/` — the ONLY package that runs external commands
  (allow-listed); everything else goes through it

## Documentation map

- `README.md` — deliberately terse: what it is, quickstart, pointers here
- this file — the detailed reference (verbs, backends, state, layout)
- `docs/SECURITY.md` — trust model, certificate chain, CA backup, known gaps
- `docs/LAN_SERVERS.md` — LAN service hosts (non-VM machines) and their certs
- `containers/apple/README.md` — the Apple `container` base image

There is no `docs/proposals/` and none should be created — design notes go
into `docs/` proper (or straight into code comments), and shipped behavior is
documented only in the canonical files above. The only planned platform work
is Linux and Windows/WSL host support, keyed on `GOOS` in this same codebase,
much later.

## Validation

- `make build` (writes `bin/mpd-virt`), `make test vet fmt-check`
- `make lint-shell` after touching shell scripts (needs shellcheck)
- End-to-end: `mpd-virt create <NNN> --backend=container` against a locally
  built image, then `list`, `stop`, `start`, `delete --keep-vm`.
- Keep changes scoped; update the owning doc above when behavior moves.
