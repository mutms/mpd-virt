# Proposal: UTM backend for `mpd-virt`

**Priority:** high. Parallels Desktop Pro requires a paid license
(~$99/yr); UTM is free, runs natively on Apple Silicon via Apple's
Virtualization.framework, and is the zero-friction path for a Mac dev
who wants to evaluate mpd VM mode without buying a hypervisor.

## Status

Parked, ready to schedule. Implementation should be straightforward
now that the in-VM `bootstrap/` chain is uniform across hypervisors —
the per-hypervisor work is reduced to "create + configure a Debian
Trixie VM, set hostname, get SSH access." Everything from there is
the same bootstrap/10..60 sequence the Parallels backend already
drives.

Historical note: UTM support was fully working in the mpd repo's git
history before the `mpd-virt` split. The earlier implementation can
be lifted as a reference for the macOS-side wiring; the in-VM half
no longer needs separate plumbing thanks to `bootstrap/`.

## Goals

1. **A second backend in `mpd-virt`** — selectable at build time
   (`#if MPD_VIRT_BACKEND_UTM` or a SwiftPM target product), so each
   compiled binary still contains exactly one backend.
2. **Same `mpd-virt` verb surface** — `create`, `start`, `stop`,
   `delete`, `doctor`, `uninstall`. Users invoke the same CLI; the
   backend choice is invisible at runtime.
3. **Free + native on Apple Silicon** — UTM with the Apple
   Virtualization backend runs without third-party kernel extensions,
   no licensing barrier, no Rosetta translation on M-series.
4. **First-class for the evaluation path** — README's "kick the
   tires on macOS without buying anything" recommendation becomes
   the UTM backend.

## Non-goals

- **No backend autodetection at runtime.** Compile-time selection
  only. Reuses the Parallels-backend pattern.
- **No UTM/Parallels coexistence in one binary.** Each binary has
  exactly one backend; users who want both install both binaries
  (`mpd-virt-utm` vs `mpd-virt-parallels`, or two installations into
  separate paths).
- **No UTM-specific feature exposure** (snapshots, sharing, etc.).
  Lifecycle parity only — anything more is left to UTM's own GUI.

## UTM backend mechanism

UTM ships with `utmctl`, a CLI for VM lifecycle, and `.utm` bundles
describe VMs as plist + qcow2 disk. The backend hooks:

| Operation | Parallels (today) | UTM (proposed) |
|---|---|---|
| List VMs | `prlctl list -a` | `utmctl list` |
| Start | `prlctl start <uuid>` | `utmctl start <uuid>` |
| Stop | `prlctl stop <uuid>` | `utmctl stop <uuid>` |
| Delete | `prlctl delete <uuid>` | `utmctl delete <uuid>` |
| Get IP | `prlctl exec <uuid> ip ...` | UTM has limited guest-tools; fall back to DHCP lease lookup or guest IP via shared network |
| Clone from template | `prlctl clone mpd-template --name mpd-<N>` | UTM doesn't ship a CLI clone primitive — see "VM creation" below |

### VM creation

Parallels uses a one-time-built template + `prlctl clone`. UTM
doesn't have a clean template-clone CLI flow; the workable shapes:

- **Option A: ship a UTM bundle template** (`mpd-template.utm`) that
  the user double-clicks in UTM.app once to import, then `mpd-virt`
  duplicates the disk + plist files programmatically (cp + UUID
  rewrite) before `utmctl start`. Lower magic, requires the user to
  open UTM.app once.
- **Option B: drive cloud-init from scratch** like the Linux/Windows
  cloud-init flows — generate a seed ISO, attach as a CDROM, boot a
  vanilla Debian cloud image, eject on first reboot. No template
  required; first-VM creation takes a couple minutes longer but the
  flow is purely scripted.

**Recommendation:** Option B. Matches what `mpd-virt-linux` and
`mpd-virt-windows` already do, removes the manual template-import
step, and means `utmctl import` of a pre-baked template can be a
later optimization. The cloud-init path also makes mpd-virt's
"first run" experience identical across all three host OSes —
no "first run on macOS is different because Parallels" caveat.

### Networking

UTM supports several network modes; the relevant ones:

- **Shared** (NAT, host gets a route to the guest) — equivalent to
  Parallels' "Shared" mode. Guest gets a DHCP address from
  192.168.x.0/24, host can reach it directly. This is what we want.
- **Bridged** — guest joins the laptop's LAN. Overkill for mpd VM;
  also gives the VM a routable LAN address the user may not want.
- **Host-only** — guest is reachable only from the host. Works, but
  identical UX to Shared with extra configuration overhead.

Pick Shared. WireGuard tunnel setup (Mac peer + VM peer) is
identical to the Parallels backend; the only per-backend bit is
"how do I get the VM's guest IP." UTM's guest IP discovery is
weaker than Parallels' (no `prlctl exec`-equivalent); the workable
shapes are:

- DHCP lease lookup via the host's shared-network leases file
  (`/var/db/dhcpd_leases` for macOS internet sharing, or the
  hypervisor's own leases store)
- `arp -an | grep <mac>` after a known MAC is assigned in the
  UTM config
- Cloud-init writes the static IP into `/var/lib/mpd/conf/platform.env`
  and `mpd-virt` reads it back via a one-shot `ssh-from-known-host`
  attempt against a static IP allocated at create time

**Recommendation:** allocate a static IP at cloud-init time, the
same way the Linux/Windows backends already do, and skip dynamic
lease lookups entirely. The IP becomes the canonical identifier
the Mac side uses for SSH + WG endpoint configuration.

## What changes in mpd-virt

- **New backend module:** `mpd-virt/Backend/UTM/UTMBackend.swift`
  (and supporting files), conforming to whatever protocol the
  Parallels backend already implements (likely `Mpd.Virt.Backend`).
- **Build product:** a second SwiftPM executable target,
  `mpd-virt-utm`, selecting the UTM backend; the existing target
  selects Parallels. Or — a Makefile flag (`make install
  BACKEND=utm` vs `BACKEND=parallels`) producing one or the other.
  Pick whichever matches the existing per-platform conditional
  pattern.
- **Cloud-init seed code:** lift the existing Linux/Windows
  cloud-init seed-ISO generation (already in `mpd-virt-linux` and
  `mpd-virt-windows`); the UTM backend reuses it.
- **VM identity:** still octet-keyed (matches the
  `macos-host-state.md` proposal). The state files
  under `~/.mpd-virt/<octet>/` are backend-agnostic.

## What does *not* change

- **In-VM `mpd` binary** — zero changes. The bootstrap/ chain
  runs identically inside a UTM-hosted Debian Trixie VM.
- **`mpd-virt` verb surface** — same commands, same flags.
- **Mac-side networking setup** — WireGuard tunnel, CA trust,
  route, DNS resolver: all backend-agnostic.
- **Per-VM state model** — `~/.mpd-virt/<octet>/env`, etc., already
  designed to be backend-agnostic.

## Open questions

1. **Apple Silicon native vs Intel.** UTM on Apple Silicon
   defaults to the AVF backend (native virtualization).
   On Intel Macs it uses QEMU (slower, but works). Acceptable
   for both — performance characteristics are user-visible but
   not mpd-blocking.
2. **`utmctl` API stability.** UTM 4.x+ ships `utmctl` as a
   first-class CLI; check current state at implementation time.
   If breakages have happened, `osascript`/AppleScript
   automation of UTM.app is a fallback (UTM ships an
   AppleScript dictionary).
3. **GPU passthrough.** Parallels' Direct3D shim doesn't apply;
   UTM's AVF backend can expose `virtio-gpu` to the guest, which
   gets you a Metal-backed GPU inside the Linux VM on Apple
   Silicon. Not required for mpd's normal workload, but a real
   differentiator vs Parallels if a future use case wants it.
4. **Snapshot story.** UTM supports snapshots via its GUI; `utmctl`
   coverage is limited. Lifecycle parity is enough for v1; snapshot
   verbs can come later.

## Acceptance

- `mpd-virt-utm create 158` end-to-end creates a Debian Trixie VM,
  runs the bootstrap chain, ends with `mpd --vm-setup` succeeding
  inside.
- WireGuard tunnel from Mac → VM works; `https://mpd.test/`
  resolves from Mac browser.
- Lifecycle verbs (`start`, `stop`, `delete`, `doctor`) operate
  on UTM-hosted VMs without regression in the Parallels backend.
- README updated to recommend UTM as the no-license-required entry
  point on macOS.
