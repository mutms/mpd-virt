# Proxmox mpd backend

Proxmox backend does not provide automatic provisioning of mpd VMs.
Developer is expected to create mpd VMs manually either using normal Debian Trixie
installer or from cloud-init images.

Once a VM is adopted, mpd-virt drives it through the Proxmox REST API — but
only for three things: VM status, start, and graceful shutdown (`mpd-virt
start/stop <NNN>`). The VM number doubles as the Proxmox VMID, and the VM's
LAN address is `NETWORK` with the last octet replaced by the number
(`10.1.10.0` + VM `150` → `10.1.10.150`) — the cloud image runs no
qemu-guest-agent, so give the VM exactly that static address in its
cloud-init IP config.

## Prepare API token and configure mpd-virt

1. Create role `mpd-virt` with permissions "VM.Audit VM.GuestAgent.Audit VM.PowerMgmt" in "Datacenter / Permissions / Roles"
2. Create a new token in "Datacenter / Permissions / API Tokens"
3. Create ~/.mpd-virt/conf/backends/proxmox.env file with the following content
```
API_URL=https://<proxmoxserverurl>:8006/api2/json/
NETWORK=<local_network_prefix>.0
TOKEN_ID=<copy_from_dialog>
TOKEN_SECRET=<copy_from_dialog>
```
`TOKEN_ID` and `TOKEN_SECRET` are the two values the token-creation dialog
shows, copied verbatim — no quotes, no angle brackets. The file holds a
secret: `chmod 600` it (mpd-virt re-asserts owner-only permissions on the
whole `~/.mpd-virt` tree on every run, but there is no reason to leave it
readable even once). Grant the token only the `mpd-virt` role from step 1,
scoped to the mpd VMs — power control and state are all it ever needs.

The API endpoint's TLS certificate must be trusted on the Mac: either serve
an mpd-CA-signed certificate on the Proxmox host, or add its CA to the System
Keychain. mpd-virt trusts the system roots plus the mpd root CA.

## Regular Debian VM installation

1. download `debian-13.X.X-amd64-netinst.iso` into `ISO images` in `local` store
2. create new `mpd-NNN` VM using NNN as VM id where NNN is 100...254
3. in "Datacenter / <node> / NNN / Permissions" and API Token permission with `mpd-virt` role
4. install Debian with SSH server enabled
5. login in your vm console and record IP address from `ip a` command
6. add your SSH key to VM via `ssh-copy-id <vm IP address>`
7. prepare VM for adoption using command `bash <(wget -qO- https://raw.githubusercontent.com/mutms/mpd/main/setup/mpd-prepare-adopt.sh)`
8. register VM in mpd-virt from macOS terminal: `mpd-virt adopt NNN <vm IP address> --backend=proxmox`

## Cloud-init Debian VM installation

1. download `debian-13-generic-amd64-202XXXXX-XXXX.qcow2` into `Import` in `local` store
  from <https://cloud.debian.org/images/cloud/trixie/> (newest dated directory)
  - **`generic`, not `genericcloud`.** genericcloud is built on Debian's cloud
    kernel, which ships no DRM drivers, so the VM has no `/dev/dri`: the text
    console works, and anything graphical (gdm, a Wayland greeter) is a black
    screen with nothing useful in any log. `generic` carries the full kernel
    and is still cloud-init driven, for about 90 MB more download.
2. create new `mpd-NNN` VM using NNN as VM id where NNN is 100...254
  - General
    - VM ID: NNN
    - Name: MPD-NNN
    - Resource Pool: mpd
  - OS
    - Do not use any media: true
  - Disks
    - delete "scsi0"
    - Import - Select image - debian-13-generic-*
  - CPU
    - Cores: 4
  - Memory
    - 12000
  - Confirm
    - press "Finish" 
3. go to "Datacenter / <node> / NNN / Hardware"
  - select "Hard disk" - press "Disk Action / Resize" - allocate more space
  - press "Add" - select "CloudInit Drive" - select local-lvm storage
4. go to "Datacenter / <node> / NNN / Cloud-Init" and set:
  - User: <your_username>
  - DNS domain: local
  - DNS server: 1.1.1.1 (or any other DNS you prefer)
  - SSH public key: <copy your SSH public key from macOS>
  - Upgrade packages: No
  - IP config: use static IP address from your local network that ends with ".NNN"
  - press "Regenerate image"
5. in "Datacenter / <node> / NNN / Permissions" and API Token permission with `mpd-virt` role
6. start the VM
7. register VM in mpd-virt from macOS terminal: `mpd-virt adopt NNN <vm IP address> --backend=proxmox`