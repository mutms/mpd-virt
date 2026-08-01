#!/bin/sh
# Init for the takeover-test container (container run / vminitd, no systemd).
#
# We do NOT touch networking. Apple's vmnet only routes the address it leased;
# a guest-set IP is never routed (confirmed even with CAP_NET_ADMIN), and
# flushing the lease just strands the box. So the container keeps whatever IP
# vmnet assigned. Its identity is the hostname (from `--name mpd-<NNN>`) plus
# DNS (<NNN>.mpd.test); the IP is supplied explicitly to `mpd-virt takeover`.
set -u

h="$(hostname 2>/dev/null || echo '')"
[ -n "${h}" ] && { grep -q "127.0.1.1 ${h}" /etc/hosts 2>/dev/null || echo "127.0.1.1 ${h}" >> /etc/hosts 2>/dev/null || true; }

[ -f /etc/ssh/ssh_host_ed25519_key ] || ssh-keygen -A
exec /usr/sbin/sshd -D -e
