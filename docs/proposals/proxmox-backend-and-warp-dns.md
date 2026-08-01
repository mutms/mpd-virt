# Proxmox backend, and warp as the resolver for `mpd.test`

Two changes that share one cause: a LAN box (`warp`, 192.168.1.102) now
exists that is authoritative for `mpd.test` and is the only router into
the Proxmox VM network. Everything mpd-virt currently writes into
`/etc/hosts` and `/etc/resolver/` on the Mac is a workaround for that box
not existing.

Status: proposed. Nothing here is implemented. Reframed 2026-08-01: the
automated backend is deferred behind manual creation plus takeover (§4),
and the id blocks are now cut by reachability class rather than by
hypervisor product (§2).

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

VM ids are bounded per class. Blocks of 32, CIDR-aligned, so each is a
single supernet and the reserved low ids cost nothing.

The blocks are cut by **how the Mac reaches the VM**, not by which
product made it — Parallels, UTM and an adopted Proxmox-less VM are
indistinguishable from the host's point of view, so splitting them was
never buying anything:

| ids     | container supernet | VM address        | class                 | host reachability       |
|---------|--------------------|-------------------|-----------------------|-------------------------|
| 100-127 | -                  | -                 | free                  | —                       |
| 128-159 | `10.163.128.0/19`  | 10.211.55.\<NNN\> | general VMs (adopted) | per-VM on-link next hop |
| 160-191 | `10.163.160.0/19`  | -                 | native containers     | vmnet, one next hop     |
| 192-223 | `10.163.192.0/19`  | 10.212.56.\<NNN\> | proxmox               | single gateway (warp)   |
| 224-255 | `10.163.224.0/19`  | -                 | free                  | —                       |

General VMs take the lower block so existing hand-made VMs — `mpd-130`
among them — keep their ids and need no renumbering. Native containers
are the class with no installed base, so they are the ones that can be
assigned freely. See
[`apple-container-backend.md`](apple-container-backend.md) §6.

A block grows by promoting its prefix rather than renumbering — proxmox
taking 224-255 makes it `10.163.192.0/18`, which is the route already
written below, so today's route is already the grown form.

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

**This does not extend to the other two classes**, and no carve of the id
space changes that. Aggregation works for Proxmox because all its
container subnets sit behind **one** next hop, warp. Each general VM is
its own next hop at `10.211.55.<NNN>`, and each zone's resolver is
`10.163.<NNN>.1` — 32 different gateways, so neither the routes nor the
resolver files collapse. Those classes keep `Net.resolverFile(octet:)`
and the per-VM sudo prompt.

Native containers sit between the two: every machine is behind the same
vmnet next hop, but that address is assigned rather than chosen, so the
routes cannot be written before the machines exist. Which of the two
shapes that block ends up taking depends on the addressing decision in
[`apple-container-backend.md`](apple-container-backend.md) §3.

The sudo prompt still goes away, just by pre-creating rather than
aggregating. A block is 32 ids, so all of it can be written once, before
any VM exists — a general-VM next hop at `10.211.55.<NNN>` is on-link, so
the route installs and simply drops packets until that VM is up, and an
unused `/etc/resolver/<NNN>.mpd.test` is only reached by a query nobody
makes.

macOS routes do not persist, so the durable form is a LaunchDaemon.

Shape: one script per class, `~/.mpd-virt/conf/general-setup.sh`,
`container-setup.sh`, `proxmox-setup.sh`, which the user reads and runs
with sudo. That is the entire privileged step for that class, and
enabling a class you do not use costs nothing. Each is self-contained —
resolver contents inline, not copied from sibling files — so reviewing it
means reading one file rather than thirty-five. It writes, for its own
block only:

* `/etc/resolver/<NNN>.mpd.test` for every id in the block
* `/usr/local/libexec/mpd-routes-<class>.sh` — its routes
* `/Library/LaunchDaemons/test.mpd.routes.<class>.plist` — RunAtLoad,
  invoking that script, so the routes survive a reboot
* a DNS cache flush

Idempotent, regenerated whenever that class's block changes;
`general-setup.sh --remove` is what `uninstall` runs. Each written file
carries a marker comment so removal never touches a hand-written one.

`proxmox-setup.sh` is much smaller, because warp is both that backend's
resolver and its single next hop: one `/etc/resolver/mpd.test` pointing
at `192.168.1.102` rather than 32 per-VM files, and two aggregate routes
rather than 32. LAN server names resolve as a side effect of the same
file, which is what makes section 1 possible.

This extends the existing `Host/SudoRecipe.swift` pattern — print it,
offer to run it — from a few inline commands to a reviewable file.
Afterwards create and delete are unprivileged for every backend.

A warp-equivalent for those hypervisors — a resolver on the Mac with one
`server=` line per zone, or a router VM per backend — would collapse the
files rather than pre-create them. Not worth it at this size; the free
blocks leave room to try it later.

**Cost.** A query for a Proxmox zone with no VM behind it times out
rather than returning NXDOMAIN quickly.

## 3. Routing

Two aggregate routes replace the per-VM route a general VM needs.
`192-254` supernets cleanly, and `10.163.192.0/18` cannot collide with
the other classes' container subnets at `10.163.100-191`:

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

## 4. Proxmox by takeover — the version to build

**There is no Proxmox backend in the first version.** Creating a VM by
hand and taking it over is enough, and it needs no API token, no pool, no
ACLs and no code in this repo. §5 keeps the automated backend on record
for when VM creation becomes frequent enough to be worth it.

A VM joins by hand:

1. **Fill in the Proxmox cloud-init panel.** It already asks for exactly
   what the pre-takeover state requires — user, SSH public key, DNS,
   static IP `10.212.56.<NNN>/24` — and the VM name supplies the
   hostname, so name it `mpd-<NNN>`. Nothing else to seed, and in
   particular no certificates: the per-VM intermediate is pushed later by
   `setup`, from the root CA that never leaves the Mac.
2. Run the tweak script in the VM, in one command fetched from GitHub.
   This is **mandatory and happens before takeover** — it is what makes
   the VM match the definition of an mpd environment. Same script, same
   contract, for every taken-over VM regardless of hypervisor. See
   [`apple-container-backend.md`](apple-container-backend.md) §4.
3. `mpd-virt setup <NNN> --backend=proxmox`, which *verifies* the VM
   conforms, pushes the CA, and writes the registry entry. No `--ip`:
   the address is `10.212.56.<NNN>`, derived from the octet like every
   other mpd fact.

Step 1 is the entire hypervisor-side job, and it is a form. That is the
argument in §5 for probably never automating it: an API that automates
filling in a form is worth building only when you fill it in often.

Steps 1 and 2 can collapse into one if wanted, by having cloud-init run
the tweak script itself. Note that Proxmox's *UI* cloud-init panel covers
identity and networking but not `runcmd` — custom user-data needs a
snippet passed with `--cicustom`. So fully hands-off costs a snippet
file; otherwise step 2 is one SSH command after first boot.

For a VM that is not cloud-init, step 1 becomes `ssh-copy-id` plus
setting the hostname and address by hand; steps 2 and 3 are unchanged.

### `proxmox` is a thin backend case, not `general` with documentation

`MpdVirt.Backend` gains `case proxmox` **now**, with no API client
behind it:

```swift
case proxmox:
    canonicalSubnet = "10.212.56"
    capabilities    = (create: false, clone: false, lifecycle: false)
```

The reason is addressing. A `general` VM lives at whatever `--ip` the
user passes; a Proxmox VM lives at `10.212.56.<NNN>`, derived from the
octet. So `locate(octet:ipHint:)` computes the address instead of
demanding it, the 192-223 range becomes enforceable rather than a
convention, and `proxmox-setup.sh` versus `general-setup.sh` map onto
backend cases instead of loose naming.

Roughly twenty lines. What varies between these two classes is not how
the VM is taken over — that is identical — but whether the host can
compute where it is.

## 5. Automated provisioning — recorded, probably not building

This section is research, not a queued task. Automating creation is only
worth it at volume: many VMs, CI creating and destroying them, or other
people onboarding without access to the Proxmox UI. None of that is the
current situation, and two things argue against it even later.

**All it automates is a form.** The tweak script already handles the part
that is actually hard — making the VM conform. Filling in the cloud-init
panel is the residue.

**It creates a credential that otherwise need not exist.** A pool, a
user, an API token with write access on the hypervisor: something to
store, rotate and eventually revoke. That cuts against the posture the
rest of mpd takes — the root CA never leaves the Mac, VM certs are
zone-constrained intermediates, LAN certs are scoped. Not creating a
standing credential beats scoping one well.

What follows is written down so whoever eventually wants it does not have
to rediscover the permissions model or the `SDN.Use` trap.

Provisioning would be REST only — no `ssh root@kitchenbox` anywhere in
the path, because that would bypass every restriction below.

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
`10.212.56.<NNN>/24`, then runs the tweak script over SSH and hands to
`setup` — the same two steps §4 does by hand. All this backend automates
is the form.

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
* The reachability classes differ in whether creating a VM needs sudo.
  Worth surfacing in `backend list` output.
* Does the tweak script need a Proxmox-specific branch at all? §5's
  "bootstrap fit" notes below — netplan ordering, cloud-init re-rendering
  `/etc/hosts` — are properties of `debian-13-genericcloud`, not of
  Proxmox. If the script handles them generally, there is nothing
  Proxmox-shaped left in the takeover path.
