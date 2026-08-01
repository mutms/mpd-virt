# takeovertest — a container that pretends to be a takeover-ready mpd VM

Runs under plain `container run` and comes up, from the host's side, looking
like a freshly-provisioned Debian Trixie box that `mpd-virt takeover` can
adopt — a rehearsal for the future native-container `mpd create`.

## systemd as PID 1

The image boots real systemd (`/sbin/init`) as PID 1, so the box brings up
`systemd-resolved` and `ssh` exactly as a VM does and presents the same
network + service shape `takeover` expects. `container run` needs `--cap-add
ALL` for this: systemd requires `CAP_SYS_ADMIN` to mount cgroups, and without
it PID 1 exits silently. The box stays isolated — no host `$HOME` mount, its
own vmnet address, and we own its hostname and accounts.

## What the box provides

1. **Hostname `mpd-<NNN>`** — taken from the container `--name`, so `--name
   mpd-141` makes `hostname` return `mpd-141`.
2. **Nothing baked about the user** — the dev account, its passwordless sudo,
   and the authorized key are added *after* start via `container exec`. The
   image is generic; adding or rotating a key is a runtime step, never a
   rebuild.
3. **The vmnet-assigned IP** — whatever the runtime leased. A guest-chosen IP
   is not routed by vmnet, so the recipe never touches networking; the IP is
   read from `container ls` and handed to `takeover`.

## Build, run & provision (on the host macOS)

Run the helper **from this directory**. It builds the image, boots systemd
under `container run` with 10G of memory, waits for the boot to finish, then
creates the dev user + passwordless sudo + the test SSH key:

```bash
cd containers/takeovertest
./setup-test-container.sh
```

It ends with `container ls`; note the vmnet-assigned IP. Re-running it
recreates the box from scratch.

`mpd create` will do the same: `container run` → `container exec` (account +
sudo + the current Mac user's key) → adopt.

## Connect / take over (from the dev VM)

```bash
ssh skodak@<ip> 'hostname; id; sudo -n true && echo SUDO-OK'
mpd-virt takeover 141 <ip>
```
