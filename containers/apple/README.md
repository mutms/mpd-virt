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
   the guest. vmnet only delivers to the address it leased, so a guest-side
   pin strands the box.

## Running & provisioning a box

`mpd-virt create <NNN> --backend=container` is the path: it runs the image,
provisions the account + sudo + your key via `container exec`, reads the IP
from `container inspect`, and adopts the box — no manual steps. The image must
exist first (build it as above — `create` neither builds nor pulls it).

### Manual fallback (debugging only)

`setup-container.sh` does the same steps by hand from this directory — build,
boot `mpd-181` with 10G of memory, wait for systemd, then create the dev user +
passwordless sudo + an SSH key — and prints the IP. It is a hardcoded personal
spike (`mpd-181`, `DEVUSER=skodak`, a throwaway `testkey`), useful for poking
at the image, not a supported path:

```bash
./setup-container.sh
# then, if you really want to adopt that box:
mpd-virt takeover 181 <ip>
```
