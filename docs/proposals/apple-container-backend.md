# Apple `container machine` as the macOS backend

macOS support currently costs a hypervisor. Parallels needs a paid Pro
licence; [`utm-backend.md`](utm-backend.md) exists to remove that
barrier, but the price of doing so is driving a GUI app over its
AppleScript dictionary — 551 lines of the least durable code in the
repo, against an interface Apple does not version.

Apple's `container` shipped a **container machine** mode at WWDC26 that
makes both unnecessary on Apple silicon.

Status: proposed. Nothing here is implemented, and nothing should be
until §7 passes.

---

## 1. Why this is possible now, and was not before

A regular `container run` boots `vminitd` — a small Swift init compiled
against musl — which execs the OCI entrypoint and offers no service
management. Had that been the only mode, this proposal would be dead on
arrival: mpd's whole in-VM design is systemd units, with dnsmasq and the
caddy frontdoor running on the VM itself at `10.163.<NNN>.1`.

A **container machine** is different. PID 1 is a thin wrapper that sets
the hostname, runs first-boot user setup, then execs **the image's own
`/sbin/init`**. systemd runs in full. Machines are persistent and
stateful — stop one, come back next week, the filesystem is as you left
it — and each gets its own address at the Mac host level.

That is a VM in every respect that matters to mpd, delivered from an OCI
image and with no hypervisor to license or drive.

**Nested containers are proven, not theoretical.** `kiac` runs
Kubernetes with `kindest/node` — systemd, containerd and kubeadm — inside
Apple containers. Each node gets its own kernel, its own cgroups and its
own IP, and the bundled kernel is monolithic with veth pairs, bridges
and iptables NAT compiled in. That is precisely the feature set podman
needs.

The framing that makes this obvious: an Apple container **is** a VM with
a real kernel, so podman inside it is podman-in-a-VM, not
podman-in-podman. The usual nesting tax — `fuse-overlayfs`,
`--privileged`, `/dev/fuse` — does not apply.

## 2. Decisions this proposal assumes

Taken 2026-08-01, and the reason the scope below is what it is:

* The container backend **replaces UTM**. [`utm-backend.md`](utm-backend.md)
  and `Backend/UTM.swift` are retired, not kept alongside.
* **Parallels stays until this works**, then is removed in a separate
  change. Existing Parallels VMs remain adoptable afterwards through
  `general`, so removing the backend does not strand them.
* **Intel Macs are not supported directly.** They are treated as VM
  takeover via `general`, which is the same path Proxmox takes.
* **Routing stays the default access model.** An SSH SOCKS proxy would
  make host routing optional and is the obvious follow-on, but it is a
  separate project and nothing here depends on it.

The last decision is the consequential one. Keeping routing means the
machine needs a *deterministic* address, and Apple's tool has no `--ip`.

## 3. Addressing

`container network create <name> --subnet <CIDR>` exists on macOS 26+,
so the subnet is ours to choose:

```sh
container network create mpd --subnet 10.212.57.0/24
```

The default subnet is also settable in `~/.config/container/config.toml`
under `[network] subnet`, which matters because it is not documented
whether `container machine create` accepts `--network` at all — if it
does not, config.toml is the way the subnet gets chosen.

There is no `--ip`. There **is** a MAC option:

```
--network <name>[,mac=XX:XX:XX:XX:XX:XX][,mtu=VALUE]
```

The address must be locally-administered unicast — the low two bits of
the first octet set to `10`. Generated MACs start with nibble `f`, so a
chosen address cannot collide with one Apple invents.

**Why one and not the other.** A MAC is a guest NIC property, set on the
virtio-net device when `container` builds the VM, entirely inside its own
control. An IP reservation is a property of the DHCP server, and that
server is macOS's `bootpd` behind `vmnet` — not `container`. There is
nothing for the tool to expose without reaching into system configuration
it does not own. Multi-network support only arrived with macOS 26 and the
tool is at 1.0, so this reads as a young feature rather than a refusal.

**The MAC is the half worth having.** Derive it from the octet, like
everything else:

```
02:6D:70:64:00:<NNN>          # 6D 70 64 = "mpd" in ASCII
```

and pin `<canonicalSubnet>.<NNN>` guest-side with systemd-networkd —
which `bootstrap/30-networking.sh` already does, unchanged, for every
other backend. No DHCP in the path at all. The MAC is then the stable
identity that lets `locate()` recognise a machine whose address moved,
and that guarantees two machines never collide.

This belongs in `MpdVirt.Net` beside `zone(octet:)` and
`containerSubnet(octet:)`, not in the backend. It is another fact derived
from the octet, and `Net.swift` is where that convention lives.

**Fallbacks**, in order, if the guest may not hold a static address:
accept the assigned one and re-read it on every verb so a changed lease
self-heals the route rather than silently breaking it; or a host-side
`bootpd` reservation keyed on our MAC (`/etc/bootptab`, subnet in
`com.apple.vmnet.plist`). The second is a last resort — privileged files
outside `container`'s control, which `container system start` may
rewrite.

## 4. The image

This is the largest piece of new work, and it is not in this repo.

`container machine` accepts any OCI image containing `/sbin/init`. The
official Debian and Ubuntu images contain no init and are **rejected
outright**, so mpd must build its own: Debian Trixie, systemd, and the
runtime stack.

That absorbs `bootstrap/40-install-software.sh` and `50-build.sh` into
image build time, which is most of what makes VM creation slow today.
`10-passwordless-sudo.sh` and `20-git-clone.sh` fold in as well.
`30-networking.sh` survives, for hostname and the static pin;
`mpd --vm-setup` stays a post-create step.

First boot provisions the user account. The default script can be
overridden with an executable `/etc/machine/create-user.sh`, which
receives `CONTAINER_UID`, `CONTAINER_USER`, `CONTAINER_HOME`,
`CONTAINER_GID` and `CONTAINER_MACHINE_ID` — enough to make the dev user
match the Mac's name and UID, which mpd already requires so `/srv`
ownership lines up.

**Note for later, not for this proposal.** Machines mount the Mac's
`$HOME` at `/Users/<username>` over VirtioFS by default. mpd currently
keeps project data in a podman volume inside the VM. Editing on the Mac
and building inside is a genuinely better workflow, but it is a change to
mpd's storage model and should be decided on its own merits.

## 5. The backend

`MpdVirt.Backend` in [`Backend/Backend.swift`](../../mpd-virt/Backend/Backend.swift)
is already the kind tag, the capability source and the dispatcher in one
enum, so this is additive: `case container`, one arm per `switch`, plus
`compiledIn`.

A new `Backend/Container.swift`, shaped like `Backend/Parallels.swift`:

| member | implementation |
|---|---|
| `canonicalSubnet` | from the §7 decision |
| `capabilities` | `create: true, clone: false, lifecycle: true` |
| `create(octet:opts:)` | `container machine create` → `Provisioned(ip:uuid:)` |
| `locate` / `describe` | parse `container machine inspect` |
| `start` / `stop` / `delete` | `container machine stop \| rm` |
| `preflight` | refuse on name collision, as Parallels does |

Clone is deliberately absent. It exists for Parallels because building a
good template by hand is expensive; creating from an image is not, so a
clone verb would only add a second way to do the same thing.

**Code impact elsewhere: none.** `Verbs/Setup.swift`, `Registry.swift`,
`Net.swift` (beyond the MAC helper), `CA.swift` and the whole
`Bootstrap/` chain should not change. If they do, logic has leaked into
the backend that belongs in the shared core — the same acceptance test
[`pluggable-backends-and-adopt.md`](pluggable-backends-and-adopt.md)
already sets, and the reason that proposal's ≤200 LOC bound is worth
keeping here.

`CloudInit.swift` is UTM-only and goes with it in §6.

## 6. Octet ranges, and retiring UTM

[`proxmox-backend-and-warp-dns.md`](proxmox-backend-and-warp-dns.md)
carves the id space by product. Carve it by **how the Mac reaches the
VM** instead — Parallels and UTM are indistinguishable from the host's
point of view, so splitting them was never buying anything:

| ids     | class                 | host reachability       |
|---------|-----------------------|-------------------------|
| 100-127 | free                  | —                       |
| 128-159 | native containers     | vmnet, one next hop     |
| 160-191 | general VMs (adopted) | per-VM on-link next hop |
| 192-223 | proxmox               | single gateway (warp)   |
| 224-255 | free                  | —                       |

Same CIDR-aligned blocks of 32, same grow-by-promoting-the-prefix
property. Only the labels change.

Once §5 passes its acceptance test: delete `Backend/UTM.swift` and
`CloudInit.swift`, drop `case utm`, delete
[`utm-backend.md`](utm-backend.md), update the index and `README.md`,
and state the Intel-Mac-via-`general` position where a reader will hit
it.

## 7. The spike this is gated on

None of the above should be built before these are answered by hand on
real hardware. `container` cannot be tested inside a macOS VM, so this
needs the bare-metal Mac.

1. Build a throwaway Debian Trixie image with systemd, `container
   machine create` it, confirm `systemctl is-system-running` answers.
2. Does `container machine create --network <name>` work? If not, set
   `[network] subnet` in `config.toml` and confirm machines land there.
3. Create a machine with `mac=02:6D:70:64:00:<NNN>` and pin
   `<subnet>.<NNN>` with systemd-networkd. **This decides §3.**
4. Install podman inside, run a container on a `10.163.<NNN>.0/24`
   bridge, confirm *native* overlayfs rather than fuse-overlayfs, and
   working iptables NAT.
5. From the Mac: `route add 10.163.<NNN>.0/24 <machine-ip>`, then reach
   dnsmasq and caddy on `10.163.<NNN>.1`.

If 3 fails but 5 still works, take the recorded-address fallback. If 5
fails outright, stop — the routing model does not survive, and this
becomes blocked on the SOCKS-proxy project instead.

**Cost.** macOS 26 and Apple silicon only. Memory is per-machine and
defaults to 50% of host memory, so several machines at once needs
`container machine set`. The M3 / `CONFIG_KVM=y` requirement in Apple's
docs applies to *nested virtualization* — VMs inside a machine — not to
podman, so an M2 is sufficient.

## Open questions

* Does `container machine create` accept `--network`? Everything in §3
  routes around this either way, but which way is not yet known.
* Is an assigned address stable across stop/start? If it is, the
  fallback in §3 is cheaper than it looks and the MAC pin may be
  unnecessary.
* Where does the image live — built locally on first `create`, or
  published to a registry? Local is simpler and has no distribution
  story; published is faster and needs one.
* Does `container machine` restore cleanly enough for `start`/`stop` to
  mean what they mean for Parallels? Third-party reports mention TCP
  connections breaking after restarting VMs from the Mac.

## Sources

* [apple/container](https://github.com/apple/container) —
  [how-to](https://github.com/apple/container/blob/main/docs/how-to.md),
  [command reference](https://github.com/apple/container/blob/main/docs/command-reference.md),
  [container machine](https://github.com/apple/container/blob/main/docs/container-machine.md)
* [kiac — Kubernetes in Apple Containers](https://blog.kubesimplify.com/introducing-kiac-kubernetes-in-apple-containers)
* [Apple Container Machine, tested](https://www.statuspal.io/blog/apple-container-machine-persistent-linux-vm-macos)
