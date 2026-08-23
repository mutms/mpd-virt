# libvirt / KVM backend (Linux host)

`mpd-virt create NNN --backend=libvirt` makes a KVM VM on the Linux VM
mpd-virt runs on: the amd64 Debian Trixie cloud image (downloaded and
SHA-512-verified once into `~/.mpd-virt/conf/cloud-images/`), a cidata
seed with your user and key, pinned to `192.168.122.NNN` on libvirt's
`default` NAT network, then the normal adoption. `start`/`stop` are
`virsh start`/`shutdown`; `remove --full` undefines the VM and deletes its
files.

## One-time host prep

```sh
sudo apt-get install -y --no-install-recommends qemu-system-x86 libvirt-daemon-system libvirt-clients qemu-utils genisoimage
sudo adduser "$USER" libvirt          # then log in again
sudo virsh net-start default && sudo virsh net-autostart default
sudo install -d -o "$USER" -g "$USER" -m 0755 /var/lib/mpd-virt
```

`/var/lib/mpd-virt/<VM>/` holds each VM's disk and seed. It is not under
`~/.mpd-virt` because qemu runs as `libvirt-qemu` and a Debian home
directory is `0700`.

Nested inside another VM (a Proxmox VM with CPU type `host`), this is how
an agent gets a hypervisor of its own; `grep -c vmx /proc/cpuinfo` > 0 and
`/dev/kvm` present means it will work. Keep the `<video>` device in the
domain even though nothing displays it: libvirt starts qemu with
`-nodefaults`, and a nested guest without any video device never gets past
GRUB ("Booting Debian GNU/Linux" loops on the serial console) — the same
image boots fine with a VGA device present.

## Reaching the VM

The VM is on the host only (`192.168.122.NNN`). `ssh mpd-NNN` works from
the host right after adoption; `https://*.NNN.mpd.test` from the host's
browser needs the host to route `10.163.NNN.0/24` via the VM and resolve
`*.mpd.test` at `10.163.NNN.1` — mpd-proxy's job on macOS, not wired on
Linux yet. The SOCKS path (`ssh -D`) works everywhere.
