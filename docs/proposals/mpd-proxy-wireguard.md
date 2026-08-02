# mpd-proxy: WireGuard reachability + split DNS

Status: proposal. Scope: **macOS only for now**, but every boundary is drawn so
a Linux (and later Windows) client is a new `mpd-proxy` backend, not a change to
`mpd-virt`.

## 1. Why

To use a box's `mpd.test` services the Mac has to reach that box's internal
`10.163.<NNN>.0/24` podman bridge. The current/planned approach is per-VM host
**routes** (`route add … via <next-hop>`) plus per-VM `/etc/resolver/<NNN>.mpd.test`
files — each a `sudo`. That hurts in three ways:

1. **Leases move.** Apple containers get a new vmnet IP on every stop/start, and
   Parallels VMs use dynamic DHCP. Each move means re-applying the route + ssh
   config + another `sudo` (see the `reference_apple-container-ip` headache).
2. **Generic adopted boxes live anywhere.** A taken-over box (`--backend generic`)
   may sit on a rented server or behind NAT — often **not plain-routable** to its
   `10.163.x` bridge at all, and its DNS/SNI/metadata cross an untrusted network
   **in clear** (only caddy's HTTPS body is protected).
3. **That host code is exactly the per-OS part.** Routes, `/etc/resolver`,
   LaunchDaemons — the stuff that would fork three ways for Linux/Windows.

**The insight** (arrived at by working the problem): one WireGuard interface with
cryptokey routing plus a localhost split-DNS forwarder collapses all of it into a
one-time setup and in-process per-VM state — and encrypts every remote hop as a
side effect.

## 2. The model

- **One `utun`, one WG device, N peers** — one peer per VM, each
  `AllowedIPs = 10.163.<NNN>.0/24` pointing at that VM's WG endpoint. WireGuard's
  cryptokey routing demuxes per packet by destination `/24`; the kernel never
  splits anything.
- **One aggregate route:** `route -n add -net 10.163.0.0/16 -interface utun6`.
  The kernel dumps *all* bridge traffic into the utun; WG picks the peer. A new
  VM's `/24` is already covered — no new route, ever.
- **One resolver file:** `/etc/resolver/mpd.test` → `nameserver 127.0.0.1` +
  `port 5353`. Longest-suffix match catches every `*.mpd.test`; the `port`
  directive lets the listener be **unprivileged**. Written once, never changes.
- **A conditional DNS forwarder** (in `mpd-proxy`): `<NNN>.mpd.test` → forward to
  `10.163.<NNN>.1:53` *through the tunnel*, relay the answer. It stays a
  forwarder, not authoritative, so dynamic container/runtime records keep working.

Everything user-facing is **id-keyed and stable** (`10.163.<NNN>.x`,
`<NNN>.mpd.test`). The only value that moves — the vmnet/DHCP lease — appears
*solely* as a peer `Endpoint`, rewritten in-process on restart from
`backend.ResolveIP` (`container inspect` / `prlctl`).

### Lifecycle and privilege

| When | Privileged? | What happens |
|---|---|---|
| First ever | one `sudo` | create utun, add `/16` route, write `/etc/resolver/mpd.test`, then **drop root** |
| Per VM (add/remove) | none | push WG peer + DNS-map entry over the control socket — in-process |
| Lease/restart | none | rewrite that peer's `Endpoint` — in-process |
| After reboot | one `sudo` | `mpd-proxy` recreates utun + route (resolver file persists); a LaunchDaemon removes even this |

Privsep is the OpenSSH pattern: root only for utun-create + route-add, then
`setuid` to the invoking user keeping the utun fd; peer and DNS changes need no
privilege.

### In-band vs out-of-band

- **In-band (healthy):** the tunnel → stable `10.163.<NNN>.x`, encrypted.
- **Out-of-band (broken):** the raw IP → direct SSH, tunnel bypassed, to fix the
  box that won't tunnel. `mpd-virt` keeps resolving/recording the raw IP for
  exactly this — it is both the *bootstrap* channel (takeover provisions WG over
  it) and the *repair* channel. Surfaced as `mpd-virt ssh <NNN> --direct`.

### CA trust (orthogonal; SOCKS dropped)

CA trust is a separate axis from transport. The mpd root CA is **name-constrained
to `*.mpd.test`**, so OS-wide Keychain trust is safe (it physically cannot vouch
for anything else) — trust it once, every app trusts `mpd.test`. No SOCKS proxy:
it was only the no-tunnel transport, WG supersedes it, and the name constraint
removes any reason to scope trust to one browser.

## 3. Rename `mpd-virt-macos` → `mpd-virt`

The `-macos` was premature. Grep shows the code is already ~portable: no build
tags, no `runtime.GOOS`, no `/etc/resolver`/`route`/keychain calls in Go — only
two macOS-only *external tools* (`container`, `prlctl`). Rename the repo and
module (`github.com/mutms/mpd-virt-macos/go` → `github.com/mutms/mpd-virt/go`,
matching the sibling `mpd`) so a Linux/Windows client is a natural extension, not
a fork.

## 4. New `mpd-proxy` repo + binary

A **separate repo** (`github.com/mutms/mpd-proxy`), deliberately not a `cmd/` in
`mpd-virt`, because it is the **only component that runs privileged** and should
be independently auditable and pinnable. It does *exactly*:

- create/own the `utun`, add the aggregate route (the privileged startup, then
  drops root),
- run the WireGuard engine — embedded `golang.zx2c4.com/wireguard` (userspace,
  the tool Tailscale is built on), no `wg`/`wg-quick`, nothing installed,
- run the split-DNS forwarder — embedded `github.com/miekg/dns`.

It imports **nothing** from `mpd-virt` — no CA, no SSH, no registry, no backend
adapters. Minimal code and deps as root = minimal attack surface; a compromised
`mpd-proxy` can move packets and nothing else.

**Control channel:** a local unix socket with a small versioned protocol.
Operations: `peer-add/remove/update-endpoint(pubkey, endpoint, allowedips)` and
`dns-map-set/clear(<NNN> → 10.163.<NNN>.1)`. The unprivileged `mpd-virt` is the
only client.

## 5. Changes in `mpd-virt`

- **Remove (mostly "never build"):** host routing-table and `/etc/resolver`
  handling. `mpd-virt` never touches the routing table or `/etc/` for
  reachability — that all moves to `mpd-proxy`.
- **Backend as a `--backend` flag, not an id range** (see §6): `takeover`/`create`
  take it, `mpd-virt` records it in the registry and passes it to
  `backend.ResolveIP` and the proxy client. `vmid` loses its class bands and
  becomes a plain `001-254` identifier.
- **Add `internal/proxy`:** a client for `mpd-proxy`'s control socket. On
  takeover / start / restart it calls `backend.ResolveIP` (already built), then
  pushes `peer(endpoint=<raw IP:port>, pubkey, allowedips=10.163.<NNN>.0/24)` and
  `dns-map(<NNN> → 10.163.<NNN>.1)`.
- **Keep** `backend.ResolveIP` and the registry's `MPD_VM_IP`: they now feed the
  WG `Endpoint` and the `--direct` out-of-band path, not a route.
- **Graceful when `mpd-proxy` is absent:** takeover, CA, and provisioning still
  work; only reachability isn't wired. `mpd-proxy` is optional infrastructure.

## 6. No id ranges — backend is an attribute

The band-per-backend scheme (`128-159 parallels`, `192-223 proxmox`, …) existed
*only* to answer "how does the Mac reach this box." WireGuard makes reaching
**uniform** — one aggregate route, one DNS forwarder, both keyed purely on
`<NNN>` — so the bands carry no information any more and are **removed**.

Instead the backend is stated explicitly at create/takeover and recorded:

```
mpd-virt takeover 130 --backend parallels
mpd-virt takeover 007 --backend generic --ip 203.0.113.9
```

Backends: `generic` (supplied IP), `parallels` (`prlctl`), `container`
(`container inspect`), `proxmox` (derives `10.212.56.<NNN>`), and `native` — the
client host's *own* hypervisor, polymorphic by the client's `GOOS`: KVM/libvirt
on Linux, Hyper-V on Windows, Apple Virtualization.framework on macOS if it ever
ships a CLI. The value is stored in the registry (`MPD_VM_BACKEND`, replacing
`MPD_VM_CLASS`) and read by `backend.ResolveIP` and the create/start/stop verbs.

The id becomes a **plain opaque identifier** — any `001-254`, any backend. Its
only remaining constraints are structural, not backend-related: it is the subnet
octet (`10.163.<NNN>`) and the zone (`<NNN>.mpd.test`), so it must be a valid
octet and globally unique (the registry enforces uniqueness — re-using an id
collides). Demos are just `mpd-001`, `mpd-002`, … whatever the backend.

This deletes the class bands from `vmid` (leaving only `001-254` validation +
zero-padding) and supersedes both the range table in `apple-container-backend.md`
and the band-carving in the id-bands work — of which **`backend.ResolveIP`
survives unchanged**, it simply takes the backend as an argument instead of
deriving it from the id.

## 7. WireGuard endpoint in mpd VMs — apt + systemd, not podman

The in-VM WG endpoint is a **first-class system service**: installed with
`apt-get install wireguard-tools`, configured as a `wg-quick@`/networkd unit, and
brought up at boot. **Not** a podman container.

Rationale: WG is the box's reachability substrate. Running it *inside* podman
would be both fragile and circular — it would depend on podman being up (the very
thing you may be debugging), and you'd need WG to reach the box that runs the
podman that runs the WG. A kernel/systemd WG comes up early, independent of
podman, and survives podman breakage — which is exactly when you need the tunnel
(or its raw-IP fallback) most.

Provisioned by mpd's `vm-setup` (sibling repo), alongside dnsmasq and caddy:
generate the box's keypair, authorize `mpd-proxy`'s public key, set the peer's
`AllowedIPs` for the Mac side, and listen on a port on the box's reachable
interface (vmnet/eth0/LAN). Takeover then hands `mpd-proxy` the endpoint. *(This
is the one piece that lands in `mpd`, not `mpd-virt`.)*

## 8. Portability (macOS now, adaptable later)

The macOS implementation uses `wireguard-go`'s darwin `tun`, `/sbin/route`,
`/etc/resolver`, and a LaunchDaemon. The per-OS surface is small and lives
**entirely in `mpd-proxy`** behind a `HostIntegration` seam (`create-tun`,
`add-route`, `install-resolver`). Linux swaps in netlink + `systemd-resolved`;
Windows swaps in Wintun + NRPT. `mpd-virt` never changes — it only ever speaks
the control-socket protocol. So a Linux client = a new `mpd-proxy` backend +
enabling the KVM class, nothing more.

## 9. Rough order of work (~a week)

1. Rename repo/module to `mpd-virt`.
2. `mpd-proxy` skeleton: utun + `/16` route + WG engine + split-DNS + privsep drop + control socket.
3. `mpd-virt internal/proxy` client; wire into takeover + lifecycle verbs; drop the (unbuilt) route/resolver plans; add `ssh --direct`.
4. `mpd` `vm-setup`: apt WireGuard endpoint, keyed to `mpd-proxy`.
5. Drop id ranges from `vmid` (plain `001-254` id); add the `--backend` flag + `MPD_VM_BACKEND` (backends: generic/parallels/container/proxmox/native).
6. LaunchDaemon for zero-sudo-after-install.

## 10. Verification

On mbair: `sudo mpd-proxy up` (utun + route + resolver, then drops root) →
`mpd-virt takeover 181` pushes peer + DNS → browse `https://181.mpd.test`
transparently, all apps, encrypted → restart the container → `mpd-virt` rewrites
the peer endpoint in-process, browsing still works with **no sudo** → kill the
in-VM WG and confirm `mpd-virt ssh 181 --direct` still gets in over the raw IP.
