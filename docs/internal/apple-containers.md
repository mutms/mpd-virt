# Apple `container` — running Podman inside a `container run` box

Empirical findings from a hands-on session on **2026-08-01**.

> **Decision (2026-08-01): mpd's macOS native-container backend uses
> `container run`, NOT `container machine`.** `container machine` is not used
> for anything. The isolated `container run` box — the `mpd-128` box built from
> [`containers/apple`](../../containers/apple) — is the **base for
> mpd native containers on macOS**. This supersedes the machine-mode direction
> in the [backend proposal](../proposals/apple-container-backend.md), which
> predates these findings.

The question this session answered: **can mpd's Podman workloads run inside a
`container run` box?** Short answer: **rootful Podman works, with `--cap-add`
plus two runtime tweaks.**

Why `container run` and not `container machine`: `container run` keeps the box
**isolated** — no host `$HOME` mount, its own IP, its own identity.
`container machine` gives all of that up (mounts the Mac's `$HOME` over
VirtioFS, forces a DHCP IP, logs you in as the host UID), which is why it is
rejected outright.

All commands below were run over SSH into a `container run` box built from
[`containers/apple`](../../containers/apple) (Debian Trixie,
`vminitd` as PID 1, sshd as the entrypoint — no systemd).

## Environment

- Host: the same bare-metal Mac as the proposal's §7 spike — M2, macOS 26.5,
  `container` CLI 1.2.x.
- Guest kernel: **kata-containers 6.18.15** (confirmed via `/proc/cmdline`:
  `init=/sbin/vminitd ro rootfstype=ext4 root=/dev/vda`, `lsm=lockdown,
  capability,landlock,yama,apparmor`).
- Podman **5.4.2** (Debian Trixie package), crun + netavark + aardvark-dns.
- `container run` mode throughout — **not** `container machine`.

## `container run` vs `container machine`

| | `container run` (vminitd) | `container machine` (systemd) |
|---|---|---|
| PID 1 | `vminitd` — execs entrypoint, no service mgmt | image's `/sbin/init` → systemd |
| Host `$HOME` | **not** mounted | mounted rw over VirtioFS |
| IP | own vmnet lease | DHCP, assigned |
| Identity | hostname from `--name`, users from image | hostname = machine name, login as host UID |
| Isolation | strong | deliberately porous (dev-env model) |

For a takeover target we want isolation, so `container run` + a lightweight
entrypoint (set `/etc/hosts`, run `sshd`). systemd is unavailable in this mode
— acceptable because mpd is driven over SSH, and Podman does not require systemd
as PID 1 (it can use the `cgroupfs` manager).

## Capability model — the core finding

By default the container runs with the **standard OCI 14-capability set**, even
for root:

```
CapEff/CapBnd: 00000000a80425fb    # root, default `container run`
```

Present: `CHOWN DAC_OVERRIDE FOWNER FSETID KILL SETGID SETUID SETPCAP
NET_BIND_SERVICE NET_RAW SYS_CHROOT MKNOD AUDIT_WRITE SETFCAP`.
**Missing (relevant): `NET_ADMIN`, `SYS_ADMIN`.**

Consequence: with the default set, **the guest cannot touch networking at all**
— even root via sudo:

```
sudo ip addr add 192.168.64.200/24 dev eth0
→ RTNETLINK answers: Operation not permitted
```

`container run --cap-add <cap>` (from the full `--help`) fixes this. There is
also `--cap-drop`. With `--cap-add ALL`:

```
CapEff/CapBnd: 000001ffffffffff    # full
sudo ip addr add … dev eth0        → OK   (NET_ADMIN)
sudo mount -t tmpfs none /mnt      → OK   (SYS_ADMIN)
```

**This was the unlock.** An earlier pass concluded "Podman can't run here"
because the capability flags were missed from a filtered `--help`. They exist.

## Networking / addressing

- `eth0` is configured **from outside the guest by `vminitd`** at boot: a
  static `valid_lft forever` address (no DHCP client runs inside; `ps` shows
  only sshd). DNS = the gateway (`.1`). Uplink **MTU 1280**.
- Default set has no `NET_ADMIN`, so the address is immutable from inside.
- **vmnet is lease-locked.** Even with `--cap-add ALL` (so the guest *can*
  reconfigure `eth0`), a guest-chosen address is **not routed**. Test: the
  entrypoint flushed the lease `.16` and set `.128`; afterwards **both `.16`
  and `.128` were unreachable** — the box stranded itself. vmnet only delivers
  to the address it leased.
- **Therefore: never pin an IP guest-side on macOS.** Take the vmnet-assigned
  address as-is. (This is the opposite of Proxmox, where mpd controls DHCP and
  static pins are correct.) mpd's identity for these boxes rides on the
  hostname (`--name mpd-<NNN>`) + `<NNN>.mpd.test`, and the IP is supplied
  explicitly to takeover: `mpd-virt takeover <NNN> <IP>`.

> Note vs. the machine-mode spike: the proposal §3 reports a guest-side pin
> *worked* there by **adding** a secondary address (keeping the lease). This
> session only tested **flush-then-replace** in `run` mode, which strands the
> box. "Add alongside" was not tested in `run` mode. Moot in practice — the
> decision is to not pin on macOS in either mode.

- Hostname: `vminitd` sets it to `--name`. `--name mpd-128` → `hostname`
  returns `mpd-128`. The guest cannot `sethostname` without `SYS_ADMIN`, so
  `--name` is the mechanism. There is **no `--hostname` flag**.

## Podman inside — what it takes

Prerequisites, all present in the box:

- unprivileged userns allowed (`max_user_namespaces: 4505`), `unshare -Ur --net`
  works, `uid_map` is the full `0 0 4294967295` (init userns).
- `/dev/net/tun` exists (mode `0600 root`).
- `subuid`/`subgid` configured (`skodak:100000:65536`).

### Default caps → Podman fails (all modes)

| mode | failure |
|---|---|
| rootful | no `NET_ADMIN` → bridge/netavark can't set up |
| rootless, multi-ID | `newuidmap` → `write to uid_map failed: Operation not permitted` — reproducible bare, in the init userns, with `CAP_SETUID` present |
| rootless, single-ID | image unpack fails: `lchown /etc/shadow: invalid argument` (needs gid 42, only one ID mapped) |

The `newuidmap` denial is notable: full range, `CAP_SETUID`, `NoNewPrivs=0`,
setuid binary intact, suid rootfs — and the kernel *still* refused the extended
`uid_map` write under the default cap set.

### `--cap-add ALL` → rootful Podman works

The `newuidmap`/`uid_map` denial **disappears** with full caps. Remaining walls
and their fixes:

| wall | fix | notes |
|---|---|---|
| `netavark: Sysctl error: Read-only file system` | `sudo mount -o remount,rw /proc/sys` | `/proc/sys` is mounted `ro`; netavark writes `ip_forward` etc. Needs `SYS_ADMIN`. |
| `crun: controller 'pids'/'io' not available … cgroup.subtree_control` | non-fatal, or `--cgroups=disabled` | see cgroups below |

After remounting `/proc/sys`:

```
sudo podman run --rm docker.io/library/alpine \
  sh -c 'wget -qO- -T5 http://1.1.1.1 && echo OK'
→ IN
→ ROOTFUL-EGRESS-OK        # container ran, bridge networking, NAT egress to internet
```

A cgroup **warning** is printed (io controller) but the container runs.
`--cgroups=disabled` runs cleanly with no warning. **Rootful Podman + netavark
bridge + egress is confirmed working.**

### Rootless — closer, not there yet

With full caps, rootless gets much further: `uid_map` succeeds; `pasta` then
fails `open() /dev/net/tun: Permission denied` (it runs as `skodak`, tun is
`0600 root`). `sudo chmod 0666 /dev/net/tun` fixes that. Next wall:

```
crun: open `/proc/sys/net/ipv4/ping_group_range`: Read-only file system
```

— the rootless container has its **own** mount namespace where `/proc/sys` is
still `ro` (the earlier remount only affected the main namespace). Not resolved
this session. **Rootful is the viable path**; rootless needs more work (per-ns
`/proc/sys` handling or disabling the `ping_group_range` default).

## cgroups (v2)

```
controllers:      cpuset cpu io memory hugetlb pids
subtree_control:  cpuset cpu pids
```

`cpuset/cpu/pids` are delegated to child cgroups; **`io`/`memory`/`hugetlb` are
not**, and adding them fails:

```
echo '+io +memory' > /sys/fs/cgroup/cgroup.subtree_control
→ Operation not supported
```

So nested containers **run** but **cannot get io/memory resource limits**. For
mpd this is a limitation to note, not a blocker (or use `--cgroups=disabled`).

## Enablement checklist (rootful Podman in a `container run` box)

1. `container run --cap-add ALL …` (or the minimal set — **next step**, see below).
2. In-guest, once: `mount -o remount,rw /proc/sys` (needs `SYS_ADMIN`).
3. Rootless only: `chmod 0666 /dev/net/tun`.
4. Accept the cgroup io/memory limitation, or `podman run --cgroups=disabled`.

## Open questions / next steps

- **Minimal cap set.** `--cap-add ALL` is effectively privileged. Rootful
  Podman likely needs only the default set + `NET_ADMIN` + `SYS_ADMIN` (maybe
  a couple more). Determining the minimum is the immediate next task.
- **`/proc/sys` remount** — is doing it in the entrypoint acceptable, or should
  mpd's own setup own it? It reduces isolation slightly.
- **Rootless** — resolve the per-namespace `/proc/sys` `ping_group_range` wall,
  or decide rootful-only.
- **cgroup io/memory** — confirm whether Apple's runtime can be made to
  delegate these, or accept no io/memory limits on nested containers.
- **Cross-ref the machine-mode `/proc/sys` puzzle.** Proposal §5 records an
  unexplained `/proc/sys` write block under `container machine run`. This
  session shows `/proc/sys` is mounted `ro` and is remountable rw with
  `SYS_ADMIN` — likely the same mechanism, and a lead for that open question.

## Relationship to the backend proposal — superseded

[`apple-container-backend.md`](../proposals/apple-container-backend.md) was
written around **`container machine`** and predates this session. **Its core
mode decision is superseded: mpd does not use `container machine` for
anything.** The `container run` model in this document is the direction.

What carries over from that proposal and what does not:

- **Still applies:** the addressing decision (no guest-side IP pin on macOS —
  take the vmnet address, hand the IP to `takeover`), the octet-range/class
  scheme, the takeover contract, and the general `Backend` shape.
- **Needs rewriting to the `container run` model:** everything specific to
  `container machine` — systemd-as-PID-1, the machine wrapper and first-boot
  user provisioning, `--home-mount`, machine persistence across stop/start, and
  the `container machine create|inspect|run` command surface. The backend
  drives `container run` / `container create|start|stop|rm` instead, and the
  image carries no systemd (sshd is the entrypoint).

That proposal revision is pending and is a separate task from these findings.
