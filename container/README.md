# mpd-virt-container-apple — the Apple-container image for mpd VMs

The base image `mpd-virt create --backend=container` runs and adopts as an
mpd VM. Pre-baked: mpd's bootstrap step 20 is applied at build time (at the
ref `MPD_BOOTSTRAP_REF` selects — the same as `bootstrapRef` in
`internal/cli/adopt.go`, `main` during development), so a VM is a pull plus a
few seconds of adoption.

## Testing a local build

Build with the tag mpd-virt pins (`DefaultContainerImage()` in
`go/internal/backend/create.go`); the local image shadows the published one:

```bash
container build -t ghcr.io/mutms/mpd-virt-container-apple:13.6.3 container
mpd-virt create 141 --backend=container
container image rm ghcr.io/mutms/mpd-virt-container-apple:13.6.3   # back to the published image
```

## Publishing

The tag is `<Debian point release>.<build>` and immutable: publish a new one
when Debian has drifted enough that step 20's re-run is slow again, or when
`MPD_BOOTSTRAP_REF` / `FROM` move. Bump the tag in the script's `TAG`,
`DefaultContainerImage()` and this file, commit, then:

```bash
container registry login ghcr.io      # once
container/github-publish.sh           # builds + pushes
```

## How it works

- Real systemd as PID 1 (`/sbin/init`), so `systemd-resolved` and `ssh` come
  up as on a VM. Needs `--cap-add ALL`: without `CAP_SYS_ADMIN` systemd
  cannot mount cgroups and exits silently.
- Hostname `mpd-<NNN>` comes from the container `--name`.
- Nothing about the user is baked in: the account, sudo and key are added
  after start via `container exec`, so one image serves every VM.
- The IP is whatever vmnet leased, read from `container inspect`; a
  guest-side pin strands the VM.
