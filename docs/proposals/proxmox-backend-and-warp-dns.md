# Proxmox backend, and warp as the resolver for `mpd.test`

Two changes that share one cause: a LAN box (`warp`, 192.168.1.102) now
exists that is authoritative for `mpd.test` and is the only router into
the Proxmox VM network. Everything mpd-virt currently writes into
`/etc/hosts` and `/etc/resolver/` on the Mac is a workaround for that box
not existing.

Status: proposed. Nothing here is implemented.

---

## 1. Drop `/etc/hosts` for LAN servers

Today `mpd-virt server list` prints hosts(5) lines and reports which are
missing from `/etc/hosts`, because `forge.mpd.test` matched no
`/etc/resolver/` file and would otherwise go to the internet.

warp answers for every LAN name, so one resolver file replaces the whole
mechanism:

```
# /etc/resolver/mpd.test
nameserver 192.168.1.102
```

macOS matches longest suffix first, so `/etc/resolver/<NNN>.mpd.test`
still wins for VM zones.

**Code impact.** `ServerAdmin.printEtcHostsReminder` goes away. If a
check is still wanted it must ask *does this name resolve*, not *is this
line in the file* — the file is no longer where the answer lives.

`Server.writeHostsFile()` stays. Its output is what gets copied to warp's
`/etc/dnsmasq-hosts.d/`, which is now the single record store; the format
was already chosen so both ends read it unchanged.

**Cost.** A static file becomes a network dependency. Away from home
`192.168.1.102` is reachable only through the Cloudflare WARP private
route. In practice this is not a regression — without that route the
addresses are unreachable anyway, so the name resolving would only defer
the failure by one step.

## 2. Drop per-VM resolver files for the Proxmox range

VM ids are bounded per backend. Blocks of 32, CIDR-aligned, so each is a
single supernet and the reserved low ids cost nothing:

| ids     | container supernet | VM address        | backend   |
|---------|--------------------|-------------------|-----------|
| 100-127 | -                  | -                 | free      |
| 128-159 | `10.163.128.0/19`  | 10.211.55.\<NNN\> | parallels |
| 160-191 | `10.163.160.0/19`  | -                 | utm       |
| 192-223 | `10.163.192.0/19`  | 10.212.56.\<NNN\> | proxmox   |
| 224-255 | `10.163.224.0/19`  | -                 | free      |

A block grows by promoting its prefix rather than renumbering — proxmox
taking 224-255 makes it `10.163.192.0/18`, which is the route already
written below, so today's route is already the grown form.

**No migration.** Parallels currently allocates from 100, but those VMs
are being recreated on 2026-07-28 before any of this lands, so the range
is free by the time it matters.

So warp can carry the whole Proxmox range as static forwarders, written
once, before any VM exists:

```
server=/192.mpd.test/10.163.192.1
…
server=/254.mpd.test/10.163.254.1
```

A forwarder whose target is not up is inert. That removes 63
`/etc/resolver/<NNN>.mpd.test` files from every client that uses warp,
and removes the privileged step from creating a Proxmox VM.

**This does not extend to Parallels or UTM**, and no carve of the id
space changes that. Aggregation works for Proxmox because all its
container subnets sit behind **one** next hop, warp. Each Parallels VM is
its own next hop at `10.211.55.<NNN>`, and each zone's resolver is
`10.163.<NNN>.1` — 32 different gateways, so neither the routes nor the
resolver files collapse. Those backends keep `Net.resolverFile(octet:)`
and the per-VM sudo prompt.

What would remove it is giving those hypervisors a warp-equivalent: a
resolver on the Mac holding one `server=` line per zone, or a router VM
per backend. Neither is worth it while Parallels VMs are few; the
supernets above leave room to try either later.

**Cost.** A query for a Proxmox zone with no VM behind it times out
rather than returning NXDOMAIN quickly.

## 3. Routing

Two aggregate routes replace the per-VM route Parallels needs. `192-254`
supernets cleanly, and `10.163.192.0/18` cannot collide with the
Parallels container subnets at `10.163.100-191`:

```sh
route -n add 10.163.192.0/18 192.168.1.102     # container subnets
route -n add 10.212.56.0/24  192.168.1.102     # VM addresses
```

Persistent via `/Library/LaunchDaemons/test.mpd.routes.plist`, RunAtLoad.
Written once; creating or deleting a Proxmox VM then needs no privileged
step on the Mac and no change on warp.

Verified 2026-07-27 from a bridged macOS VM: with both routes present,
`10.212.56.1` (warp's `ens19`) answers. A Mac behind Parallels *Shared*
needs no routes of its own — its default route reaches the host, which
NATs and routes onward — but bridged is the better test bed, since it
exercises the same path the MacBook Air uses.

**Uninstall.** `mpd-virt uninstall` should remove the LaunchDaemon along
with the resolver files it already handles.

## 4. Proxmox backend

`MpdVirt.Backend` gains `case proxmox` alongside `parallels`, `utm`,
`general`, with `canonicalSubnet = "10.212.56"`.

Provisioning is REST only — no `ssh root@kitchenbox` anywhere in the
path, because that would bypass every restriction below.

**Config** (new keys, alongside the existing per-VM registry):

* Proxmox host — `kitchenbox.mpd.test`
* API token — `mpd@pve!<id>`
* gateway — `warp.mpd.test`
* trust anchor — the root CA that signed the node's `pveproxy-ssl.pem`

The last one is not optional: a machine running mpd-virt has its own
`~/.mpd-virt/conf/caroot/`, which is a *different* root sharing the same
CN. The node's certificate will not verify against it, and the failure
looks like a Proxmox error rather than a CA mismatch.

**Create** clones template VMID 190 (`debian-13-genericcloud`, kept at a
constant id and name so refreshing the image changes nothing), sets
cloud-init user, ssh key, nameserver and static IP
`10.212.56.<NNN>/24`, then runs the existing bootstrap over SSH.

**Bootstrap fit.** `bootstrap/30-networking.sh` in the mpd repo already
targets systemd-networkd + systemd-resolved and writes
`/etc/systemd/network/05-mpd.network`, whose `05-` prefix sorts ahead of
netplan's generated `10-netplan-*.network` — `.network` files do not
merge, first match wins. `debian-13-genericcloud-amd64` ships netplan,
systemd-resolved and cloud-init, and no ifupdown, so the stack matches.

Two things it does not cover:

* `05-mpd.network` sets `DNS=${gateway}` on the assumption the
  hypervisor gateway proxies DNS. On the isolated bridge the gateway is
  warp, whose dnsmasq must therefore listen on `10.212.56.1` — it does.
* cloud-init sets the hostname from the Proxmox VM name and re-renders
  `/etc/hosts` every boot, while `30-networking.sh` renames to
  `mpd-<NNN>`. Name the Proxmox VM `mpd-<NNN>` so they agree, or set
  `preserve_hostname: true`.

**Permissions.** The token is scoped to a pool, so it cannot see, let
alone delete, the other LAN VMs:

* pool `mpd-pool` holds the storage and template 190
* `PVEVMAdmin` + `PVEDatastoreUser` on `/pool/mpd-pool` — `PVEVMAdmin`
  alone cannot create a disk, and `PVEDatastoreAdmin` would let it
  administer the storage
* `SDN.Use` on `/sdn/zones/localnetwork/vmbr1` (PVE 8.2+) — without it,
  attaching a NIC fails at create time with an unrelated-looking error
* nothing granted at `/`
* API tokens have privilege separation on by default and start with **no
  rights at all**, independent of the user's

Proxmox cannot enforce the 192-254 id range; that stays mpd-virt's
convention. Nor can ACLs cap disk usage — only separate storage does.

## Open questions

* Does `mpd-virt server list` keep a resolution check at all, or does
  `diag` absorb it?
* Should mpd-virt generate warp's 63 `server=` lines and its
  `lan-hosts`, and push both, or does warp stay hand-maintained? Pushing
  needs SSH to warp, which is a smaller credential than Proxmox root but
  is still a credential.
* Parallels and Proxmox now differ in whether creating a VM needs sudo.
  Worth surfacing in `backend list` output.
