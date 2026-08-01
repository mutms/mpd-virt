# Apple `container machine` as the macOS backend

macOS support currently costs a hypervisor. Parallels needs a paid Pro
licence; [`utm-backend.md`](utm-backend.md) exists to remove that
barrier, but the price of doing so is driving a GUI app over its
AppleScript dictionary — 551 lines of the least durable code in the
repo, against an interface Apple does not version.

Apple's `container` shipped a **container machine** mode at WWDC26 that
makes both unnecessary on Apple silicon.

Status: proposed. Nothing here is implemented. The §7 spike ran on
2026-08-01 and settled everything about running mpd inside a machine and
about addressing it. What remains is building the image and the backend.

---

## 1. Why this is possible now, and was not before

A regular `container run` boots `vminitd` — a small Swift init compiled
against musl — which execs the OCI entrypoint and offers no service
management. Had that been the only mode, this proposal would be dead on
arrival: mpd's whole in-VM design is systemd units, with dnsmasq and the
caddy frontdoor running on the VM itself at `10.163.<NNN>.1`.

A **container machine** is different. PID 1 is a thin wrapper that sets
the hostname, runs first-boot user setup, then execs **the image's own
`/sbin/init`**. systemd runs in full. Machines are persistent and
stateful — stop one, come back next week, the filesystem is as you left
it — and each gets its own address at the Mac host level.

That is a VM in every respect that matters to mpd, delivered from an OCI
image and with no hypervisor to license or drive.

**Nested containers are proven, not theoretical.** `kiac` runs
Kubernetes with `kindest/node` — systemd, containerd and kubeadm — inside
Apple containers. Each node gets its own kernel, its own cgroups and its
own IP, and the bundled kernel is monolithic with veth pairs, bridges
and iptables NAT compiled in. That is precisely the feature set podman
needs.

The framing that makes this obvious: an Apple container **is** a VM with
a real kernel, so podman inside it is podman-in-a-VM, not
podman-in-podman. The usual nesting tax — `fuse-overlayfs`,
`--privileged`, `/dev/fuse` — does not apply.

**None of the above is inference any more.** The §7 spike ran all of it
on real hardware and every point held, including native overlayfs and a
podman bridge on an `10.163.<NNN>.0/24` subnet. See the results table.

## 2. Decisions this proposal assumes

Taken 2026-08-01, and the reason the scope below is what it is:

* **Both hypervisor backends go.** UTM is retired before it is built —
  [`utm-backend.md`](utm-backend.md) and `Backend/UTM.swift` with it —
  and Parallels follows soon after this backend works. `container` and
  `general` are the only backends that survive. Existing Parallels VMs
  are not stranded: they become adopted `general` VMs at their existing
  ids and addresses.
* **macOS 26 is the only supported host.** Nothing older is supported at
  all, which lets `Package.swift` raise its floor from `.macOS(.v14)`
  and drops availability guards everywhere.
* **Macs that run 26 but are not Apple silicon** use `general` takeover,
  the same path Proxmox takes. Apple's runtime requires Apple silicon;
  everything else about mpd-virt does not.
* **Routing stays the default access model.** An SSH SOCKS proxy would
  make host routing optional and is the obvious follow-on, but it is a
  separate project and nothing here depends on it.
* **Native containers are a macOS-only backend.** `mpd-virt-linux` and
  `mpd-virt-windows` get no equivalent; those hosts reach mpd by
  takeover. This is why the image lives in this repo — see §4.

The last decision is the consequential one. Keeping routing means the
machine needs a *deterministic* address, and Apple's tool has no `--ip`.

## 3. Addressing

**An earlier draft of this section proposed a per-octet MAC address.
That does not work and has been removed.** `--network
<name>[,mac=…][,mtu=…]` is a flag on `container run`. `container machine
create` has no `--network` and no MAC option at all — its flags are
`-n/--name`, `--cpus`, `--memory`, `--home-mount`, `--kernel`,
`--virtualization`, `--no-boot`, `--set-default`, plus image and registry
options. A machine lands on the default network and takes what it is
given.

So the subnet is choosable but only **globally**:

```sh
container system property set network.subnet 10.212.57.0/24
```

or the equivalent `[network] subnet` in `~/.config/container/config.toml`.
Allocation within it is sequential — the spike's first machine got
`192.168.64.2` on the stock default network.

There is no `--ip`, and the reason is worth recording: an IP reservation
belongs to the DHCP server, which here is macOS's `bootpd` behind
`vmnet`, not `container`. There is nothing for the tool to expose without
reaching into system configuration it does not own.

**Settled 2026-08-01 in favour of option 1.** `ip addr add
192.168.64.165/24` inside a machine whose lease was `192.168.64.4`, then
`ping` from the Mac — it answered. vmnet does not filter on the address
it handed out, so a guest may hold one it was never leased. Collisions
are structurally avoidable for the same reason they are under Parallels:
vmnet allocates upward from `.2`, the native-container block is 160-191,
and reaching it would take ~160 simultaneous machines.

The two candidates, for the record:

1. **Pin guest-side.** `bootstrap/30-networking.sh` already pins
   `<canonicalSubnet>.<NNN>` with systemd-networkd for every other
   backend. If vmnet tolerates a guest holding an address outside its
   DHCP pool, nothing changes and the octet keeps deriving everything.
   This is the Parallels arrangement exactly.
2. **Accept the assigned address.** Read it from `container machine
   inspect` and store it, which is what `Provisioned.ip` → `MPD_VM_IP` →
   `locate(ipHint:)` already do; `general` works this way today. `locate`
   would re-read on every verb so a changed address self-heals the route
   instead of silently breaking it.

Option 1 keeps the model uniform across backends, and is what the test
above confirms. Option 2 always works but makes the octet no longer
determine the address, losing a property the rest of the design leans
on — it stays recorded only as the fallback if the pin is ever found not
to survive a reboot.

**Mechanics.** The pin must be a systemd-networkd config file, not `ip
addr add`, or it does not survive a restart —
`bootstrap/30-networking.sh` already writes exactly that. The flow is
therefore DHCP-then-pin, the same shape Parallels uses today:
`container machine inspect` yields the leased address, SSH to it, write
`05-mpd.network`, restart networkd, and the machine moves to
`<canonicalSubnet>.<NNN>`. SSH drops mid-way, which `30-networking.sh`
already expects.

One thing still to check: whether Apple's machine wrapper writes its own
`.network` file, in which case ours must sort ahead of it. The `05-`
prefix `30-networking.sh` already uses exists for precisely this reason
on netplan systems, so the mechanism is in place either way.

**Last resort** if neither is satisfactory: a host-side `bootpd`
reservation (`/etc/bootptab`, subnet in `com.apple.vmnet.plist`). Both
are privileged files outside `container`'s control that `container system
start` may rewrite, so this is a fallback and not a plan.

## 4. The image, and the inversion it causes

This is the largest piece of new work, and it lives in **this repo** —
`mpd-virt-macos`, alongside the binary that builds it. There is no
`mpd-virt-linux` or `mpd-virt-windows` native-container backend planned;
those hosts reach mpd by takeover instead, so the image has exactly one
consumer and belongs beside it.

That holds even if a Windows native-container backend appears later.
Apple's runtime and Microsoft's differ enough — init model, kernel,
networking — that one image serving both is not a safe assumption; each
would likely need its own. So an image per host repo is the right
default rather than a consequence of there being only one today.

`container machine` accepts any OCI image containing `/sbin/init`. The
official Debian and Ubuntu images contain no init and are **rejected
outright**, so we build our own: Debian Trixie, systemd, and the runtime
stack. The spike's base image built in **18.7 seconds**.

### The image becomes the source of truth

Today `bootstrap/10`–`50` define what an mpd environment is, by
constructing one step by step inside a VM over SSH. That inverts: the
**Containerfile** becomes the definition, and everything else conforms to
it.

* `10-passwordless-sudo.sh` — gone. The machine's dev user already has
  passwordless sudo.
* `20-git-clone.sh`, `40-install-software.sh`, `50-build.sh` — absorbed
  into image build time, which is most of what makes VM creation slow
  today.
* `30-networking.sh` — survives, for hostname and the static pin.
* `mpd --vm-setup` — stays a post-create step.

**VMs conform to the image, not the other way round.** A VM being taken
over — Proxmox, a surviving Parallels VM, a Mac that is not Apple
silicon, anything reached through `general` — first runs a single tweak
script fetched from GitHub and
executed in one command. That step is **mandatory and happens before
takeover**. `mpd-virt setup` then only *verifies* the VM matches, rather
than building it.

### The takeover contract

Takeover takes **one argument, the IP**, and derives everything else:

| from | derived |
|---|---|
| last octet | the VM id `NNN` |
| which block `NNN` falls in | the class — general, native container, proxmox |
| the class | the expected address prefix |
| `NNN` | expected hostname `mpd-<NNN>`, zone, container subnet |

So `mpd-virt setup 10.212.56.200` says id 200, therefore proxmox,
therefore the prefix must be `10.212.56`, therefore the host must call
itself `mpd-200`. The id and the address cannot disagree, because one is
derived from the other. `--ip` stops existing; `validateOctet` and
`managedOctetRange` already do the range half.

Before anything is written, mpd-virt asserts: the prefix matches the
class, the hostname is `mpd-<NNN>`, and the VM conforms. **It refuses
rather than remediates.** Running the tweak script is the developer's
homework and must have happened first — which is the point, not a
limitation.

**This is a security control, not only tidiness.** `setup` pushes the
per-VM intermediate's *private key* into the VM. A mistargeted takeover
therefore hands a signing key to a machine you did not mean — one that
could then forge any name in that zone. The hostname assertion and SSH
key authentication are two independent confirmations that the box at
that address is the box you meant, catching a typo, a DHCP lease that
moved, or an address someone else answered for. The CA's name
constraints cap the blast radius to a single zone, but that is the last
line of defence, not the first.

Residual gap worth closing separately: on a first connection SSH accepts
an unknown host key, so the two confirmations above are the only ones
until a key is pinned. Whatever writes the managed `Host mpd-<NNN>` block
in `~/.ssh/config` is the natural place to record it.

**One deliberate asymmetry.** A general VM's prefix comes from whatever
IP is typed, so two of them may sit on different `/24`s — which means
their routes and resolver files cannot be pre-written, and general keeps
a per-VM privileged step. Container and proxmox have fixed prefixes, so
their whole block is written once. The cost lands on the escape-hatch
class, which is where it belongs.

Two consequences worth planning for:

* `mpd-virt` loses most of `Bootstrap/`. `Bootstrap/RunInVM.swift` is 304
  lines of driving a script chain over SSH; with a conformant VM,
  `setup` collapses to verify → push the per-VM CA → write the registry
  entry → `mpd --vm-setup`.
* Verification needs a definition of "correct", so mpd gains a
  `--vm-verify` verb. Keeping that in the mpd binary matches the existing
  split — the README already says of `update` that the flow "is mpd's
  contract, not mpd-virt's".
* The tweak script must install what the Containerfile installs, or
  takeover verification fails on VMs that were set up correctly. They
  change together; a change to one is a change to the other.

### What the image must carry

Three things the spike established:

* **`systemd-sysv`** — it supplies `/sbin/init` as a symlink to systemd.
  Without it the image is rejected exactly as stock Debian is.
* **`systemctl mask systemd-modules-load.service`** — the kata kernel is
  monolithic with no loadable modules, so this unit always fails and
  leaves the machine `degraded`. Masking it is cosmetic but makes
  `is-system-running` a usable health check.
* **`sshd`** — see §5. `mpd-virt` drives the machine over SSH, not
  `container machine run`. Apple already mounts an agent socket at
  `/var/host-services/ssh-auth.sock`.

First boot provisions the user account. The default script can be
overridden with an executable `/etc/machine/create-user.sh`, which
receives `CONTAINER_UID`, `CONTAINER_USER`, `CONTAINER_HOME`,
`CONTAINER_GID` and `CONTAINER_MACHINE_ID`.

Note the machine's user comes out as `uid=501(skodak) gid=20(dialout)` —
the UID mirrors the Mac account, but GID 20 is `staff` on macOS and
`dialout` on Debian. This does **not** matter: mpd mounts no host files,
so there is no ownership to align. The UID/GID-matching requirement in
older docs dates from Podman Desktop and is obsolete.

**Note for later, not for this proposal.** Machines mount the Mac's
`$HOME` at `/Users/<username>` over VirtioFS by default, and
`--home-mount none` turns it off. mpd keeps project data in a podman
volume inside the VM and should pass `none`, so no Mac filesystem is
exposed. Editing on the Mac and building inside is a genuinely better
workflow and `container volume` exists to support it, but that is a
change to mpd's storage model and should be decided on its own merits.

### Build mechanics

The Containerfile still needs mpd's source, to build the `mpd` binary —
it clones it during the build exactly as `20-git-clone.sh` does inside
the VM today, so the build context stays near-empty. A local checkout
should be usable instead, for developing mpd itself.

**Build once, not once per VM.** Tag the result and reuse it; rebuild
only on an explicit `--rebuild`, or when the recorded mpd revision
differs from the one the image was built from. Otherwise creating three
machines builds the same image three times.

**`container system start` prompts.** On a fresh install it asks:

```
No default kernel configured.
Install the recommended default kernel from [https://github.com/kata-containers/…]? [Y/n]:
```

`create --yes` is meant to be scriptable end to end, so it must either
check `container system status` and refuse with instructions, or run
`container system start` itself and handle the prompt. Inheriting a Y/n
prompt inside a scriptable verb is how a CI run hangs.

## 5. The backend

`MpdVirt.Backend` in [`Backend/Backend.swift`](../../mpd-virt/Backend/Backend.swift)
is already the kind tag, the capability source and the dispatcher in one
enum, so this is additive: `case container`, one arm per `switch`, plus
`compiledIn`.

A new `Backend/Container.swift`, shaped like `Backend/Parallels.swift`:

| member | implementation |
|---|---|
| `canonicalSubnet` | from the §3 decision |
| `capabilities` | `create: true, clone: false, lifecycle: true` |
| `create(octet:opts:)` | `container machine create` → `Provisioned(ip:uuid:)` |
| `locate` / `describe` | parse `container machine inspect` |
| `start` / `stop` / `delete` | `container machine stop \| rm` |
| `preflight` | refuse on name collision, as Parallels does |

Clone is deliberately absent. It exists for Parallels because building a
good template by hand is expensive; creating from an image is not, so a
clone verb would only add a second way to do the same thing.

**Drive the machine over SSH, not `container machine run`.** The spike
found that commands launched through `container machine run` cannot write
`/proc/sys` — `sysctl -w net.ipv4.ip_forward=1` returns `EACCES`, and
netavark fails with `Read-only file system` when building a bridge. The
same command spawned by PID 1 via `systemd-run` succeeds. It is not
capabilities (`CapBnd` is `000001ffffffffff` for both the shell and
PID 1), not user namespace (`uid_map` is `0 0 4294967295`), not network
namespace (both `net:[4026531833]`), and not seccomp (`Seccomp: 0`); the
mechanism was not identified.

This costs nothing. mpd runs everything as systemd units, and `mpd-virt`
already drives Parallels, UTM and general over SSH — so the container
backend gets *less* special, not more. It is the reason `sshd` is in the
image.

### Drive the CLI, not the Swift package

`mpd-virt` is a Swift project and Apple ships
[`apple/containerization`](https://github.com/apple/containerization) as
a Swift package, so this will be raised again. Shell out to the
`container` CLI anyway, for two reasons that are about layering rather
than taste.

**The package has no machine layer.** Its modules are `Containerization`,
`ContainerizationOCI`, `ContainerizationEXT4`, `ContainerizationNetlink`,
`ContainerizationOS`, `ContainerizationIO`, `ContainerizationArchive`,
`CloudHypervisor`, `cctl` — primitives for running a container inside a
VM. `container machine`, which is the entire basis of this proposal,
lives in `apple/container`'s CLI and its `container-apiserver`. The
library does not expose it.

**VM lifetime is in-process.** The package spawns and manages VMs inside
the calling process. `mpd-virt` runs one verb and exits, so a VM it owned
would exit with it — and persistence across stop/start is the property
that makes a machine usable as an mpd VM at all. Getting it from the
library would mean writing a long-lived daemon, i.e. reimplementing
`container-apiserver`.

Secondary: the package is 0.x with source stability guaranteed only
within minor versions, while the CLI is at 1.2.0 with a documented
command surface. And every other backend in this repo wraps a CLI —
`Parallels.swift` drives `prlctl`, `UTM.swift` drives `osascript` — so
this is the established shape, with `BackendError.missingExecutable`
already there for the probe.

**Revisit if Apple exposes a machine-level API.** That is the condition
that would change the answer; nothing else here would.

`create` flags worth fixing in the backend rather than inheriting:

* `--memory` — **10G minimum**. 8G breaks PhpStorm inside the machine.
  The built-in default is half of system memory, which is 12G on a 24GB
  Mac but 8G on a 16GB one, so the default is wrong exactly where it
  hurts. Warn on small Macs rather than silently accepting.
* `--home-mount none` — see §4.
* `--kernel <path>` is per-machine, which is a better escape hatch than
  the global `container system kernel set` if the kata kernel ever falls
  short.

**Code impact from the backend itself: none.** `Verbs/Setup.swift`,
`Registry.swift`, `Net.swift` and `CA.swift` should not change to
accommodate it. If they do, logic has leaked into the backend that
belongs in the shared core — the same acceptance test
[`pluggable-backends-and-adopt.md`](pluggable-backends-and-adopt.md)
already sets, and the reason that proposal's ≤200 LOC bound is worth
keeping here.

`Bootstrap/` is the exception, and not because of this backend: the
inversion in §4 shrinks it for *every* backend at once. Treat that as a
separate change with its own review, landing before or after the backend
but not tangled with it.

`CloudInit.swift` is UTM-only and goes with it in §6.

## 6. Octet ranges, and retiring UTM

[`proxmox-backend-and-warp-dns.md`](proxmox-backend-and-warp-dns.md)
carves the id space by product. Carve it by **how the Mac reaches the
VM** instead — Parallels and UTM are indistinguishable from the host's
point of view, so splitting them was never buying anything:

| ids     | class                 | host reachability       |
|---------|-----------------------|-------------------------|
| 100-127 | free                  | —                       |
| 128-159 | general VMs (adopted) | per-VM on-link next hop |
| 160-191 | native containers     | vmnet, one next hop     |
| 192-223 | proxmox               | single gateway (warp)   |
| 224-255 | free                  | —                       |

Same CIDR-aligned blocks of 32, same grow-by-promoting-the-prefix
property. Only the labels change.

General VMs take the lower block so that existing hand-made VMs — `mpd-130`
among them — keep their ids and need no renumbering. Native containers
are the class with no installed base yet, so they are the ones that can
be assigned freely.

**The principle underneath all of this: everything is predictable before
mpd-virt arrives.** One number gives the name `mpd-<NNN>`, the address
`<prefix>.<NNN>`, the zone `<NNN>.mpd.test`, the container subnet
`10.163.<NNN>.0/24` with its gateway at `.1`, the resolver file and the
registry directory. Nothing is discovered, negotiated, or recorded
because it happened to turn out that way.

That is what makes verification cheap and total. `--vm-verify` does not
inspect a VM and work out what it is; it computes what the VM must look
like and checks. No discovery protocol, no reconciliation. It is the
reason `setup` can shrink from driving a script chain to
verify-push-register, and the reason to refuse any later "convenience"
that reintroduces discovery.

The three classes differ only in how much is predictable before mpd-virt
arrives — which is the same thing as who creates the VM, and whether the
host can compute where it is:

| | `create` | lifecycle | IP source | host network setup |
|---|---|---|---|---|
| **container** | fully automatic | yes | assigned or pinned (§3, open) | one next hop, per-class script |
| **proxmox** | Proxmox UI + tweak script | no | derived, `10.212.56.<NNN>` | 2 aggregate routes + 1 resolver file, written once |
| **general** | manual + tweak script | no | prefix from the IP given, host octet still `NNN` | per-VM route + per-VM resolver file |

Read down the `create` column and the product is visible: one backend
where the user types `create 165` and gets a working environment, and two
where they build the VM themselves, run the tweak script, and mpd-virt
verifies.

Proxmox sits in the middle deliberately. It takes over like `general` but
addresses like `container`, which is why it earns a `Backend` case of its
own rather than being `general` with documentation — see
[`proxmox-backend-and-warp-dns.md`](proxmox-backend-and-warp-dns.md) §4.

Once §5 passes its acceptance test, both hypervisor backends come out.
The removal is larger than it looks, because two pieces of the `Backend`
protocol exist solely to serve them:

* **`clone` loses every implementation.** Parallels is the only backend
  with `clone: true`; UTM, `general` and `container` are all `false`. So
  `Verbs/Clone.swift`, `CloneOpts`, the dispatcher arm and the README row
  all go with it.
* **`afterCanonicalIPReady` loses every caller.** Its own docstring says
  it exists for Parallels' VM-name/guest-hostname race. It comes out of
  the protocol.

Deleted outright: `Backend/Parallels.swift` (613 lines),
`Backend/UTM.swift` (551), `CloudInit.swift` (259), `Verbs/Clone.swift`
(50) — around 1,500 lines, before the separate `Bootstrap/` shrink in §4.

What remains is `container` and `general`, so mpd-virt becomes two flows:
*make me a container machine*, and *adopt this thing*. Every verb that is
not `create` is takeover or bookkeeping.

`General.canonicalSubnet` is `10.211.55`, chosen to be "Parallels-like so
the common adopt path Just Works". That value stays correct once
Parallels VMs become adopted general VMs at their existing addresses —
only its comment needs rewording. It is also why swapping the blocks
above was right: `mpd-130` keeps its id and its IP and merely changes
class.

`README.md` needs more surgery than the proposals do — the three-backend
table, and the whole "Building a Parallels template" section.

## 7. Spike results

Run 2026-08-01 on the bare-metal Mac — `container` CLI 1.2.0, macOS
26.5.2, M2, 24GB, kata-containers kernel 6.18.15. `container` cannot be
tested inside a macOS VM, so this needed real hardware.

| # | question | result |
|---|---|---|
| 1 | systemd as PID 1 from a Debian Trixie image | ✅ `degraded`, sole failure `systemd-modules-load.service` |
| 2 | `container machine create --network` | ❌ **does not exist** — see §3 |
| 3 | deterministic address | ✅ guest-side pin works — vmnet does not filter |
| 4 | podman inside | ✅ see below |
| 5 | Mac reaches `10.163.<NNN>.1` over a route | ⬜ not yet attempted |

Item 4 in detail, all confirmed: cgroups v2 with `cgroupManager:
systemd`; `graphDriverName: overlay`, native, with no `fuse-overlayfs`
anywhere; rootful podman 5.4.2 with crun and netavark; a `podman network
create --subnet 10.163.133.0/24` bridge; a container at `10.163.133.4`
with a real veth pair; and successful NAT egress to the internet. The
`Native Overlay Diff: false` line in `podman info` is not a fallback — it
is the consequence of `metacopy`, which Debian enables by default.

The one caveat is the `container machine run` privilege restriction
recorded in §5, which does not affect mpd.

**What is left.** Item 5, and the image in §4. Nothing else about running
mpd inside a machine, or about addressing it, is unknown.

Two incidental findings. `ip addr add` succeeded through `container
machine run` without needing `systemd-run`, so the privilege restriction
in §5 is narrowly about `/proc/sys` rather than network operations in
general. And the machine's uplink MTU is **1280**, while podman hands
containers 1500 — netavark's MSS clamping hides that for TCP, but it is
worth remembering if UDP or path-MTU-sensitive traffic misbehaves.
`mtu=` is settable on `container run --network`; there is no `--network`
on machine create, so it may not be adjustable here.

**Cost.** macOS 26 and Apple silicon only. Memory is per-machine, so
several machines at once needs care — see the `--memory` note in §5. The
M3 / `CONFIG_KVM=y` requirement in Apple's docs applies to *nested
virtualization*, VMs inside a machine, not to podman; an M2 is
sufficient, as the spike demonstrated.

## 8. What a new developer does

The flow this is all in service of, on a Mac with nothing installed:

1. Clone this repo into `~/Developer/mpd-virt-macos`.
2. Install the Xcode command-line tools.
3. `make install` — puts `mpd-virt` in `~/.local/bin`.
   `scripts/install-message.sh` already detects whether that is on PATH
   and prints shell-specific instructions, so nothing more is needed.
4. `mpd-virt create 165` — fails with instructions to install the
   `container` signed package from its GitHub release, and to run
   `container system start` (which prompts once for the kata kernel).
   This mirrors how `Backend/Parallels.swift` probes for `prlctl` and
   throws `BackendError.missingExecutable`.
5. `mpd-virt create 165` again — builds the image if it is not already
   built, creates the machine, runs the remaining setup over SSH.
6. Interactive `diag` asks for sudo once: the route, the
   `/etc/resolver/165.mpd.test` file, and CA trust in the System
   Keychain. This is the only privileged step, and it exists because
   routing is the access model — see §2.
7. `https://165.mpd.test/` loads.

Two things this flow depends on that are not automatic today. `container`
must be the compiled-in default backend on macOS, or step 4 needs
`--backend=container` — `Create.run` calls `resolveBackend(flag:)`, which
throws when no default is stored. And step 5's build caching is what
stops steps 5–7 being repeated slowly for the second and third machine.

## Open questions

* Does a pinned address survive a stop/start cycle, and does Apple's
  machine wrapper write its own `.network` file that ours must sort
  ahead of? The `05-` prefix in `30-networking.sh` handles the second
  case if so.
* What actually blocks `/proc/sys` writes under `container machine run`?
  Capabilities, both namespaces and seccomp were all ruled out. The
  mount-namespace comparison was never run and is the obvious next
  check. Only matters if something later needs that path.
* ~~Where does the image live?~~ **Settled.** For now: one Containerfile
  in this git checkout, built locally on first `create`. No registry —
  which also sidesteps Apple's poor handling of private registries with
  custom CAs ([#305](https://github.com/apple/container/issues/305),
  [#731](https://github.com/apple/container/issues/731)); `container
  image save`/`load` covers transfer if it is ever needed.

  Once things stabilise, this splits into three published images: a
  `base` (trixie + systemd), `mpd` on top of it, and a Moodle demo
  container alongside. `sshd` belongs in the `mpd` layer rather than the
  base — `mpd-virt` needs it, and a demo image shipped to strangers has
  no reason to run an SSH server.

  Write the Containerfile with that seam already visible: base-layer
  instructions in one contiguous block at the top, mpd-specific ones
  below, so the split is a cut rather than an untangling. And note a
  locally built image has no digest anyone else can pin, so the
  "which image is this VM from" check should key on the mpd revision it
  was built from, as the rebuild check in §4 already does.
* Does `container machine` restore cleanly enough for `start`/`stop` to
  mean what they mean for Parallels? Third-party reports mention TCP
  connections breaking after restarting VMs from the Mac.
* The stock default network is `192.168.64.0/24` — the same range
  `UTM.canonicalSubnet` claims. Harmless only because UTM is being
  retired; worth not reintroducing.

## Sources

* [apple/container](https://github.com/apple/container) —
  [how-to](https://github.com/apple/container/blob/main/docs/how-to.md),
  [command reference](https://github.com/apple/container/blob/main/docs/command-reference.md),
  [container machine](https://github.com/apple/container/blob/main/docs/container-machine.md)
* [kiac — Kubernetes in Apple Containers](https://blog.kubesimplify.com/introducing-kiac-kubernetes-in-apple-containers)
* [Apple Container Machine, tested](https://www.statuspal.io/blog/apple-container-machine-persistent-linux-vm-macos)
