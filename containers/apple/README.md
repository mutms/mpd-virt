# mpd-virt-container-apple — the Apple-container base image for mpd boxes

The base image `mpd-virt` runs, provisions, and adopts as an mpd box on Apple
`container`. From the host's side a box booted from it looks like a
freshly-provisioned Debian Trixie machine — the same network + service shape
`mpd-virt create --backend=container` (and manual `takeover`) expect.

## Build it

Build the image once (rebuild when the base changes); everything else runs it:

```bash
cd containers/apple
container build -t mpd-virt-container-apple .
```

## systemd as PID 1

The image boots real systemd (`/sbin/init`) as PID 1, so a box brings up
`systemd-resolved` and `ssh` exactly as a VM does. `container run` needs
`--cap-add ALL`: systemd requires `CAP_SYS_ADMIN` to mount cgroups, and without
it PID 1 exits silently. The box stays isolated — no host `$HOME` mount, its own
vmnet address, and mpd-virt owns its hostname and accounts.

## What a box from it provides

1. **Hostname `mpd-<NNN>`** — taken from the container `--name`, so `--name
   mpd-141` makes `hostname` return `mpd-141` (what `takeover` checks).
2. **Nothing baked about the user** — the dev account, its passwordless sudo,
   and the authorized key are added *after* start via `container exec`, so one
   image serves every box and keys rotate without a rebuild.
3. **The vmnet-assigned IP** — whatever the runtime leased; read from
   `container inspect` (the name does not resolve host-side), never chosen by
   the guest.

## Running & provisioning a box

`mpd-virt create --backend=container` is the intended path: it runs the image,
provisions the account + sudo + your key via `container exec`, reads the IP from
`container inspect`, and adopts the box — no manual steps.

Until that lands, `setup-container.sh` does it by hand from this directory —
build, boot with 10G of memory, wait for systemd, then create the dev user +
passwordless sudo + the SSH key — and prints the IP:

```bash
./setup-container.sh
# then, from the dev VM:
mpd-virt takeover 141 <ip>
```
