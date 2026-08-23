# Proxmox mpd backend

`mpd-virt create NNN --backend=proxmox` clones a template VM (see "Template
VM" below) and adopts the clone. VMs can also be created by hand — Debian
installer or cloud-init image — and adopted.

Once a VM is adopted, mpd-virt drives it through the Proxmox REST API — but
only for three things: VM status, start, and graceful shutdown (`mpd-virt
start/stop <NNN>`). The VM number doubles as the Proxmox VMID, and the VM's
LAN address is `NETWORK` with the last octet replaced by the number
(`10.1.10.0` + VM `150` → `10.1.10.150`) — the cloud image runs no
qemu-guest-agent, so give the VM exactly that static address in its
cloud-init IP config.

## Prepare API token and configure mpd-virt

1. Create role `mpd-virt` in "Datacenter / Permissions / Roles" with
   `VM.Audit VM.Clone VM.Allocate VM.Config.Cloudinit VM.Config.Options VM.GuestAgent.Audit VM.PowerMgmt Datastore.AllocateSpace SDN.Use`
   (`create` needs clone/allocate/cloud-init/start; `Datastore.AllocateSpace`
   — not `Datastore.Allocate` — is what the clone's disk needs)
2. Create a new token in "Datacenter / Permissions / API Tokens" with
   privilege separation on
3. Put the VMs, the template and the storage in a pool (`mpd`) and grant the
   token the role on `/pool/mpd` and on the bridge
   (`/sdn/zones/localnetwork/vmbr0`), both with propagate
4. Create `~/.mpd-virt/proxmox.env` file with the following content
```
API_URL=https://<proxmoxserverurl>:8006/api2/json/
NETWORK=<local_network_prefix>.0/24
GATEWAY=<gateway>
TOKEN_ID=<copy_from_dialog>
TOKEN_SECRET=<copy_from_dialog>
TEMPLATE_VMID=999
POOL=mpd
```
`NETWORK` with `.NNN` is the VM's address; its prefix (`/24` when omitted)
and `GATEWAY` make up the clone's cloud-init IP line
(`ip=10.1.10.154/16,gw=10.1.1.1`). The template itself stays on DHCP.
`TOKEN_ID` and `TOKEN_SECRET` are the two values the token-creation dialog
shows, copied verbatim — no quotes, no angle brackets. The file holds a
secret: `chmod 600` it (mpd-virt re-asserts owner-only permissions on the
whole `~/.mpd-virt` tree on every run, but there is no reason to leave it
readable even once). Grant the token only the `mpd-virt` role from step 1,
scoped to the pool: it can then create, configure, power and delete VMs
there and nothing outside it.

The API endpoint's TLS certificate must be trusted on the Mac: either serve
an mpd-CA-signed certificate on the Proxmox host, or add its CA to the System
Keychain. mpd-virt trusts the system roots plus the mpd root CA.

## Template VM and `mpd-virt create`

`mpd-virt create NNN --backend=proxmox` does, through the API: full clone of
the template as VMID NNN named `mpd-NNN` into `POOL`; cloud-init on the
clone set to the static `NETWORK.NNN` with `GATEWAY`, your user and SSH
key, no password; start;
wait for cloud-init's first boot; then the normal adoption. A clone comes
up in about a minute because the template already carries mpd's packages.

Build the template once (VMID `TEMPLATE_VMID`, default 999):

1. create the VM as in "Cloud-init Debian VM installation" below, named
   `mpd-template`, in the pool, cloud-init: user, your SSH key(s), DNS,
   **no password**, IP config DHCP, "Upgrade packages: No". A clone keeps
   the template's keys; `create` adds the key it was given (`--pubkey`,
   default `~/.ssh/id_ed25519.pub`) when it is not among them
2. start it, SSH in and run mpd's bootstrap steps 10, 15 and 20 (sudo,
   keys-only sshd, the full package set incl. qemu-guest-agent):
   ```
   bash <(wget -qO- https://raw.githubusercontent.com/mutms/mpd/main/bootstrap/10-passwordless-sudo.sh)
   bash <(wget -qO- https://raw.githubusercontent.com/mutms/mpd/main/bootstrap/15-secure-ssh.sh)
   bash <(wget -qO- https://raw.githubusercontent.com/mutms/mpd/main/bootstrap/20-install-software.sh)
   ```
3. `sudo cloud-init clean --logs && sudo poweroff` — so every clone's first
   boot is a clean cloud-init run (new hostname, fresh host keys, your key)
4. optionally convert it to a template in the UI; `create` does a full
   clone either way

Never run `mpd --vm-setup` on the template: it would install the cloud-init
drop-in that freezes identity, and clones would keep the template's
hostname. Refresh the template by starting it and re-running step 20,
then step 3 again.

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