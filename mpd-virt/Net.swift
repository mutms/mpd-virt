// mpd-virt — MpdVirt.Net namespace.
//
// The in-VM container network, as seen from the Mac. Reaching a VM's
// containers means one static route plus one scoped resolver file — no
// tunnel involved.
//
// ── Per-VM addressing ──────────────────────────────────────────────────
// Every fact here is a function of the VM's octet, because every fact in
// the VM is too. VM 222 serves `10.163.222.0/24` with its resolver on
// `10.163.222.1` and owns the DNS zone `222.mpd.test`; VM 150 serves
// `10.163.150.0/24` / `10.163.150.1` / `150.mpd.test`.
//
// That is what lets several VMs be reachable at once. The Mac holds one
// route and one `/etc/resolver/<id>.mpd.test` file per VM, and they do
// not collide: the routes are to disjoint /24s, and macOS resolver(5)
// picks by longest suffix match, so each zone file governs only its own
// VM.
//
// The in-VM half of this lives in `Mpd.Net` (mpd repo, `mpd/Net.swift`).
// The two must agree; they are separate repos, so a change here is a
// change there.
//
// Mirrors the layout mpd uses inside the VM: the host part of an address
// never moves, only the third octet varies, and it always equals the VM
// ID.
//
// Everything this Mac talks to is at `.1`, the podman bridge gateway —
// which is the VM itself, not a container. mpd runs the resolver and the
// TLS frontdoor there as systemd units. They used to be containers on
// `.3` and `.4`; both moved onto the VM, and `.3`/`.4`/`.5` are now
// unassigned. A Mac still pointing at `.3` gets no answer at all: the
// address is simply unused, so lookups fail rather than resolve wrongly.

import Foundation

extension MpdVirt.Net {

    // MARK: - Fixed facts

    /// The DNS root mpd owns, shared by every VM. It is what the CA is
    /// name-constrained to (`permitted;DNS:mpd.test`), which is why one
    /// CA covers every VM's zone without change. Per-VM names hang below
    /// it — see `zone(octet:)`.
    static let rootDomain = "mpd.test"

    /// First two octets of the container address space. `10.163.0.0/16`
    /// is reserved by mpd in aggregate; each VM takes one /24 inside it.
    static let subnetPrefix = "10.163"

    /// Host octets with a fixed meaning inside every VM's /24.
    enum Host {
        /// The podman bridge gateway — the VM itself. Everything mpd
        /// serves from the VM rather than from a container answers here:
        /// dnsmasq on :53, and caddy on :443 fronting the status page and
        /// adminer. One address, so the Mac needs one route and one
        /// resolver entry.
        static let gateway = 1
    }

    // MARK: - Who owns which name

    /// Is this first label a VM zone rather than a LAN service name?
    ///
    /// `mpd.test` holds two kinds of thing: VM zones (`126.mpd.test`) and
    /// names for real machines on the LAN (`forge.mpd.test`). The rule that
    /// keeps them apart is positional — a 3-digit first label is a VM, and
    /// everything else is not — which is what lets both live in one tree
    /// with no registry of reservations to consult.
    ///
    /// It follows from `vmId(octet:)` formatting every octet to three
    /// digits, so this is the inverse of that, not a second convention.
    /// `managedOctetRange` is deliberately *not* checked: `099.mpd.test`
    /// is not a VM mpd-virt would create, but it still reads as a VM zone,
    /// and a LAN service that looked like one would be a trap.
    static func isVMZoneLabel(_ label: String) -> Bool {
        label.count == 3 && label.allSatisfy(\.isNumber)
    }

    /// A LAN service's name: `forge` → `forge.mpd.test`.
    ///
    /// These sit directly under the root, beside the VM zones rather than
    /// inside one, because they belong to machines no VM owns. Their
    /// certificates are signed by the root on the Mac — a VM's
    /// zone-constrained CA could not issue them even if it tried.
    static func serviceHost(_ name: String) -> String {
        "\(name).\(rootDomain)"
    }

    // MARK: - Per-VM facts

    /// This VM's DNS zone: `150.mpd.test`. Also the zone apex, which
    /// resolves to the portal — so `https://150.mpd.test/` is the
    /// browser entry point for VM 150.
    static func zone(octet: Int) -> String {
        "\(MpdVirt.vmId(octet: octet)).\(rootDomain)"
    }

    /// This VM's container subnet in CIDR form: `10.163.150.0/24`.
    /// The destination of the Mac's static route.
    static func containerSubnet(octet: Int) -> String {
        "\(subnetPrefix).\(octet).0/24"
    }

    /// Compose an address on this VM's subnet from its host octet:
    /// `ip(octet: 150, host: Host.gateway)` → `10.163.150.1`.
    static func ip(octet: Int, host: Int) -> String {
        "\(subnetPrefix).\(octet).\(host)"
    }

    /// This VM, as seen from its own container subnet: the bridge
    /// gateway. It is the nameserver the scoped resolver file points at,
    /// the address the zone apex resolves to, and the only address on
    /// this subnet the Mac ever needs to talk to directly.
    ///
    /// One function rather than a `dns` and a `portal` accessor, because
    /// there is genuinely one address now — separate names would only
    /// suggest they could differ.
    static func gateway(octet: Int) -> String {
        ip(octet: octet, host: Host.gateway)
    }

    /// The scoped resolver file for this VM: `/etc/resolver/150.mpd.test`.
    ///
    /// The filename *is* the match domain. macOS resolver(5) selects by
    /// longest suffix, so per-VM files never conflict with each other —
    /// that property is what makes concurrent VMs work, and it is why the
    /// file is named for the zone rather than for the root domain.
    static func resolverFile(octet: Int) -> String {
        "/etc/resolver/\(zone(octet: octet))"
    }

    /// A runtime's name in this VM's zone: `php.runtime.150.mpd.test`.
    static func runtimeHost(_ runtime: String, octet: Int) -> String {
        "\(runtime).runtime.\(zone(octet: octet))"
    }

    /// `vm.service.<zone>` — the diagnostic record mpd's resolver serves,
    /// answering with the VM's own LAN IP rather than an address on the
    /// container subnet. Used by `diag` to confirm the route lands where
    /// intended.
    static func vmServiceRecord(octet: Int) -> String {
        "vm.service.\(zone(octet: octet))"
    }

    // MARK: - Legacy

    /// The pre-per-VM-addressing resolver file, from when every VM shared
    /// one flat `mpd.test` zone. Nothing writes it any more; `uninstall`
    /// still offers to remove it so an upgraded Mac doesn't keep sending
    /// bare `*.mpd.test` lookups to an address that no longer answers.
    static let legacyResolverFile = "/etc/resolver/\(rootDomain)"

    /// The subnet every VM used to share. Only referenced when cleaning
    /// up a stale route left over from that era.
    static let legacySubnet = "\(subnetPrefix).0.0/24"
}
