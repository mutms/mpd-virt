# mpd-virt-container-apple — the Apple-container base image for mpd VMs

The base image `mpd-virt` runs, provisions, and adopts as an mpd VM on Apple
`container`. From the host's side a VM booted from it looks like a
freshly-provisioned Debian Trixie machine — the same network + service shape
`mpd-virt create --backend=container` (and a manual `adopt`) expect.

## Testing a local build

Build with the tag mpd-virt currently pins (`DefaultContainerImage()` in
`go/internal/backend/create.go`); the local image shadows the published
one on this Mac only:

```bash
container build -t ghcr.io/mutms/mpd-virt-container-apple:13.6.1 container
mpd-virt create 141 --backend=container
```

When done, remove it so the next `create` pulls the published image again:

```bash
container image rm ghcr.io/mutms/mpd-virt-container-apple:13.6.1
```

## Publishing image

Nobody builds this per Mac. `mpd-virt create --backend=container` runs the
tag `backend.DefaultContainerImage()` names — and `container run`
pulls it when it is not local. That is the point of the image: it is
pre-baked (mpd's bootstrap step 20 already applied), so a VM is a pull
plus a few seconds of adoption rather than a 300 MB apt run.

The tag is `<Debian point release>.<build>`. Tags should be immutable; publish
a new one when Debian has drifted far enough that adoption's re-run of
step 20 is slow again (or when `MPD_BOOTSTRAP_REF` / `FROM` move):

```bash
container registry login ghcr.io      # once
container/github-publish.sh           # builds + pushes the TAG set in the script
```

To bump the tag, search-and-replace it across the repo (the script's
`TAG`, `DefaultContainerImage()` in `go/internal/backend/create.go`, and
this file), commit, then run the script.

## systemd as PID 1

The image boots real systemd (`/sbin/init`) as PID 1, so a VM brings up
`systemd-resolved` and `ssh` exactly as a VM does. `container run` needs
`--cap-add ALL`: systemd requires `CAP_SYS_ADMIN` to mount cgroups, and without
it PID 1 exits silently. The VM stays isolated — no host `$HOME` mount, its own
vmnet address, and mpd-virt owns its hostname and accounts.

## What a VM from it provides

1. **Hostname `mpd-<NNN>`** — taken from the container `--name`, so `--name
   mpd-141` makes `hostname` return `mpd-141` (what `adopt` checks).
2. **Nothing baked about the user** — the dev account, its passwordless sudo,
   and the authorized key are added *after* start via `container exec`, so one
   image serves every VM and keys rotate without a rebuild.
3. **The vmnet-assigned IP** — whatever the runtime leased; read from
   `container inspect` (the name does not resolve host-side), never chosen by
   the guest. vmnet only delivers to the address it leased, so a guest-side
   pin strands the VM.
4. **Every package mpd needs, already installed** — the image runs mpd's
   bootstrap step 20 (`bootstrap/20-install-software.sh`: apt dist-upgrade,
   podman, dnsmasq, caddy, WireGuard, the build toolchain, avahi,
   qemu-guest-agent) at build time, at the commit pinned by
   `MPD_BOOTSTRAP_REF` in the Containerfile — the same pin as `bootstrapRef`
   in `internal/cli/adopt.go`. Adoption re-runs the step and finds nothing to
   do.

## Running & provisioning a VM

`mpd-virt create <NNN> --backend=container` is the path: it runs the image,
provisions the account + sudo + your key via `container exec`, reads the IP
from `container inspect`, and adopts the VM — no manual steps. `container run`
pulls the published image on first use; `create` itself never builds one.
