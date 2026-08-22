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

`NNN` is the VM's id: a plain identifier `100`–`254`, always exactly three
digits (`go/internal/vmid` is the single source of truth — ids carry no
class or range semantics, and there is no zero-padding: the same three
characters appear everywhere the id does). It keys the name `mpd-<NNN>`, the
registry dir `~/.mpd-virt/<NNN>/`, the container subnet `10.163.<NNN>.0/24`,
the overlay gateway `10.163.<NNN>.1`, the DNS zone `<NNN>.mpd.test`, and on
proxmox also the VMID and the LAN address's last octet. Starting at 100
matches Proxmox's own VMID floor and keeps ids clear of low
router/DHCP-infrastructure addresses. The box's own address is found by name
(or given to `adopt` explicitly), not a fixed value. Several VMs are
reachable at once — the bare `mpd.test` apex deliberately does not resolve.
Sandbox VMs (the main mpd repo's sandbox flow) use ordinary ids from the
same range — there are no special ids.

## Backends

`--backend=<name>` is required on `create` and on the first `adopt` of a
box — there is deliberately no default. Once a box is registered, every verb
(including a re-adoption) reads the backend recorded in the registry; passing
`--backend` to a re-adoption changes the record.

| Backend | Host | What it does |
|---|---|---|
| `generic` | anywhere | Adopt an **already-running** Debian box by IP — a cloud VM, bare metal. No power control (it stays up). The path for anything not on the laptop. |
| `parallels` | macOS laptop | Parallels Desktop Pro (`prlctl`): power on/off + find the box's current DHCP IP. |
| `container` | Apple-Silicon laptop | Native Apple `container`: power on/off + read the vmnet lease. `create` runs the [container/](container) base image — build it first. |
| `utm` | Apple-Silicon laptop | UTM.app, driven via AppleScript (the App Store build ships no CLI). `create` downloads the ~200 MB Debian cloud image on first use, seeds cloud-init, and pins the VM to `192.168.64.<NNN>`; power on/off. |
| `proxmox` | a Proxmox host | A Debian VM on a Proxmox host: power on/off + state through the Proxmox REST API (token in `~/.mpd-virt/conf/backends/proxmox.env` — see [`docs/PROXMOX.md`](docs/PROXMOX.md)); no `create`. |

The `proxmox` backend talks to the Proxmox REST API for VM state, start,
graceful shutdown, and — once a box is adopted — its live IP through the
guest agent. Provisioning stays manual by design — you create the VM on the
Proxmox side (mpd-virt does not automate the cloud-init; doing it by hand is
good for understanding the basics), then adopt it with `adopt
--backend=proxmox`. The VM number is the Proxmox VMID. For the box's LAN
address there are two sources, in order: at first adoption, before the box
runs a guest agent, the address is derived by convention — `NETWORK` in
`~/.mpd-virt/conf/backends/proxmox.env` with the last octet replaced by the
number — so the cloud-init assigns a static address matching that rule.
After adoption the box runs `qemu-guest-agent` (the prep script and
bootstrap install it), so `start` asks the API for its real address, which
is authoritative and finds a box that sits off the convention on a
non-standard lease. Create/delete of the VM itself stay manual on the
Proxmox side — [`docs/PROXMOX.md`](docs/PROXMOX.md) walks through both
installation flavors and the API token setup.

## Verbs

| Verb | Args | Role |
|---|---|---|
| `adopt <NNN> [IP]` | `--backend= --username=` | Adopt a reachable Debian box: run the bootstrap over SSH, install `mpd`, push the CA + LAN service names + developer assets + env, register it, write ssh-config, wire reachability, check CA trust. IP is resolved by name when omitted — the backend's own source (parallels/container, or the proxmox guest agent once adopted), or mDNS for a box that advertises itself (cloud-init or the prep script sets up avahi); pass it explicitly when neither reaches the box. **Preconditions:** a reachable sshd with key auth and passwordless sudo on Debian Trixie — nothing about the network stack is gated; mpd's DNS works on whatever the box has (it keeps its records in `/etc/hosts` and forwards through the box's own `/etc/resolv.conf`). avahi (mDNS discovery) and qemu-guest-agent (the proxmox IP query) are conveniences, not requirements: the cloud-init image ships them and the bootstrap installs them regardless. Proxmox VMs (their cloud-init seed) and `create`-made boxes (utm/container) are adoptable as they are. mpd's `setup/mpd-prepare-adopt.sh` (run on the box) standardises a box that did **not** come from cloud-init onto the systemd-networkd + systemd-resolved stack and adds the same avahi + qemu-guest-agent conveniences. The two cases that want it are an already-running generic/bare-metal box, and a sandbox VM (the intended upgrade path from trying mpd to the daily managed workflow, projects intact): a sandbox deliberately ships without sshd, and the prep script installs it idempotently on top of the sandbox's own network conversion. |
| `create <NNN>` | `--backend=<utm\|container> --image= --memory= --disk= --pubkey= --username=` | Provision a new local VM, then take it over. `--image` is the container base image (default the published `ghcr.io/mutms/mpd-virt-container-apple:<tag>` from `backend.DefaultContainerImage()`), `--memory` defaults to 10g, `--disk` (utm) to 80g, `--pubkey` to `~/.ssh/id_ed25519.pub`. For laptop-local backends only; Proxmox/cloud VMs are made by hand and adopted with `adopt`. |
| `start <NNN>` | `--username=` | Bring an adopted box into service: power it on (parallels/container/utm/proxmox; a no-op for generic), find its current IP, update the registry + ssh-config, refresh the LAN service names + developer assets + env, register the mpd-proxy overlay, verify. Safe to re-run on a live box — the backend's state is read first, so an already-running box is not started again. |
| `stop <NNN>` | — | Detach from the overlay and power the box off (a no-op for generic, and for a box already stopped). |
| `update <NNN>` | `--username=` | Refresh the LAN service names + developer assets + env, then run mpd's bootstrap step 20 over SSH (apt dist-upgrade + the package set, so a stale template or image converges) and `mpd --vm-upgrade` (pull + rebuild + re-run `mpd --vm-setup`), then re-wire reachability. |
| `remove <NNN>` | `--yes` | Un-adopt a box (aliases `delete`, `rm`): detach it from the overlay, strip its ssh-config block, and delete `~/.mpd-virt/<NNN>/` — registry entry, pinned host key, per-VM CA. **Powers the box off** (a no-op for generic) so its hypervisor object can be deleted right after — a running Apple container refuses `container rm` — but **never deletes the box itself**: destroying it belongs to the hypervisor (UTM, `container delete`, the Proxmox UI), and a stopped box stays re-adoptable. Keeps the root CA. A rebuilt box comes back via `remove` + `adopt`: the new host key is recorded as a deliberate first contact and its certs are reissued under a fresh per-VM CA. |
| `list` (`ls`) | `--json` | Registered VMs, with a live `:22` reachability probe. |
| `server …` | `add / list / delete / cert / sync` | Manage LAN service hosts (non-VM machines) and their certs — see [`docs/LAN_SERVERS.md`](docs/LAN_SERVERS.md). |
| `ca [export]` | `--path=` | Print the root CA's public certificate (to install in another host's trust store). |
| `uninstall` | `--yes` | Stop every box (kept, re-adoptable), wipe `~/.mpd-virt` **except the root CA and your `mpd-virt.env`**, strip ssh-config blocks, and report the follow-ups it won't do for you (mpd-proxy, keychain, binary). |

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
(no `sudo`). `start`/`adopt` check Keychain trust and print the SOCKS
instructions whenever mpd-proxy isn't running.

## ssh-config

`adopt`/`start` write one managed block per VM into `~/.ssh/config`:

- `mpd-<NNN>` — the unified runtime container, via `ProxyJump` through the
  box (works with or without the overlay, since the jump rides the box's
  sshd). The bare name goes here because it is where the developer, their
  IDE (PhpStorm Gateway, VS Code Remote-SSH) and their agent actually work.
- `mpd-<NNN>-vm` — the box itself: `mpd`, podman, the assets tree.
- `mpd-<NNN>-socks` — `DynamicForward 1080`, the SOCKS tier above.

The runtime stanza's `HostName` is the bare `runtime`, not the FQDN: with
`ProxyJump` the *box* resolves the target, and mpd publishes `runtime` as
an alias on the runtime's line in the box's `/etc/hosts`. That makes the
block directly transcribable into an SSH app that offers a jump-host field
but reads no config file (Terminus on an iPad, say) — **jump =
`mpd-<NNN>-vm` at the box's address, host = `runtime`**.

Host side only. Inside the VM the bare `mpd-<NNN>` is that machine's own
hostname, so mpd's in-VM aliases for the runtime stay `runtime` and
`mpd-<NNN>-runtime` (see the sibling repo's `vm.EnsureSSHConfig`). The
runtime's prompt renders `mpd-<NNN>` and the VM's `mpd-<NNN>-vm`, so the
prompt always echoes the alias you typed — cosmetic only, no hostname is
changed.

All ride plain SSH to the box, so they work even when mpd-proxy is down.

Every stanza pins the box's ssh host key: first contact (adopt/create)
records it into `~/.mpd-virt/<NNN>/known_hosts` under the stable alias
`mpd-<NNN>` (`HostKeyAlias`, so DHCP churn does not reset the pin), and a
changed key is refused — by the aliases and by mpd-virt's own verbs alike.
See `docs/SECURITY.md` for why that pin is the identity of the box.

## Registry

One `~/.mpd-virt/<NNN>/env` file per adopted VM, shell-style:

```
MPD_VM_OCTET=141
MPD_VM_NAME=mpd-141
MPD_VM_BACKEND=generic        # generic | parallels | container | utm | proxmox
MPD_VM_IP=10.1.1.141
MPD_VM_USER=skodak
```

## State / secrets layout (on the Mac)

```
~/.mpd-virt/                       ← everything mpd-virt owns
├── conf/
│   ├── caroot/                    ← the root CA — SURVIVES `remove` and `uninstall`
│   │   ├── rootCA.pem             ← trust anchor; pushed to VMs, trusted in the Keychain
│   │   └── rootCA-key.pem         ← 0600. NEVER leaves this Mac
│   ├── lan-hosts                  ← rendered hosts(5) file pushed into VMs (adopt/create/start/update, `server sync`)
│   ├── cloud-images/              ← cached Debian cloud-image archive (utm `create`)
│   └── utm-staging/<name>/        ← disk + cidata seed while UTM imports them (transient)
├── assets/                        ← OPTIONAL: your own scripts/files, mirrored into every box (see Developer assets)
├── mpd-virt.env                   ← OPTIONAL: your MPD_* defaults, pushed into every box — SURVIVES `uninstall` (see Developer env)
├── proxy/                         ← mpd-proxy's control socket dir (0700, created and used by mpd-proxy; socket dies with the proxy)
├── servers/<name>/                ← LAN service hosts (see docs/LAN_SERVERS.md)
│   ├── env                        ← MPD_SERVER_{NAME,IP}
│   ├── cert.pem / key.pem         ← leaf signed directly by the root; key 0600
│   └── sans                       ← issued SAN list, so a re-issue reproduces it
└── <NNN>/                         ← per-VM, removed by `remove`
    ├── env                        ← registry entry (see Registry above)
    ├── known_hosts                ← the box's pinned ssh host key (alias mpd-<NNN>)
    └── ca/
        ├── vmCA.pem               ← this VM's signing CA, signed by the root
        └── vmCA-key.pem           ← 0600. Pushed to the VM
```

The whole tree is owner-only: every invocation re-asserts 0700 on
directories and strips group/other bits from files (owner execute bits on
`assets/` scripts survive). Nothing under `~/.mpd-virt` is another user's
to read — least of all the CA keys and the proxmox token.

Two environment overrides exist, mostly for tests and dry-runs:
`MPD_VIRT_ROOT` relocates `~/.mpd-virt`, and `MPD_VIRT_SSH_CONFIG` points the
managed blocks at a file other than `~/.ssh/config`.

Keeping the root CA across `remove` and `uninstall` is deliberate: a re-adopt
reuses the same trust anchor, so you never have to re-trust a fresh-fingerprint
CA. Delete `caroot/` by hand only when you truly want a new one. `mpd-virt.env`
survives `uninstall` for a different reason — mpd-virt never wrote it, so it
cannot write it back; nothing this tool generated is worth losing your own
defaults over. Delete it by hand too. `~/.mpd/` is
**not** created on the host — that path is exclusively the in-VM runtime state
directory inside each mpd VM.

The certificate design (why there are two CAs, name constraints, what a
compromised VM can and cannot forge) and the trust model live in
[`docs/SECURITY.md`](docs/SECURITY.md).

## Developer assets

`~/.mpd-virt/assets/` is an optional directory of **your own** scripts and
files — private hacks, experiments, site-specific wiring. `adopt`,
`create`, `start` and `update` mirror it into every box at
`/opt/mpd-virt/assets/`, and if it contains a `bin/`, that directory is
appended to `PATH` for interactive shells via
`/etc/profile.d/mpd-virt-assets.sh`. Nothing else happens: mpd-virt carries
the files and never runs, reads, or interprets them.

This exists so a one-off need does not become a feature here. Something
that applies to one developer's LAN — a static route to an office gateway,
a scratch tool — is a script in your own tree, not a flag in this repo.

- **The Mac is the source of truth.** The push is a *mirror*: the box's copy
  is removed and replaced, so a file deleted on the Mac disappears from the
  box on the next lifecycle verb. Edit on the Mac and re-push, never in the
  VM — an in-VM edit is overwritten on the next push anyway. The in-VM copy
  is **owned by the dev user**, like `/opt/mpd`: root ownership would protect
  nothing on a passwordless-sudo VM and only gets in the way.
- **No assets directory means no action**, not "remove them from the box" —
  absence is the normal state for a VM that never wanted any.
- **Best-effort.** A failed push warns and points at `mpd-virt start <NNN>`;
  it never fails an adoption, start, or update.
- **Not under `/opt/mpd`** — that is mpd's git checkout, which
  `mpd --vm-upgrade` pulls; `/opt/mpd-virt` is mpd-virt's own slot and
  is never touched by an mpd update.
- **VM-only.** mpd bind-mounts `/opt/mpd` read-only into every runtime
  container but knows nothing about `/opt/mpd-virt`, so these do not appear
  inside containers.
- `sudo <script>` will not find them (sudoers `secure_path` does not include
  the directory) — same as mpd's in-runtime tools. Scripts `sudo` internally.

## Developer env

`~/.mpd-virt/mpd-virt.env` is an optional file of **your own** `MPD_*`
defaults — PHP version, Moodle admin password, Behat preferences, the
`MPD_RUNTIME_CONTROL` switch. `adopt`, `create`, `start` and `update`
push it into every box at `/var/lib/mpd/env/mpd-virt.env`, where mpd layers
it under each project's own `mpd.env`. The keys and their meanings belong
to mpd, not here — see its `assets/vm/mpd-virt.env` template and
`docs/ARCHITECTURE.md` §8; mpd-virt only carries the file.

The layer it feeds is scoped to *you*, not to a box. A VM runs one runtime,
so per-VM defaults were a distinction without a difference — while a
developer routinely runs several boxes that should agree on how they
behave. Holding the file on the Mac is what makes one edit reach all of
them.

- **The Mac is the source of truth**, as with assets: an edit made inside a
  VM survives only until the next lifecycle verb.
- **No file on the Mac means no action.** Absence leaves whatever the box
  has, which matters for a sandbox VM adopted later: its hand-written file
  survives until you actually put one on the Mac.
- **Digest-guarded.** Every lifecycle verb calls it, and the common case
  costs one remote `sha256sum` and nothing else.
- **Best-effort**, and nothing to republish afterwards: mpd re-reads the
  file per command, and the runtime container sees it through a directory
  bind-mount.
- **Pushed as the dev user, not root** — unlike the assets mirror. That
  directory is mpd's, dev-owned, and `mpd --vm-setup` seeds the same path
  from mpd's template when nothing is there. A root-owned file would leave
  mpd unable to seed a replacement if the Mac's copy later went away.
- **Survives `uninstall`**, alongside the root CA. It is yours, and unlike
  `assets/` there is no copy on a box worth restoring from.

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
- `container/README.md` — the Apple `container` base image

There is no `docs/proposals/` and none should be created — design notes go
into `docs/` proper (or straight into code comments), and shipped behavior is
documented only in the canonical files above. The only planned platform work
is Linux host support, keyed on `GOOS` in this same codebase, much later.
Windows is not on the roadmap — WSL containers cannot run the mpd runtime's
systemd, and mdl-demo covers Windows users; if Windows ever happens it would
ride on the Linux path via WSL, sharing that code rather than its own.

## Validation

- `make build` (writes `bin/mpd-virt`), `make test vet fmt-check`
- `make lint-shell` after touching shell scripts (needs shellcheck)
- End-to-end: `mpd-virt create <NNN> --backend=container` against a locally
  built image, then `list`, `stop`, `start`, `remove`.
- Keep changes scoped; update the owning doc above when behavior moves.
