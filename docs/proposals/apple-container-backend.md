# Apple `container` (run mode) as the macOS backend

macOS support currently costs a hypervisor: Parallels needs a paid Pro licence,
and UTM means driving a GUI over its AppleScript dictionary. Apple's `container`
tool removes both — a Linux box per `container run`, on a real kernel, with no
hypervisor to license or drive.

This proposal replaces an earlier draft that targeted `container machine`. That
direction is **dropped**: machine mode mounts the Mac's `$HOME`, and a
default-expose posture is the wrong foundation for a sandbox that runs untrusted
Moodle/plugin code. We use **`container run`** — default-deny isolation — for
everything.

Status: proposed. The feasibility spike ran 2026-08-01 and is written up in
[`../internal/apple-containers.md`](../internal/apple-containers.md); this
proposal builds on it. What remains is the Go binary and the image.

---

## 1. Decisions (2026-08-01)

1. **`container run` only.** No `container machine`, no second mode. Supporting
   two runtimes is wasted effort, and `container run` is the one that's secure
   by default (§2).
2. **A new Go binary.** `mpd-virt` is rewritten from Swift to Go. The existing
   Swift codebase (`Backend/*`, `Verbs/*`, `Bootstrap/*`, `CloudInit.swift`) is
   retired. The whole stack becomes Go — `mpd` already is.
3. **Takeover first.** The first milestone is adopting an *existing*,
   hand-created `container run` box — exactly the one validated in the spike.
4. **Create second.** Having `mpd-virt` create the container itself
   (`container run …` under the hood) is the next milestone, built on the
   takeover path.
5. **The host IP does not derive from the VM id.** vmnet assigns the container's
   address; it will not be `<prefix>.<NNN>`. That's fine — `container inspect`
   reports the address of an existing container, so we read it rather than
   compute it (§4).

## 2. Why `container run`, and not `container machine`

The two modes are opposites on the axis that matters here — the **default**:

| | `container run` (vminitd) | `container machine` (systemd) |
|---|---|---|
| Host `$HOME` | **not** mounted (opt-in via `-v`/`--mount`) | mounted **rw** over VirtioFS by default |
| IP | own vmnet lease | DHCP, assigned |
| Identity | hostname from `--name`, users from image | hostname = machine name, login as host UID |
| Init | `vminitd` execs the entrypoint | image's `/sbin/init` → systemd |
| Default posture | **deny** — isolated until you open it | **expose** — porous until you close it |

Both were verified in the spike. `container machine` genuinely mounted
`/Users/skodak` read-write into the guest (`virtiofs … rw`). It *can* be locked
down (`--home-mount none`), but for a component whose entire job is running
untrusted code, isolation must be **the default you cannot forget**, not a flag
you must remember — and machine mode's other defaults (host UID, forced
identity) stay porous regardless. `container run` starts sealed; a clean box
shows only `/dev/vdb`, no host paths. That decides it.

The cost is no in-guest systemd. It is not a real cost: `mpd-virt` drives the
box over SSH (as it does every other backend), and Podman does not need systemd
as PID 1.

## 3. Podman inside a `container run` box — proven

Full detail in [`../internal/apple-containers.md`](../internal/apple-containers.md).
The headline: **rootful Podman runs** — netavark bridge, veth, NAT egress to
the internet — inside a `container run` box, once three things are set:

1. **Capabilities.** Default is the OCI 14-cap set (no `NET_ADMIN`/`SYS_ADMIN`);
   `container run --cap-add …` grants more. Rootful Podman needs at least
   `NET_ADMIN` + `SYS_ADMIN` on top of the default — **narrowing the exact set
   down from `--cap-add ALL` is an open task.**
2. **`/proc/sys` writable.** It mounts `ro`; netavark writes `ip_forward`.
   `mount -o remount,rw /proc/sys` in-guest (needs `SYS_ADMIN`) fixes it.
3. **cgroups.** `cpuset/cpu/pids` delegate to children; `io/memory` do **not**
   (Apple's runtime won't add them to `subtree_control`). Nested containers run
   but get no io/memory limits — a limitation to record, not a blocker.

An Apple container is a VM with a real kernel, so Podman inside is
podman-in-a-VM, not podman-in-podman: native overlayfs, no `fuse-overlayfs`, no
`/dev/fuse`. Rootless is close but not yet working (a per-namespace `/proc/sys`
wall); **rootful is the supported path.**

## 4. Addressing — the id derives everything except the host IP

The old proposal's elegance was "one number determines everything, including
the address `<prefix>.<NNN>`." Under vmnet that half is false and cannot be
made true: the guest cannot hold a routable address it wasn't leased (confirmed
even with `CAP_NET_ADMIN` — a guest-set `.128` was never routed; flushing the
lease just stranded the box). **So we do not pin an IP guest-side on macOS.**

Everything else still derives from the id `NNN`:

| from `NNN` | derived |
|---|---|
| the number | VM id, hostname `mpd-<NNN>` (set via `--name`) |
| the class (which block `NNN` falls in) | general / native-container / proxmox |
| `NNN` | zone `<NNN>.mpd.test`, in-container podman subnet `10.163.<NNN>.0/24` (gw `.1`), resolver file, registry dir |

The **host-reachable IP** is the one exception: vmnet assigns it, so takeover
**always takes it as an explicit argument** — `mpd-virt takeover <NNN> <IP>` for
every class. You find the address with `container ls` / `container inspect
mpd-<NNN>` and pass it; `mpd-virt` never auto-discovers the target. An explicit
address keeps the adopt target unambiguous, which the security model (§9)
depends on. The registry records it. For the container class, a lease that moved
across a restart can be refreshed from `container inspect` on lifecycle verbs,
but the address is never guessed at takeover time.

DNS ties it together: `<NNN>.mpd.test` and `mpd-<NNN>` resolve to the
looked-up IP, so nothing user-facing depends on the vmnet octet.

## 5. Octet ranges and classes

Carve the id space by **how the Mac reaches the box**:

| ids     | class                 | host reachability          |
|---------|-----------------------|----------------------------|
| 100-127 | free                  | —                          |
| 128-159 | general VMs (adopted) | per-VM, IP supplied         |
| 160-191 | native containers     | vmnet lease, IP read back  |
| 192-223 | proxmox               | single gateway (warp)      |
| 224-255 | free                  | —                          |

CIDR-aligned blocks of 32. General VMs take the lower block so existing
hand-made VMs keep their ids. Native containers are the class with no installed
base, free to assign. For native containers the id still fixes the *identity*
(name, zone, internal subnet); only the host IP floats.

The principle survives, narrowed: **everything is predictable from `NNN` before
`mpd-virt` arrives — except the host IP, which the runtime alone decides and you
supply to takeover.** Verification stays cheap: `mpd-virt` computes what the box
must be (name, zone, conformant image) and checks it at the address you gave.

## 6. The base image

The [`containers/takeovertest`](../../containers/takeovertest) recipe — Debian
Trixie, `vminitd`, sshd as the entrypoint, `skodak` + passwordless sudo + the
authorized key, **no systemd** — is the **base for mpd native containers on
macOS**. The real mpd image is this base plus the runtime stack.

`container run` accepts any OCI image; there is no `/sbin/init` requirement (that
was a machine-mode constraint). What the image must carry:

* **sshd** — `mpd-virt` drives the box over SSH, and the entrypoint runs
  `sshd -D` as PID 1. Host keys are generated on first boot, not baked.
* **Podman + rootless bits** (`podman`, `passt`, `slirp4netns`, `uidmap`) and
  whatever mpd's compute needs.
* **The `mpd` binary**, built at image-build time (cloned from the mpd repo, as
  the old `20`/`40`/`50` bootstrap steps did inside a VM — absorbed into the
  build).
* **No network config.** The entrypoint must never touch `eth0` (§4): pinning
  strands the box. It sets `/etc/hosts` from the runtime hostname and starts
  sshd, nothing more.

### The image is the source of truth

The Containerfile *is* the definition of an mpd environment. Boxes conform to
the image, not the other way round:

* A **native container** is built from the image directly — conformance is by
  construction.
* An **adopted general VM** first runs a one-shot tweak script (fetched from
  GitHub, run in one command) that installs what the image installs. That step
  is mandatory and happens **before** takeover. `mpd-virt` then only *verifies*.

Build once and reuse (tag it); rebuild only on `--rebuild` or when the recorded
mpd revision differs from the image's. The Podman-enablement steps from §3
(`--cap-add`, `/proc/sys` remount, tun perms) are applied at run/first-boot, not
baked into the image layers.

## 7. Takeover first, create second

### Phase 1 — takeover (the milestone we're at)

Adopt an existing `container run` box that was created by hand:

```sh
# host, by hand for now:
container run -d --name mpd-160 --cap-add ALL <mpd-image>
container ls                          # note the IP, e.g. 192.168.64.17
mpd-virt takeover 160 192.168.64.17
```

`takeover 160 <IP>` asserts the box *at that IP* conforms (hostname `mpd-160`,
SSH key auth works, image matches), pushes the per-VM CA intermediate, writes
the registry entry and the `~/.ssh/config` block, and runs `mpd --vm-setup`.
**It refuses rather than remediates** (§9).

Takeover always takes `<NNN> <IP>`. The class only changes what the supplied IP
is checked against and what host-side setup is written:

| class | takeover | class fixes |
|---|---|---|
| native container | `takeover <NNN> <IP>` | IP is vmnet-assigned (find via `container ls`); no prefix constraint |
| general VM | `takeover <NNN> <IP>` | IP is wherever the VM lives; per-VM route + resolver |
| proxmox | `takeover <NNN> <IP>` | IP must be `10.212.56.<NNN>`; single gateway |

### Phase 2 — create

`mpd-virt create 160` does the `container run` itself: builds the image if
needed, runs it with the right `--name` and cap set, waits for sshd, then runs
the adopt path against it. It needs no IP argument — having made the box, it
asks the runtime (`container inspect mpd-160`) for the address. One command from
nothing to a working box.

`create` differs by class in exactly *how it gets the address* — the mirror of
why takeover needs an explicit IP:

* **native container** — run, then **read** the vmnet-assigned IP from
  `container inspect` (the runtime chose it; it won't match `NNN`).
* **proxmox** (when implemented) — create the VM via the Proxmox API and
  **assign** the static `10.212.56.<NNN>`, octet matching, because there we
  control the network and static pinning is correct.
* **general** — no `create`; arbitrary hand-built VMs are adopt-only.

## 8. The Go binary

`mpd-virt` is rewritten in Go. Rationale: `mpd` is already Go, so the whole
stack unifies on one language and toolchain; and every backend just wraps a CLI
(`container`, `ssh`) plus SSH-driven scripts — none of which needed Swift. The
Go binary owns:

* **The registry** — one entry per box (`id`, `name`, `ip`, `user`, class),
  re-reading the IP from `container inspect` on every verb.
* **The CA** — root stays on the Mac; a per-VM name-constrained intermediate is
  generated and its private key pushed into the box (§9). Unchanged in intent
  from the Swift design.
* **The `container` backend** — wraps `container run|create|start|stop|rm|
  inspect|ls|build`. No `container machine` anywhere.
* **The general backend** — adopt a box at a supplied IP over SSH.
* **SSH driving** — key-auth, `scp` the CA material, run `mpd --vm-setup` and
  the verify checks.

Verb surface (what falls out of §4–§7):

```
mpd-virt create 160            # native container: run, then adopt (IP self-read after run)
mpd-virt takeover 160 <IP>     # adopt an existing box at <IP> — any class
mpd-virt start|stop|delete 160
mpd-virt list | diag 160 | update 160 | uninstall
```

**Takeover always takes `<NNN> <IP>`** — the address is a positional argument
for every class, never guessed (§4, §9). `create` is the exception that needs no
IP: on macOS it reads the address from `container inspect` after running the
box; on Proxmox (future) it assigns the matching static `10.212.56.<NNN>`
itself. Gone, and why: `--backend` (the id's block determines the
class); `clone`/`--template` (Parallels-only, retired); `--vm-disk` (containers
are thin-provisioned against the Mac's disk); `--username` (defaults to
`whoami`).

**Design constraint:** the shared core (registry, CA, verb dispatch,
verification) must not grow backend-specific logic. A backend is a thin wrapper
over `container`/SSH; if core code starts branching on class beyond the
class→prefix/IP-source table, the abstraction has leaked.

The exact Go package layout is left to implementation. The one hard rule: shell
out to the `container` **CLI** (documented, stable 1.x surface), not the
`apple/containerization` Swift package — which has no equivalent for Go, manages
VM lifetime in-process (wrong for a run-and-exit tool), and is 0.x. Every
backend wrapping a CLI is the established, correct shape.

## 9. Security model

Unchanged in intent from the Swift design, and the reason takeover *verifies*
rather than *remediates*:

* The root CA's private key **never leaves the Mac**. Takeover pushes a **per-VM
  intermediate's private key** into the box, **name-constrained** to that box's
  zone (`<NNN>.mpd.test`). Rooting one box therefore buys forging names only in
  a zone the attacker already reached — the blast radius is capped at the CA
  layer, as a last line of defence.
* A **mistargeted takeover hands a signing key to the wrong box.** This is why
  the address is always **supplied, never discovered** (§4): you point takeover
  at an explicit IP, and before writing anything `mpd-virt` asserts two
  independent confirmations that the box *there* is the intended one — the
  **hostname is `mpd-<NNN>`**, and **SSH key auth succeeds**. Either catches a
  typo, a moved lease, or an address someone else answered for. It **refuses**
  on mismatch. Auto-discovering the address would remove the very act — naming
  the target — that these checks confirm.
* Running the conformance tweak script (general VMs) is the developer's homework
  and must have happened first. That is the point, not a limitation.

Residual gap to close separately: first-connection SSH trust-on-first-use.
Whatever writes the `Host mpd-<NNN>` block in `~/.ssh/config` is the natural
place to pin the host key — and re-reading the IP on every verb (§4) means the
pin must key on the name, not the address.

The isolation posture from §2 backs this up at the runtime layer: the box mounts
nothing from macOS, so even a fully compromised container reaches no host files
— only the network surface, which `container run` scopes to its own vmnet
address.

## 10. Roadmap and open questions

**Roadmap:** (1) takeover of a hand-created container — current milestone; (2)
`create`; (3) retire the Swift codebase as the Go binary reaches parity; (4)
proxmox as its own class (custom network, static IPs — where pinning *is*
correct).

**Open questions:**

* **Minimal cap set** for rootful Podman — narrow from `--cap-add ALL` to the
  smallest working set (likely default + `NET_ADMIN` + `SYS_ADMIN`). Immediate
  next task.
* **Where the runtime tweaks live** — `/proc/sys` remount, tun perms,
  cap-add: baked into `create`/first-boot, or owned by mpd's own setup? They
  reduce isolation marginally and should be deliberate.
* **cgroup io/memory limits** — confirm whether Apple's runtime can be made to
  delegate these, or accept their absence on nested containers.
* **Rootless Podman** — resolve the per-namespace `/proc/sys` `ping_group_range`
  wall, or commit to rootful-only.
* **Restart semantics** — a stopped `container run` box restarts with
  `container start`, but its vmnet IP may change; §4's re-read handles the
  route, but confirm the filesystem and Podman state survive stop/start well
  enough to call it persistence.
* **The default vmnet subnet is `192.168.64.0/24`** — the native-container
  boxes live here regardless of their `160-191` id, since the id no longer
  drives the host IP. Ensure nothing else assumes `<prefix>.<NNN>` for this
  class.

## Sources

* [apple/container](https://github.com/apple/container) —
  [command reference](https://github.com/apple/container/blob/main/docs/command-reference.md),
  [how-to](https://github.com/apple/container/blob/main/docs/how-to.md)
* [`../internal/apple-containers.md`](../internal/apple-containers.md) — the
  2026-08-01 `container run` + Podman spike this proposal is built on.
