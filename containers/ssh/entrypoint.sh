#!/bin/sh
set -eu

# Generate host keys on first boot if missing, so the image doesn't ship a
# fixed identity that would be shared across every container built from it.
if [ ! -f /etc/ssh/ssh_host_ed25519_key ]; then
    ssh-keygen -A
fi

# sshd in the foreground so it is PID 1 and the container's lifecycle == sshd's.
# -e logs to stderr (visible via `container logs`); -D keeps it in the foreground.
exec /usr/sbin/sshd -D -e
