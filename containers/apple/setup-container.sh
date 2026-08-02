#!/usr/bin/env bash
# Build the mpd-virt-container-apple image, boot it as a systemd box under `container run`,
# and provision the dev user - all in one go. Run from THIS directory:
#
#   cd containers/apple
#   ./setup-container.sh
#
# The box boots real systemd (PID 1) with 10G of memory, then gets the dev
# account + passwordless sudo + the test SSH key added via `container exec`.
# Afterwards adopt it from the dev VM with:  mpd-virt takeover 141 <ip>

set -e

NAME=mpd-181
DEVUSER=skodak
MEMORY=10g
KEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIO61UcrKSqmzE7chEPW3jxexm/7afk7252JsjUoG5I3Y testkey"

# 1. Build the image from this directory.
container build -t mpd-virt-container-apple .

# 2. Refuse to clobber an existing box of the same name - force-deleting one
#    could throw away a box you still want. Stop and remove it yourself first.
if container ls -a 2>/dev/null | grep -qw "$NAME"; then
  echo "A container named '$NAME' already exists."
  echo "Stop and delete it first, then re-run this script:"
  echo "    container stop $NAME && container rm $NAME"
  exit 1
fi

# 3. Boot systemd as PID 1. --cap-add ALL gives systemd the CAP_SYS_ADMIN it
#    needs to mount cgroups.
container run -d --name "$NAME" --cap-add ALL --memory "$MEMORY" mpd-virt-container-apple

# 4. Wait for systemd to finish booting (running or degraded both mean "up").
until container exec "$NAME" sh -c 'systemctl is-system-running | grep -qE "running|degraded"'; do
  sleep 1
done

# 5. Create the dev user + passwordless sudo + authorized key (idempotent;
#    re-run to rotate the key).
container exec "$NAME" sh -c "id $DEVUSER >/dev/null 2>&1 || useradd --create-home --shell /bin/bash $DEVUSER"
container exec "$NAME" usermod -aG sudo "$DEVUSER"
container exec "$NAME" sh -c "printf '%s ALL=(ALL) NOPASSWD:ALL\n' $DEVUSER > /etc/sudoers.d/90-$DEVUSER"
container exec "$NAME" chmod 0440 "/etc/sudoers.d/90-$DEVUSER"
container exec "$NAME" install -d -m 700 -o "$DEVUSER" -g "$DEVUSER" "/home/$DEVUSER/.ssh"
container exec "$NAME" sh -c "printf '%s\n' '$KEY' > /home/$DEVUSER/.ssh/authorized_keys"
container exec "$NAME" chmod 600 "/home/$DEVUSER/.ssh/authorized_keys"
container exec "$NAME" chown "$DEVUSER:$DEVUSER" "/home/$DEVUSER/.ssh/authorized_keys"

echo
echo "done - $DEVUSER ready on $NAME:"
container exec "$NAME" sh -c "id $DEVUSER; grep -c . /home/$DEVUSER/.ssh/authorized_keys"
container ls
