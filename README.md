# mpd-virt

macOS host-side orchestrator for [mpd](https://github.com/mutms/mpd). Adopts and
manages `mpd` development VMs from your Mac. The binary is `mpd-virt`.

An mpd VM is a Debian Trixie box running the in-VM
[`mpd`](https://github.com/mutms/mpd) platform. `mpd-virt` bootstraps it over
SSH, installs `mpd`, issues its certificates, and wires up host-side
reachability — then stays out of the way.

## Backends

`--backend=<name>` is required on every verb; there is deliberately no default
backend.

| Backend | Host | What it does |
|---|---|---|
| `generic` | anywhere | Adopt an **already-running** Debian box by IP — Proxmox, a cloud VM, bare metal. No power control (it stays up). The path for anything not on the laptop. |
| `parallels` | macOS laptop | Parallels Desktop Pro (`prlctl`): power on/off + find the box's current DHCP IP. |
| `container` | macOS laptop | Native Apple `container`: power on/off + read the vmnet lease. |
| `utm` | Apple-Silicon laptop | UTM (osascript): `create` from a cloud-init image + power. |

There is no dedicated Proxmox backend: a Proxmox-hosted VM is just an always-on
Debian box you created however you like (mpd-virt does not automate the
cloud-init — doing it by hand is good for understanding the basics) and adopt
with `--backend=generic`.

## Reachability: two tiers

The container subnet `10.163.<NNN>.0/24` inside each VM is **sealed**: an in-VM
nftables firewall drops inbound routing into it, and the WireGuard peer is scoped
to the gateway `.1` alone. So the only things a VM exposes on its network are
`ssh` (tcp/22) and WireGuard (udp/51820), both cryptographically authenticated —
which is what makes it safe to run a VM anywhere reachable by IP. Everything you
use (the portal, adminer, project URLs) is served by caddy on the VM's gateway
`10.163.<NNN>.1`. Two ways to reach it, and mpd-virt sets up both:

- **Simple — SOCKS over SSH (start here).** `ssh -N mpd-<NNN>-socks` opens a
  SOCKS5 proxy on `127.0.0.1:1080`; point a dedicated browser at it (with remote
  DNS) and `*.mpd.test` resolves and serves through the VM. No `sudo`, no extra
  daemon, one VM at a time. The recommended starting point for a new developer.
- **Advanced — WireGuard overlay
  ([mpd-proxy](https://github.com/mutms/mpd-proxy)).** A small privileged helper
  running one WireGuard tunnel + split DNS, so every `*.mpd.test` name resolves
  transparently for **every app** and **several VMs at once**. `sudo mpd-proxy
  up` once; the daily driver when you run VMs regularly.

Either way, trusting the mpd root CA makes `*.mpd.test` HTTPS work — in the
System Keychain (transparent, every app) or imported into that dedicated browser
(no `sudo`). `start`/`takeover` check Keychain trust and print the SOCKS
instructions whenever mpd-proxy isn't running.

## Other platforms

macOS-only for now. Linux/Windows host support may be added later in this same
codebase, keyed on `GOOS` — not separate per-OS repos.

## Verbs

`NNN` is the VM's id: a plain zero-padded identifier `001`–`254`. It keys the
name `mpd-<NNN>`, the registry dir `~/.mpd-virt/<NNN>/`, the container subnet
`10.163.<NNN>.0/24`, the overlay gateway `10.163.<NNN>.1`, and the DNS zone
`<NNN>.mpd.test`. The box's own address is found by name (or given with `--ip`),
not a fixed value. Several VMs are reachable at once — the bare `mpd.test` apex
deliberately does not resolve.

| Verb | Args | Role |
|---|---|---|
| `takeover <NNN> [IP]` | `--backend= --username=` | Adopt a reachable Debian box: run the bootstrap over SSH, install `mpd`, register it, write ssh-config, wire reachability, check CA trust. IP is resolved by name when omitted (parallels/container); pass it for `generic`. |
| `create <NNN>` | `--backend=<utm\|container> --disk= --username=` | Provision a new local VM from a cloud-init image, then take it over. For laptop-local backends only; Proxmox/cloud VMs are made by hand and adopted with `takeover --backend=generic`. |
| `start <NNN>` | `--username=` | Bring an adopted box into service: power it on (parallels/container; a no-op for generic), find its current IP, update the registry + ssh-config, register the mpd-proxy overlay, verify. |
| `stop <NNN>` | `--kill` | Detach from the overlay and power the box off (a no-op for generic). |
| `update <NNN>` | — | Pull + rebuild `mpd` on the VM and re-run `mpd --vm-setup`, then re-wire reachability. Runs mpd's own `bootstrap/99-update.sh` over SSH. |
| `delete <NNN>` | `--keep-vm --yes` | Remove the VM and its registry entry (keeps the root CA). `--keep-vm` leaves the hypervisor VM in place. |
| `list` (`ls`) | `--json` | Registered VMs, with a live `:22` reachability probe. Default verb. |
| `server …` | `add / list / delete / cert / sync` | Manage LAN service hosts (non-VM machines) and their certs — see [`docs/LAN_SERVERS.md`](docs/LAN_SERVERS.md). |
| `ca [export]` | `--path=` | Print the root CA's public certificate (to install in another host's trust store). |
| `uninstall` | `--yes` | Stop every box (kept, re-adoptable), wipe `~/.mpd-virt` **except the root CA**, strip ssh-config blocks, and report the follow-ups it won't do for you (mpd-proxy, keychain, binary). |

## ssh-config

`takeover`/`start` write one managed block per VM into `~/.ssh/config`:

- `mpd-<NNN>` — the box itself.
- `mpd-<NNN>-php` / `-node` / `-util` — the runtime containers, via `ProxyJump`
  through the box (the container IPs are sealed, so the jump rides the box's
  sshd). IDEs (PhpStorm Gateway, VS Code Remote-SSH) use these directly.
- `mpd-<NNN>-socks` — `DynamicForward 1080`, the SOCKS backup above.

All ride plain SSH to the box, so they work even when mpd-proxy is down.

## Registry

One `~/.mpd-virt/<NNN>/env` file per adopted VM, shell-style:

```
MPD_VM_OCTET=141
MPD_VM_NAME=mpd-141
MPD_VM_BACKEND=generic        # generic | parallels | container | utm
MPD_VM_IP=192.168.1.146
MPD_VM_USER=skodak
```

## State / secrets layout (on the Mac)

```
~/.mpd-virt/                       ← everything mpd-virt owns
├── conf/
│   └── caroot/                    ← the root CA — SURVIVES `delete` and `uninstall`
│       ├── rootCA.pem             ← trust anchor; pushed to VMs, trusted in the Keychain
│       ├── rootCA-key.pem         ← 0600. NEVER leaves this Mac
│       └── rootCA.srl             ← serial counter
├── servers/<name>/                ← LAN service hosts (see docs/LAN_SERVERS.md)
└── <NNN>/                         ← per-VM, removed by `delete`
    ├── env                        ← registry entry (see Registry above)
    └── ca/
        ├── vmCA.pem               ← this VM's signing CA, signed by the root
        └── vmCA-key.pem           ← 0600. Pushed to the VM — see below
```

Keeping the root CA across `delete` and `uninstall` is deliberate: a re-adopt
reuses the same trust anchor, so you never have to re-trust a fresh-fingerprint
CA. Delete `caroot/` by hand only when you truly want a new one.

**Why two CAs.** The root's private key never leaves this Mac. Each VM instead
gets its own intermediate, name-constrained to that VM's zone alone
(`permitted;DNS:<NNN>.mpd.test`, `pathlen:0`), which the in-VM `mpd` uses to sign
its service and project certificates:

```
mpd Root CA                        key: this Mac, and only this Mac
└── mpd VM 200 CA                  key: pushed to VM 200
      permitted;DNS:200.mpd.test
      └── 200.mpd.test, moodle.200.mpd.test, …   signed inside the VM
```

So a compromised VM can forge names in its own zone and nowhere else — not
another VM's zone, and not names issued directly under `mpd.test`. The root's own
`permitted;DNS:mpd.test` constraint means trusting it can vouch for no domain
outside `*.mpd.test`, which is what makes System-Keychain trust safe. The VM CA
lives under `<NNN>/` rather than `conf/` because that is its lifetime: `delete`
takes it with the VM, and a re-created VM at the same id gets a fresh one. Its
validity is capped by whatever the root has left, since nothing may outlive its
issuer.

LAN machines that are not VMs — `forge.mpd.test`, `runner.mpd.test`, … — live
under `~/.mpd-virt/servers/<name>/` and get leaves signed directly by the root
here on the Mac. See [`docs/LAN_SERVERS.md`](docs/LAN_SERVERS.md).

The sandbox VM uses id `000` and lives in the main mpd repo's sandbox flow, not
here. `~/.mpd/` is **not** created on the host — that path is exclusively the
in-VM runtime state directory inside each mpd VM.

Host-side trust model and rationale: see
[`docs/proposals/threat-model-and-ca-backup.md`](docs/proposals/threat-model-and-ca-backup.md).

## Build

```bash
make install      # produces ./bin/mpd-virt
```

Requires the Go toolchain (Go 1.24 or newer) — mpd-virt is a single Go binary
built from `go/`. No Xcode or Swift.

## Acknowledgments

Part of the [mpd](https://github.com/mutms/mpd) project. mpd and its related
tools are my first fully AI-driven project — the code and docs are largely
written by [Claude Code](https://claude.com/claude-code) (Anthropic) under my
direction (design and review stay human).

## License

Copyright (C) 2026 Petr Skoda. [GPL-3.0](LICENSE) or later.

Moodle is a registered trademark of [Moodle Pty Ltd](https://moodle.com).
