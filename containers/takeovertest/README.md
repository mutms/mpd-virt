# takeovertest — a container that pretends to be a takeover-ready mpd VM

Runs under plain **`container run`** and comes up looking, from the host's side,
like a freshly-provisioned Debian Trixie VM already sitting at its canonical
address — so `mpd-virt setup <NNN>` can adopt it exactly as it would a
Parallels/UTM VM. `30-networking.sh` has nothing left to do; takeover resumes
at install/build/`mpd --vm-setup`.

## Why no systemd (we checked)

Apple `container` has two modes, and they're a hard fork:

- **`container run`** — PID 1 is Apple's `vminitd`, which only execs the
  entrypoint. No systemd, but the box is **isolated** (no host mount), gets its
  **own IP**, and we control its hostname and user accounts.
- **`container machine`** — boots the image's real `/sbin/init`, so systemd
  runs in full. But it also mounts your Mac home over virtiofs (`rw`), assigns
  a **DHCP** IP, forces the hostname to the machine name, and logs you in as the
  **host UID**. Verified: `hostname` → `takeovertest`, `eth0` → `.9` (not our
  static `.128`), `/Users/skodak` mounted rw.

A takeover target must be isolated, at a fixed IP, named `mpd-<NNN>`, and
adoptable over SSH as `skodak`. Only `container run` delivers that — so **there
is no systemd here**. A small [`entrypoint.sh`](./entrypoint.sh) plays init:
sets the hostname, pins the static IP, and execs `sshd -D`. From the host's
point of view the box is indistinguishable from a real VM at that address —
which is all takeover actually inspects.

## What "takeover ready" means here

1. **Fixed IP** `<SUBNET>.<OCTET>` — the entrypoint flushes the DHCP lease and pins it.
2. **Dev user `skodak`** with passwordless sudo and the authorized key ([`authorized_keys`](./authorized_keys)).
3. **Hostname `mpd-0NN`** (zero-padded to 3 digits, matching `vmName(octet:)`), set by the entrypoint.
4. **A working networking posture** at the canonical address (via `ip`, not systemd-networkd — the observable end state is the same).

**OCTET must be in `128..159`** — the subnet plan's "general VMs (adopted)"
band (per-VM on-link next hop), which is what an adopted Apple container is.
The build hard-fails on anything outside that range.

Everything above is also the groundwork the future *native* container backend
will bake in — this recipe is a rehearsal, not a throwaway.

## Build & run (on the host macOS)

```bash
# octet 128 -> hostname mpd-128, IP 192.168.64.128
container build --build-arg OCTET=128 -t takeovertest:128 containers/takeovertest

container run -d --name takeovertest takeovertest:128
container ls          # confirm STATE=running and the IP
```

Build args: `OCTET` (128..159), `SUBNET` (default `192.168.64`),
`GATEWAY` (default `192.168.64.1`), `DEVUSER` (default `skodak`).

## Connect / take over (from the dev VM)

```bash
ssh skodak@192.168.64.128 'hostname; id; sudo -n true && echo SUDO-OK'
# then, once wired into mpd-virt:
mpd-virt setup 128 --username skodak
```

## The one thing left to prove

Whether the guest can **hold a static IP that vmnet actually routes**. `vminitd`
brings the iface up via DHCP; the entrypoint flushes that and pins
`<subnet>.<octet>`. If `ssh skodak@192.168.64.128` fails but the container is
running, check `container logs takeovertest` for the `net:` / `warn:` line —
that tells us whether the pin took and whether vmnet honored it. If vmnet
refuses a guest-chosen address, the alternatives are a `container run` IP flag
(if one exists) or accepting the DHCP IP as canonical.
