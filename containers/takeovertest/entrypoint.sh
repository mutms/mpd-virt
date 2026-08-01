#!/bin/sh
# Init for the takeover-test container (container run / vminitd, no systemd).
# Identity comes from --name: `--name mpd-<NNN>` -> hostname mpd-<NNN> ->
# IP 192.168.64.<NNN>. Reads the octet off the hostname, pins the static IP,
# runs sshd. No build args.
set -u

SUBNET="192.168.64"
GATEWAY="192.168.64.1"

# Octet from the hostname: mpd-128 -> 128.
h="$(hostname 2>/dev/null || echo '')"
octet="${h##*-}"
case "${octet}" in ''|*[!0-9]*) octet="" ;; esac

# Pin the static IP for a valid adopted octet (128..159); otherwise keep the
# DHCP address so a mis-named box isn't stranded.
if [ -n "${octet}" ] && [ "${octet}" -ge 128 ] && [ "${octet}" -le 159 ]; then
    IFACE="$(ip -o -4 route show default 2>/dev/null | awk '{print $5; exit}')"
    IFACE="${IFACE:-eth0}"
    ip addr flush dev "${IFACE}" 2>/dev/null || true
    ip addr add "${SUBNET}.${octet}/24" dev "${IFACE}" 2>/dev/null || true
    ip route replace default via "${GATEWAY}" 2>/dev/null || true
    echo "net: ${IFACE} -> ${SUBNET}.${octet}/24 via ${GATEWAY}" >&2
else
    echo "warn: hostname '${h}' has no octet in 128..159 — keeping DHCP IP" >&2
fi

grep -q "127.0.1.1 ${h}" /etc/hosts 2>/dev/null || echo "127.0.1.1 ${h}" >> /etc/hosts 2>/dev/null || true
[ -f /etc/ssh/ssh_host_ed25519_key ] || ssh-keygen -A
exec /usr/sbin/sshd -D -e
