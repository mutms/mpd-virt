#!/usr/bin/env bash
# 02-runtime-sshkey.sh — give an already-created runtime its own sshd host
# key.
#
# Runtimes created from an image built before mpd stopped shipping host
# keys answer with the key baked into that image, which is the same key in
# every runtime pulled from it. Fresh creates generate their own; this
# replaces the key in an existing one without a rebuild.
#
# Three things have to end up right, and the script ensures all three
# whether or not it had to generate anything:
#   - the runtime holds a key generated in the runtime;
#   - /var/lib/mpd/state/runtime-ssh/ holds a copy, so a later
#     `mpd --runtime-rebuild` answers with the same key;
#   - ~/.mpd-virt/<NNN>/known_hosts here pins it, or `ssh mpd-<NNN>` is
#     refused.
#
# Idempotent. A key generated in the container carries the comment
# root@<container>, an image's carries the build host's — which is the
# test, and unlike a list of known fingerprints it holds for a runtime
# built from any image.
#
# Run from the mpd-virt host, once per VM. Usage: 02-runtime-sshkey.sh <NNN>
set -euo pipefail

NNN="${1:?usage: 02-runtime-sshkey.sh <NNN>}"

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
KEEP=/var/lib/mpd/state/runtime-ssh

# Only the public keys may reach stdout: the caller writes it into a
# known_hosts file, and one stray line there invalidates the whole file.
# `ssh-keygen -A` and podman both print, so stdout goes to stderr and the
# keys are written to fd 3.
exec 3>&1 1>&2

RUNTIME="$(sudo podman ps --filter label=mpd.runtime --format '{{.Names}}' | head -n 1)"
if [ -z "${RUNTIME}" ]; then
    echo "No running runtime container. Start it with: mpd --vm-start" >&2
    exit 1
fi

# ssh-keygen -A stamps the generating host into the comment, so a key made
# in this container names it and one baked into an image does not.
COMMENT="$(sudo podman exec "${RUNTIME}" \
    ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | awk '{print $3}')"
if [ "${COMMENT}" = "root@${RUNTIME}" ]; then
    echo "    key is the runtime's own — keeping it" >&2
else
    echo "    replacing ${COMMENT} with a key generated in the runtime" >&2
    sudo podman exec "${RUNTIME}" bash -c 'rm -f /etc/ssh/ssh_host_*; ssh-keygen -A'
    RESTART=yes
fi

# Store (or re-store) the keys for later rebuilds. Copied out from the VM
# side: a runtime created before the keep-directory mount existed cannot
# see that path. Globbing inside KEEP happens in a root shell — it is 0700
# root-owned, so a glob in this shell would expand to nothing and silently
# remove nothing.
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
echo "    stored in ${KEEP}" >&2

sudo podman exec "${RUNTIME}" bash -c 'cat /etc/ssh/ssh_host_*_key.pub' >&3

if [ -n "${RESTART:-}" ]; then
    sudo podman exec "${RUNTIME}" systemctl restart ssh
    echo "    sshd restarted; established sessions are unaffected" >&2
fi
REMOTE

echo "==> VM: runtime host key"
# Progress goes to stderr so stdout carries only the public keys. They are
# read here, over the connection whose host key is already pinned, rather
# than in a second call — sshd is restarted last, so this session survives
# it and the pin below is in place before anything reconnects.
PUBKEYS="$(ssh "mpd-${NNN}-vm" bash -s < "${REMOTE_SCRIPT}")"

KNOWN_HOSTS=~/.mpd-virt/"${NNN}"/known_hosts
echo "==> this host: pin mpd-${NNN}-runtime in ${KNOWN_HOSTS}"
# The per-VM file, never ~/.ssh/known_hosts: the managed ssh-config block
# points UserKnownHostsFile here. ssh-keygen -R exits 0 whether or not the
# entry was there, and keeps the previous file as known_hosts.old. Only the
# runtime alias: the VM's own key did not change.
mkdir -p ~/.mpd-virt/"${NNN}" && chmod 700 ~/.mpd-virt/"${NNN}"
touch "${KNOWN_HOSTS}" && chmod 600 "${KNOWN_HOSTS}"
ssh-keygen -R "mpd-${NNN}-runtime" -f "${KNOWN_HOSTS}" >/dev/null 2>&1 || true

# Key lines only. A line that is not one would invalidate the file for ssh.
printf '%s\n' "${PUBKEYS}" \
    | awk -v alias="mpd-${NNN}-runtime" \
        'NF >= 2 && $1 ~ /^(ssh-|ecdsa-|sk-)/ { print alias, $1, $2 }' \
    >> "${KNOWN_HOSTS}"

echo "==> done — 'ssh mpd-${NNN}' connects with no prompt"
