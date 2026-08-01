# takeovertest — a container that pretends to be a takeover-ready mpd VM

Runs under plain **`container run`** and comes up looking, from the host's side,
like a freshly-provisioned Debian Trixie box that `mpd-virt takeover` can adopt
— a rehearsal for the future native-container `mpd create`.

## Why `container run`, not `container machine`

`container run` (PID 1 = Apple's `vminitd`) keeps the box **isolated** — no host
`$HOME` mount, its own vmnet address, and we control its hostname and user
accounts. `container machine` boots the image's real `/sbin/init` (systemd) but
mounts your Mac `$HOME` rw, forces the hostname to the machine name, and logs
you in as the host UID — so it's rejected. There is no systemd here: a small
[`entrypoint.sh`](./entrypoint.sh) sets `/etc/hosts` for the hostname, generates
host keys on first boot, and execs `sshd -D`.

## What the box provides

1. **Hostname `mpd-<NNN>`** — set from the container `--name` (vminitd uses it),
   so `--name mpd-128` → `hostname` returns `mpd-128`.
2. **Nothing baked about the user** — the dev account, its passwordless sudo,
   and the authorized SSH key are all created *after* start via `container
   exec`. The image is fully generic; adding/rotating a key or the account is a
   runtime step, never a rebuild.
3. **The vmnet-assigned IP** — whatever the runtime leased. A guest-chosen IP is
   **not** routed by vmnet (proven), so the recipe never touches networking; the
   IP is read from `container ls`/`inspect` and handed to `takeover`.

## Build & run (on the host macOS)

The image is built once and reused for every box:

```bash
container build -t takeovertest containers/takeovertest

# --cap-add ALL is required if Podman must run inside (see docs/internal/apple-containers.md)
container run -d --name mpd-128 --cap-add ALL takeovertest

# Provision the account + sudo + your key in one exec (runs through the runtime,
# no SSH needed). Idempotent — re-run to rotate/add keys:
container exec mpd-128 sh -c '
  id skodak >/dev/null 2>&1 || useradd --create-home --shell /bin/bash skodak
  usermod -aG sudo skodak
  printf "skodak ALL=(ALL) NOPASSWD:ALL\n" > /etc/sudoers.d/90-skodak
  chmod 0440 /etc/sudoers.d/90-skodak
  umask 077; d=/home/skodak/.ssh; mkdir -p "$d"
  printf "%s\n" "'"$(cat ~/.ssh/id_ed25519.pub)"'" >> "$d/authorized_keys"
  chown -R skodak:skodak "$d"
'

container ls        # note the vmnet-assigned IP
```

`mpd create` will do the same: `container run` → `container exec` (account +
sudo + the current Mac user's key) → adopt.

## Connect / take over (from the dev VM)

```bash
ssh skodak@<ip> 'hostname; id; sudo -n true && echo SUDO-OK'
# then adopt it (IP read from `container ls`):
mpd-virt takeover 128 <ip>
```
