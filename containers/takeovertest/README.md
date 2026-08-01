# takeovertest — a container that pretends to be a takeover-ready mpd VM

Boots **systemd as PID 1** and comes up looking like a freshly-provisioned
Debian Trixie VM already sitting at its canonical address — so
`mpd-virt setup <NNN>` can adopt it exactly as it would a Parallels/UTM VM.
The `30-networking.sh` relocation step has nothing left to do; takeover
resumes at install/build/`mpd --vm-setup`.

## What "takeover ready" means here

1. **Fixed IP** `<SUBNET>.<OCTET>` via `systemd-networkd`, DHCP off — the box owns its address.
2. **Dev user `skodak`** with passwordless sudo and the authorized key ([`authorized_keys`](./authorized_keys)).
3. **Hostname `mpd-0NN`** (zero-padded to 3 digits, matching `vmName(octet:)`).
4. **The usual Debian networking stack** (systemd-networkd), not an ad-hoc entrypoint.

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
container ls          # confirm it reports 192.168.64.128
```

Build args: `OCTET` (128..159), `SUBNET` (default `192.168.64`),
`GATEWAY` (default `<SUBNET>.1`), `DEVUSER` (default `skodak`).

## Connect / take over (from the dev VM)

```bash
ssh skodak@192.168.64.128 'hostname; id; sudo -n true && echo SUDO-OK'
# then, once wired into mpd-virt:
mpd-virt setup 128 --username skodak
```

## Open question this recipe deliberately tests

Whether **systemd comes up cleanly as PID 1 under Apple `container`** and the
**static IP holds** end to end. If systemd stalls at boot, check
`container logs takeovertest` — the fallback is a lighter init that just applies the
static address and starts sshd (no full systemd), at the cost of VM fidelity.
