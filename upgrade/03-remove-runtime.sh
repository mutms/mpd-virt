#!/usr/bin/env bash
# 03-remove-runtime.sh — retire the runtime container from an adopted VM.
#
# mpd no longer runs a runtime container: PHP-FPM and the project caddy
# run on the VM, on a second bridge address (10.163.<NNN>.2) the caddy
# binds. The old container holds that address, so two owners on one
# segment is an ARP conflict — `mpd --vm-setup` refuses while it exists.
# This removes it first, then lets the normal update/setup converge.
#
# Runs against the OLD code still installed on the VM, so the container's
# home is backed up with the tooling that still knows how.
#
# Nothing here touches project data: /srv is the data volume, bind-mounted
# rather than owned by the container, and the database containers are left
# alone. What the removal destroys is the container's own home directory —
# shell history, IDE settings, dotfiles — which step 1 preserves.
#
# Run from the mpd-virt host, once per VM. Idempotent.
# Usage: 03-remove-runtime.sh <NNN>
set -euo pipefail

NNN="${1:?usage: 03-remove-runtime.sh <NNN>}"

echo "==> start ${NNN}"
mpd-virt start "${NNN}"

# The remote half goes to a file rather than straight into the command
# substitution below: macOS ships bash 3.2, whose parser matches the
# closing `)` of a `$( )` without regard for a heredoc inside it, so
# parentheses in the heredoc text break the script.
REMOTE_SCRIPT="$(mktemp)"
trap 'rm -f "${REMOTE_SCRIPT}"' EXIT
cat > "${REMOTE_SCRIPT}" <<'REMOTE'
set -euo pipefail

RUNTIME="$(sudo podman ps -a --filter label=mpd.runtime --format '{{.Names}}' | head -n 1)"

# Back up first, while the old binary that knows how is still installed.
# Best-effort: a VM whose runtime is already gone has nothing to save, and
# a failed backup must not block the migration.
if [ -n "${RUNTIME}" ] && command -v mpd >/dev/null 2>&1; then
    echo "==> backing up the runtime home to /srv/backups/runtime/"
    mpd --runtime-backup || echo "    warn: backup failed — continuing" >&2
fi

if [ -n "${RUNTIME}" ]; then
    echo "==> removing container ${RUNTIME}"
    sudo podman rm -f "${RUNTIME}"
    sudo podman volume rm "${RUNTIME}-tmp" 2>/dev/null || true
else
    echo "==> no runtime container — already removed"
fi

# The control socket served `mpd` typed inside the container. Nothing
# forwards any more, so the daemon and its socket directory go.
if systemctl --user list-unit-files mpd-control.service >/dev/null 2>&1; then
    echo "==> removing mpd-control.service"
    systemctl --user disable --now mpd-control.service 2>/dev/null || true
    rm -f ~/.config/systemd/user/mpd-control.service
    systemctl --user daemon-reload
fi

echo "==> removing runtime state"
# runtimes/ and runtime-ssh/ are root-owned; home/ was the container's
# dotfile override mount. All three are rebuilt or obsolete.
sudo rm -rf /var/lib/mpd/state/runtimes /var/lib/mpd/state/runtime-ssh \
    /var/lib/mpd/run /var/lib/mpd/home

# mpd wrote these aliases for reaching the container from the VM.
if [ -f ~/.ssh/config ]; then
    echo "==> stripping the mpd runtimes block from ~/.ssh/config"
    sed -i '/^# >>> mpd runtimes/,/^# <<< mpd runtimes/d' ~/.ssh/config
fi

# vm.env is the only env file now; fold in whatever runtime.env held.
if [ -f /var/lib/mpd/env/runtime.env ]; then
    echo "==> folding runtime.env into vm.env"
    touch /var/lib/mpd/env/vm.env
    if ! grep -qxFf /var/lib/mpd/env/runtime.env /var/lib/mpd/env/vm.env 2>/dev/null; then
        printf '\n# merged from runtime.env by upgrade/03-remove-runtime.sh\n' \
            >> /var/lib/mpd/env/vm.env
        cat /var/lib/mpd/env/runtime.env >> /var/lib/mpd/env/vm.env
    fi
    rm -f /var/lib/mpd/env/runtime.env
fi

echo "==> removing the runtime image"
sudo podman image rm --force ghcr.io/mutms/mpd-runtime 2>/dev/null || true

echo "==> VM side done"
REMOTE

echo "==> VM: remove the runtime"
ssh "mpd-${NNN}-vm" bash -s < "${REMOTE_SCRIPT}"

# The developer's overlay tier moved with mpd's own: everything that was a
# runtime tool is now a VM tool.
OVERLAY=~/.mpd-virt/assets
if [ -d "${OVERLAY}/runtime" ]; then
    echo "==> this host: moving ${OVERLAY}/runtime into ${OVERLAY}/vm"
    mkdir -p "${OVERLAY}/vm"
    # -n so a file the developer already has under vm/ wins; the leftover
    # runtime/ is reported rather than deleted, so nothing is lost silently.
    cp -Rn "${OVERLAY}/runtime/." "${OVERLAY}/vm/" 2>/dev/null || true
    if [ -n "$(find "${OVERLAY}/runtime" -type f 2>/dev/null)" ]; then
        echo "    kept ${OVERLAY}/runtime — compare it against vm/ and remove it yourself"
    else
        rmdir -p "${OVERLAY}/runtime" 2>/dev/null || true
    fi
fi

# The runtime had its own pinned host key; there is no second hop now.
KNOWN_HOSTS=~/.mpd-virt/"${NNN}"/known_hosts
if [ -f "${KNOWN_HOSTS}" ]; then
    echo "==> this host: unpinning mpd-${NNN}-runtime"
    ssh-keygen -R "mpd-${NNN}-runtime" -f "${KNOWN_HOSTS}" >/dev/null 2>&1 || true
fi

# Rewrites the ssh-config block (bare name straight at the VM, no
# ProxyJump), pushes assets and vm.env, then runs `mpd --vm-setup`, which
# now installs PHP, the project caddy and the second bridge address.
echo "==> update ${NNN}"
mpd-virt update "${NNN}"

echo "==> done — 'ssh mpd-${NNN}' lands on the VM, where the tools now live"
