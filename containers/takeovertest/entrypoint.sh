#!/bin/sh
# Minimal init for the takeover-test container. Under `container run`, vminitd
# execs this as the container's process; it does the parts of a VM's early boot
# that a takeover target must present, then hands PID 1 to sshd.
#
# NOTE: the IP is left as vmnet assigned it (dynamic). We do NOT pin a static
# address here — under Apple `container`, a guest-chosen IP isn't routed by
# vmnet, and flushing the iface strands the box. Pinning the canonical address
# (if possible at all) belongs at `container run` time, not in the guest.
#
# Everything below is best-effort and MUST fall through to `exec sshd` — a
# takeover target that isn't reachable over SSH is useless, so nothing here is
# allowed to abort before sshd starts.

# Canonical identity, baked at build time (HOSTNAME=mpd-<NNN>, plus unused
# IPCIDR/GATEWAY kept for when runtime IP assignment lands).
. /etc/mpd-net.conf 2>/dev/null || true

# Hostname -> mpd-<NNN> (vminitd otherwise names it after the container --name).
hostname "${HOSTNAME:-}" 2>/dev/null || echo "warn: could not set hostname" >&2

# Loopback name mapping (best effort — /etc/hosts may be a read-only mount).
if [ -n "${HOSTNAME:-}" ] && ! grep -q "127.0.1.1 ${HOSTNAME}" /etc/hosts 2>/dev/null; then
    echo "127.0.1.1 ${HOSTNAME}" >> /etc/hosts 2>/dev/null \
        || echo "warn: /etc/hosts not writable, skipping loopback entry" >&2
fi

# Host keys on first boot (not baked, so containers don't share an identity).
[ -f /etc/ssh/ssh_host_ed25519_key ] || ssh-keygen -A

# sshd in the foreground as PID 1.
exec /usr/sbin/sshd -D -e
