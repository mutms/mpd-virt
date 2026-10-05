# UTM backend (Apple-Silicon Mac)

`mpd-virt create NNN --backend=utm` makes a VM in UTM.app: the arm64 Debian
Trixie cloud image (downloaded and SHA-512-verified once into
`~/.mpd-virt/conf/cloud-images/`), a cidata seed with your user and key, on
UTM's shared network, then the normal adoption. `start`/`stop` drive UTM over
AppleScript; `remove --full` deletes the VM and its bundle.

Nothing to prepare beyond installing UTM (App Store or
<https://mac.getutm.app>).

## Address

The VM takes a DHCP lease on the shared network — there is no fixed address.
mpd-virt finds the current one through UTM's guest-agent query, or
`mpd-NNN.local` over mDNS, on every `start`. First boot takes about a minute:
cloud-init installs the guest agent and avahi before the VM can be found.

## No window, and how to watch the boot

A created VM has no display, so starting it opens no window — it runs in the
background and you reach it over ssh.

To see the boot sequence, give it a serial console: stop the VM, open its
settings in UTM, set **Serial** to **Built-in Terminal**, and start it again.
UTM then opens a terminal window showing the kernel and systemd output. It is
for watching, not logging in: the dev user has no password, only your ssh key.

A graphical desktop does not need a display either: GNOME installed in the VM
and reached over RDP works with no video card attached.

If you do want a window — the text console, or a local GNOME session — stop
the VM, add a new **Display** device in its UTM settings and set it to
`virtio-gpu-pci`. UTM then opens a window for the VM on every start.

## Disk

The disk and the cidata seed both live inside the VM's `.utm` bundle.

- **Grow it**: stop the VM, resize the drive in UTM's settings, start it. The
  root filesystem grows to fill the disk on that boot — the seed stays
  attached, so cloud-init is still there to do it.
- **Reclaim space**: UTM's drive settings can compress the image while the VM
  is stopped.

`--disk` on `create` sets the initial size (default 80g).
