#!/usr/bin/env bash
# Provision the dev user on a running takeovertest container, from the macOS
# host. Uses `container exec` (no SSH), so it works before any key is in place,
# and re-running it rotates the key. Change NAME if you used another.

NAME=mpd-141
DEVUSER=skodak
KEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIO61UcrKSqmzE7chEPW3jxexm/7afk7252JsjUoG5I3Y testkey"

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
