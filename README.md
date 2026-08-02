# mpd-virt

macOS host-side orchestrator for [mpd](https://github.com/mutms/mpd).
Creates and manages `mpd` VMs on the user's Mac. The binary is called
`mpd-virt`.

This is the Swift replacement for the bash scripts that previously lived
under `setup/macos/` in the mpd repo.

**Hypervisor backends.** Three compiled in:

- **`parallels`** — Parallels Desktop Pro (`prlctl`). Initial scope: `clone` from an `mpd-template-<suffix>` template + lifecycle (`start`/`stop`/`delete`). Gains `create` later.
- **`utm`** — UTM (`utmctl` + AppleScript). Initial scope: `create` from a cloud-init seed ISO + lifecycle. Gains `clone` later.
- **`general`** — no hypervisor. Adopts any reachable Debian Trixie VM by IP. Only `setup` (and bookkeeping for `delete`/`list`/`show`/`doctor`) is meaningful here.

Pick a default with `mpd-virt backend set-default <name>` (persists to `~/.mpd-virt/conf/backend.env`), or pass `--backend=<name>` on every invocation.

## Sibling repos (planned)

- `mpd-virt-linux` — Linux host (libvirt/KVM and possibly others).
- `mpd-virt-windows` — Windows host (Hyper-V and possibly others).

Each per-OS repo is its own self-contained Swift project with its own
`mpd-virt` binary. No source sharing between repos; small repos, simple
builds. Hypervisor variety lives *inside* each repo as plugins.

## Verbs

The 3-digit octet `NNN` is the canonical key for every VM — and it keys the addressing too: name `mpd-<NNN>`, static IP `10.211.55.<NNN>`, registry dir `~/.mpd-virt/<NNN>/`, container subnet `10.163.<NNN>.0/24`, DNS zone `<NNN>.mpd.test`. Because each VM owns a distinct subnet and a distinct zone, **several VMs are reachable from this Mac at once**: one static route and one `/etc/resolver/<NNN>.mpd.test` per VM, none of which conflict (macOS `resolver(5)` matches longest suffix). Note the bare `mpd.test` apex does not resolve — with two VMs up it could only mean one of them.

| Verb | Args | Role |
|---|---|---|
| `create <NNN>` | `--backend= --username= --vm-disk= --vm-ram= --yes` | User-friendly. Materialize a new VM (UTM cloud-init → eventually Parallels too) → `setup` → interactive `diag`. |
| `clone <NNN>` | `--backend= --template=mpd-template-<suffix> --username= --vm-disk= --vm-ram= --yes` | User-friendly. Duplicate an existing VM (Parallels `prlctl clone` → eventually UTM too) → `setup` → interactive `diag`. |
| `setup <NNN>` | `--ip= --backend= --username= --debug` | **VM side only.** Set up host↔VM SSH, run the in-VM bootstrap pipeline, install `mpd`. Non-interactive — for advanced/scripted use. Finishes with `diag --non-interactive`. |
| `diag <NNN>` | `--non-interactive` | **macOS side.** Mandatory phase: registry → backend → ping → hostname check → SSH alias. Optional phase: DNS / routing check + CA trust suggestion (always reported; interactive mode also pauses to apply fixes and re-test). |
| `update <NNN>` | — | Pull latest mpd source on the VM, rebuild the `mpd` binary, re-run `mpd --vm-setup`. Just runs `bash /opt/mpd/bootstrap/99-update.sh` over SSH — the update flow itself is mpd's contract, not mpd-virt's. |
| `delete <NNN>` | `--keep-vm --yes` | Remove VM and registry entry. `--keep-vm` keeps the hypervisor VM (re-add with `setup`). |
| `start <NNN>` | — | Hypervisor start. General: hard error. |
| `stop <NNN>` | `--kill` | Hypervisor suspend (or hard-stop with `--kill`). General: hard error. |
| `list` | `--json` | List registered VMs. Default verb. |
| `uninstall` | `--force --yes` | Per-machine cleanup: CA from System Keychain, `~/.mpd-virt/conf/`, every `/etc/resolver/<NNN>.mpd.test` and every `10.163.x.0/24` route (plus the pre-per-VM-zone leftovers). |
| `backend list` | — | Compiled-in backends + capabilities + default. |
| `backend set-default` | `<name>` | Persist default backend to `~/.mpd-virt/conf/backend.env`. |

### Building a Parallels template (for `clone`)

`mpd-virt clone` duplicates an existing Parallels VM and runs the
bootstrap pipeline against the copy. Build the template once, clone
from it as many times as you want.

Build the template:

1. **Configure Parallels Desktop Pro Shared network** to use
   `10.211.55.1–99` as its DHCP range (Preferences → Network → Shared
   → "Provide IP addresses via DHCP" → upper bound 99). mpd VMs take
   static IPs from `.100+`, so they never collide with DHCP guests.
2. **Install Debian Trixie (13)** in a new Parallels VM: Debian desktop
   environment, GNOME, SSH server, standard system utilities.
3. **Install Parallels Tools** (Actions → Install Parallels Tools, or
   `sudo bash /media/cdrom/installer/install-cli.sh -i` from a guest
   terminal if the GUI path doesn't run).
4. **Name the VM** `mpd-template-<suffix>` (e.g. `mpd-template-trixie`).
   The bootstrap's hostname gate also accepts `mpd-sandbox-<suffix>`.
5. **Disable in-guest automatic updates:**

   ```
   sudo systemctl mask packagekit packagekit-offline-update
   sudo systemctl disable --now unattended-upgrades
   ```

   Why, and what mpd does about it when you skip it, is documented once
   in mpd itself — [`setup/README.md` §"Prepare the guest"](https://github.com/mutms/mpd/blob/main/setup/README.md#prepare-the-guest-disable-in-guest-automatic-updates).
   It applies to every guest you keep and re-clone, not just Parallels
   templates.
6. **Convert to Template** in Parallels: File → Convert to Template
   (optional — full clones from a regular VM work too).

The bootstrap pipeline (`mpd-virt setup` runs it automatically after
`clone`) handles the rest — converting NetworkManager → systemd-networkd,
pinning the static IP, installing the runtime stack, building `mpd`,
and running `mpd --vm-setup`. No "sandbox take-over" step is needed.

Then run two mpd-virt-specific commands **from your Mac terminal**,
against the template VM, before the first clone:

1. **Authorize your SSH key on the VM** (one-time; you'll be prompted
   for the VM user's password):

   ```
   ssh-copy-id -i ~/.ssh/id_ed25519.pub USER@VM_IP
   ```

   Adjust the key path if you use a non-default identity. After this,
   `ssh USER@VM_IP` should work without a password.

2. **Enable passwordless sudo** for the dev user:

   ```
   ssh -t USER@VM_IP 'bash <(wget -qO- https://raw.githubusercontent.com/mutms/mpd/main/bootstrap/10-passwordless-sudo.sh)'
   ```

   The `-t` flag forces a remote PTY so the root password prompt uses
   noecho — your password will NOT echo to the screen.

If you skip either, `mpd-virt setup` will pause and print the exact
command for you to run in another window before continuing — the
template path just front-loads both.

Then clone with:

```
mpd-virt clone 150 --template=mpd-template-trixie --username=USER --backend=parallels
```

### Setup vs diag — division of labor

- **`setup`** owns the VM. It establishes SSH, runs the bootstrap chain
  on the VM (10–50), pushes the CA to `/var/lib/mpd/conf/`,
  runs `mpd --vm-setup` inside the VM, and registers the VM in
  `~/.mpd-virt/<NNN>/`. It is non-interactive end-to-end — every input
  comes from the CLI or the registry.
- **`diag`** owns the Mac. It verifies the VM is healthy (mandatory
  phase), then reports DNS / routing / CA trust status (optional
  phase, always printed). With `--non-interactive` (used by `setup`)
  the optional phase only *prints* the suggested fix commands — the
  workflow doesn't stop. Interactive mode (used by `create` / `clone`)
  additionally pauses to let the dev apply each fix and re-tests.

### Setup dispatch

`setup` decides between **fix-known** mode (registry entry exists →
reuse stored backend/IP/user) and **first-time adoption** (no entry →
asks the backend via `locate(octet, ipHint:)`). Parallels can locate a
manually-created `mpd-<NNN>` by name; General falls back to whatever
`--ip` you pass; UTM is currently the same as General.

## Registry

The registry is the set of `~/.mpd-virt/<NNN>/env` files, one per known
VM. Each file is shell-style key=value:

```
MPD_VM_OCTET=155
MPD_VM_NAME=mpd-155
MPD_VM_BACKEND=parallels        # parallels | utm | general
MPD_VM_IP=10.211.55.155
MPD_VM_USER=skodak
MPD_VM_UUID={abc12345-…}         # omitted for general
MPD_VM_DISK=80G                  # diagnostic (when create/clone set it)
MPD_VM_RAM=8G                    # diagnostic
```

## State / secrets layout (on the Mac)

```
~/.mpd-virt/                       ← everything mpd-virt owns
├── conf/                          ← identity (survives every `delete`)
│   ├── caroot/
│   │   ├── rootCA.pem             ← trust anchor; pushed to VMs, trusted in the Keychain
│   │   ├── rootCA-key.pem         ← 0600. NEVER leaves this Mac
│   │   └── rootCA.srl             ← serial counter
│   ├── service/
│   └── backend.env                ← MPD_VIRT_DEFAULT_BACKEND=<name>
└── <NNN>/                         ← per-VM, removed by `delete`
    ├── env                        ← registry entry (see Registry above)
    └── ca/
        ├── vmCA.pem               ← this VM's signing CA, signed by the root
        └── vmCA-key.pem           ← 0600. Pushed to the VM — see below
```

**Why two CAs.** The root's private key never leaves this Mac. Each VM
instead gets its own intermediate, name-constrained to that VM's zone
alone (`permitted;DNS:<NNN>.mpd.test`, `pathlen:0`), which the in-VM
`mpd` uses to sign its service and project certificates:

```
mpd Root CA                        key: this Mac, and only this Mac
└── mpd VM 200 CA                  key: pushed to VM 200
      permitted;DNS:200.mpd.test
      └── 200.mpd.test, moodle.200.mpd.test, …   signed inside the VM
```

So a compromised VM can forge names in its own zone and nowhere else —
not another VM's zone, and not names issued directly under `mpd.test`.
The VM CA lives under `<NNN>/` rather than `conf/` because that is its
lifetime: `delete` takes it with the VM, and a re-created VM at the same
octet gets a fresh one. Its validity is capped by whatever the root has
left, since nothing may outlive its issuer.

LAN machines that are not VMs — `proxmox.mpd.test`, `forge.mpd.test`,
`runner.mpd.test` — live under `~/.mpd-virt/servers/<name>/` and get
leaves signed directly by the root here on the Mac. See
[`docs/LAN_SERVERS.md`](docs/LAN_SERVERS.md).

Octet range for managed VMs: `100–254` (Parallels Shared DHCP owns 1–99).
The sandbox VM uses ID `000` and lives in the main mpd repo's sandbox
flow, not here.

`~/.mpd/` is **not** created on the host — that path is exclusively the
in-VM runtime state directory inside each mpd VM.

Full design rationale: see
[`docs/proposals/macos-host-state.md`](docs/proposals/macos-host-state.md).

## Build

```bash
make install      # produces ./bin/mpd-virt
```

Requires Xcode command-line tools.
