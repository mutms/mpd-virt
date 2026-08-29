#!/usr/bin/env bash
# 02-runtime-sshkey.sh — give an already-created runtime its own sshd host
#
# Runtimes created from an image built before mpd stopped shipping host
# keys answer with the key that was baked into that image, so it is the
# same key in every runtime pulled from that tag. Fresh creates generate
# their own; this replaces the key in an existing one without a rebuild.
#
# Two halves, useless apart:
#   - on the VM: a new key for the runtime container, stored at
#     /var/lib/mpd/state/runtime-ssh/ so later rebuilds keep answering
#     with it;
#   - on this host: the stale mpd-<NNN>-runtime entry cleared from
#     ~/.ssh/known_hosts and the new key pinned, or the next
#     `ssh mpd-<NNN>` is refused.
#
# Idempotent: the keys the old image shipped are known (their fingerprints
# are below), so a runtime that already has its own is left alone and only
# the known_hosts pin is refreshed.
#
# Run from the mpd-virt host, once per VM. Usage: 02-runtime-sshkey.sh <NNN>
set -euo pipefail

NNN="${1:?usage: 02-runtime-sshkey.sh <NNN>}"
KEEP=/var/lib/mpd/state/runtime-ssh

# The host key fingerprints baked into old delete image —
# the ones every runtime pulled from that tag shares. Matching one is what
# marks a runtime as not yet fixed.
IMAGE_KEYS="SHA256:jCde2p1+4+95xjrfmNyPH9viGutsYkr99JnJewTkyQc
SHA256:pUyT6rHpl1K8wxItNlHb+/u+5h/CjTtqRDQAQSwwGGo
SHA256:Ifil+N5JPILCY4O4C9IxMsu42IPGYX+q4nA3i+9P8vY"

echo "==> start ${NNN}"
mpd-virt start "${NNN}"

echo "==> VM: new host key for the runtime container"
ssh "mpd-${NNN}-vm" bash -s -- "${IMAGE_KEYS}" <<'REMOTE'
set -euo pipefail
IMAGE_KEYS="$1"
KEEP=/var/lib/mpd/state/runtime-ssh
RUNTIME="$(sudo podman ps --filter label=mpd.runtime --format '{{.Names}}' | head -n 1)"
if [ -z "${RUNTIME}" ]; then
    echo "No running runtime container. Start it with: mpd --vm-start" >&2
    exit 1
fi

# Only replace a key that came from the image. A runtime already carrying
# its own is left exactly as it is — re-running must not rotate a good key
# and strand every other workstation that trusts it.
CURRENT="$(sudo podman exec "${RUNTIME}" bash -c \
    'for f in /etc/ssh/ssh_host_*_key.pub; do ssh-keygen -lf "$f"; done' | awk '{print $2}')"
if ! grep -qxF -f <(printf '%s\n' "${IMAGE_KEYS}") <(printf '%s\n' "${CURRENT}"); then
    echo "    already has its own key — nothing to replace"
    exit 0
fi

sudo podman exec "${RUNTIME}" bash -c 'rm -f /etc/ssh/ssh_host_*; ssh-keygen -A'

# Copied out from the VM side rather than written from inside the
# container: a runtime created before the keep-directory mount existed
# cannot see that path, and this works either way. Globbing inside KEEP
# happens in a root shell — it is 0700 root-owned, so a glob in this shell
# would expand to nothing and silently remove nothing.
sudo install -d -m 700 "${KEEP}"
sudo bash -c 'rm -f "$1"/ssh_host_*' _ "${KEEP}"
for f in $(sudo podman exec "${RUNTIME}" bash -c 'ls /etc/ssh/ssh_host_*'); do
    dest="${KEEP}/$(basename "${f}")"
    sudo podman cp "${RUNTIME}:${f}" "${dest}"
    case "${f}" in
        *.pub) sudo chmod 644 "${dest}" ;;
        *) sudo chmod 600 "${dest}" ;;
    esac
done

sudo podman exec "${RUNTIME}" systemctl restart ssh
echo "    stored; sshd restarted (established sessions are unaffected)"
sudo ssh-keygen -lf "${KEEP}/ssh_host_ed25519_key.pub" | sed 's/^/    /'
REMOTE

echo "==> this host: re-pin mpd-${NNN}-runtime in ~/.ssh/known_hosts"
# Only the runtime alias — the VM's own key did not change, and dropping
# its pin would cost trust for nothing. ssh-keygen -R exits 0 whether or
# not the entry was there, and keeps the previous file as known_hosts.old.
ssh-keygen -R "mpd-${NNN}-runtime" >/dev/null 2>&1 || true

mkdir -p ~/.ssh && chmod 700 ~/.ssh
touch ~/.ssh/known_hosts && chmod 600 ~/.ssh/known_hosts
# Read back over the ssh connection whose own host key is pinned, so the
# new key arrives on a channel that is already trusted.
ssh "mpd-${NNN}-vm" "sudo -n cat ${KEEP}/ssh_host_*_key.pub" \
    | awk -v alias="mpd-${NNN}-runtime" 'NF >= 2 { print alias, $1, $2 }' \
    >> ~/.ssh/known_hosts

echo "==> done — 'ssh mpd-${NNN}' connects with no prompt"
