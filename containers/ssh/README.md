# ssh-target — host-side SSH container

A minimal Debian Trixie container running `sshd`, key-auth only, for the
Parallels dev VM to reach over SSH. Built and run with Apple `container` on the
host macOS; the dev VM cannot build/run containers itself (no nested
virtualization).

## What's baked in

- **Base:** `debian:trixie-slim`
- **Auth:** public-key only — passwords and keyboard-interactive disabled.
  `root` is authorized via [`authorized_keys`](./authorized_keys) (the dev VM's
  `~/.ssh/id_ed25519.pub`, `skodak@macos.shared`).
- **Host keys:** generated on first boot (`entrypoint.sh`), not shipped in the
  image, so two containers off this image don't share a host identity.
- **PID 1:** `sshd -D -e`, so the container lives and dies with sshd.

## Build & run (on the host macOS)

```bash
# from the repo root
container build -t ssh-target:latest containers/ssh

container run -d --name ssh-target ssh-target:latest
container ls                       # note the container's IP (192.168.64.x)
```

## Connect (from the dev VM)

```bash
ssh -o StrictHostKeyChecking=accept-new root@<container-ip> 'hostname; id'
```

## Notes

- Rotating the authorized key = edit `authorized_keys`, rebuild, re-run.
- To authorize additional keys, append lines to `authorized_keys` before build.
- Logs: `container logs ssh-target` (sshd runs with `-e`).
