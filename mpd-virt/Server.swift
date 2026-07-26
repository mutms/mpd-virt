// mpd-virt — MpdVirt.Server namespace: LAN machines that are not mpd VMs.
//
// A "server" here is a real host on the local network that gets a name in
// the `mpd.test` tree — `proxmox.mpd.test`, `forge.mpd.test`,
// `runner.mpd.test`. mpd-virt does not manage those machines and knows
// nothing about what runs on them; it issues their certificates, remembers
// their addresses, and publishes their names into every VM's resolver so
// containers can reach them over verified TLS. What to install where is a
// property of the machine, not of mpd-virt — it belongs in that machine's
// own documentation.
//
// ── Why a registry rather than a flat hosts file ───────────────────────
// Every operation needs more than one fact about the same machine:
// issuing needs the name, deploying needs the address, and diagnosing a
// broken lookup needs both, compared against what `/etc/hosts` says. Three
// files would drift; one directory per server does not.
//
// Layout mirrors the per-VM registry (`Registry.swift`) deliberately — the
// same `KEY=VALUE` env file, the same atomic write, the same
// "directory exists + env inside" definition of known:
//
//   ~/.mpd-virt/servers/forge/
//   ├── env        — MPD_SERVER_{NAME,IP}
//   ├── cert.pem   — leaf, signed directly by the root on this Mac
//   ├── key.pem    — 0600
//   └── sans       — the SAN list, so a re-issue reproduces it
//
// ── Why signed directly by the root ───────────────────────────────────
// VMs get a name-constrained intermediate because their signing key has
// to live on a machine we do not trust. These certificates are signed here
// on the Mac, where the root key already is, so an intermediate would add
// a chain hop and one more file to install on each LAN host while
// protecting a key that never moves.
//
// ── The naming rule this depends on ───────────────────────────────────
// A first label that is a 3-digit number belongs to a VM zone
// (`126.mpd.test`); anything else under `mpd.test` is a LAN service name.
// That is what lets both live in one tree without a registry of
// reservations — see `Net.isVMZoneLabel`.

import Foundation

extension MpdVirt.Server {

    /// One registered LAN server.
    struct Entry {
        /// Bare label: `forge`. The DNS name is derived, never stored
        /// twice — see `host`.
        let name: String
        let ip: String

        /// `forge.mpd.test`.
        var host: String { MpdVirt.Net.serviceHost(name) }
    }

    // MARK: - Paths

    /// `~/.mpd-virt/servers/`.
    static var serversDir: String { "\(MpdVirt.rootDir)/servers" }

    static func dir(_ name: String) -> String { "\(serversDir)/\(name)" }
    static func envFile(_ name: String) -> String { "\(dir(name))/env" }
    static func certPath(_ name: String) -> String { "\(dir(name))/cert.pem" }
    static func keyPath(_ name: String) -> String { "\(dir(name))/key.pem" }
    static func sansPath(_ name: String) -> String { "\(dir(name))/sans" }

    /// The rendered hosts file pushed into VMs.
    static var lanHostsFile: String { "\(MpdVirt.confDir)/lan-hosts" }

    /// Where that file lands inside a VM. The in-VM `mpd` reads it during
    /// `--vm-setup` and republishes it through dnsmasq.
    static let remoteLanHostsPath = "/var/lib/mpd/conf/lan-hosts"

    // MARK: - Read

    /// Names of every registered server, sorted. A directory without an
    /// `env` file inside is not registered — same rule as the VM registry.
    static func known() throws -> [String] {
        let fm = FileManager.default
        guard fm.fileExists(atPath: serversDir) else { return [] }
        return try fm.contentsOfDirectory(atPath: serversDir)
            .filter { fm.fileExists(atPath: envFile($0)) }
            .sorted()
    }

    static func exists(_ name: String) -> Bool {
        FileManager.default.fileExists(atPath: envFile(name))
    }

    static func load(_ name: String) throws -> Entry {
        let path = envFile(name)
        guard FileManager.default.fileExists(atPath: path) else {
            throw Error.notKnown(name: name)
        }
        let raw = try String(contentsOfFile: path, encoding: .utf8)
        var kv: [String: String] = [:]
        for line in raw.split(whereSeparator: { $0 == "\n" }) {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty, !trimmed.hasPrefix("#") else { continue }
            let parts = trimmed.split(separator: "=", maxSplits: 1, omittingEmptySubsequences: false)
            guard parts.count == 2 else { continue }
            kv[parts[0].trimmingCharacters(in: .whitespaces)] =
                parts[1].trimmingCharacters(in: .whitespaces)
        }
        func required(_ key: String) throws -> String {
            guard let v = kv[key], !v.isEmpty else {
                throw Error.malformed(name: name, missingKey: key)
            }
            return v
        }
        return Entry(
            name: try required("MPD_SERVER_NAME"),
            ip: try required("MPD_SERVER_IP")
        )
    }

    /// Every entry, skipping (and reporting) any that fails to parse
    /// rather than aborting the whole listing.
    static func loadAll() throws -> [Entry] {
        try known().compactMap { name in
            do { return try load(name) } catch {
                FileHandle.standardError.write(Data(
                    "warning: skipping server '\(name)' — \(error)\n".utf8
                ))
                return nil
            }
        }
    }

    // MARK: - Write

    static func save(_ entry: Entry) throws {
        try FileManager.default.createDirectory(
            atPath: dir(entry.name), withIntermediateDirectories: true
        )
        var body = """
            # mpd-virt registry entry for \(entry.host).
            # Managed by `mpd-virt server add`. Edit at your own risk.
            MPD_SERVER_NAME=\(entry.name)
            MPD_SERVER_IP=\(entry.ip)
            """
        body += "\n"
        try body.write(toFile: envFile(entry.name), atomically: true, encoding: .utf8)
    }

    /// Remove a server and everything issued for it. The certificate goes
    /// too: keeping key material for a machine we no longer track is how
    /// an unaccounted-for private key ends up on disk.
    static func remove(_ name: String) throws {
        let path = dir(name)
        guard FileManager.default.fileExists(atPath: path) else { return }
        try FileManager.default.removeItem(atPath: path)
    }

    // MARK: - Validation

    /// Normalise `forge` or `forge.mpd.test` to the bare label `forge`,
    /// rejecting anything that cannot be a LAN service name.
    ///
    /// Each rejection is a distinct message because each has a different
    /// fix, and "invalid name" tells the user nothing about which rule
    /// they hit.
    static func normalise(_ input: String) throws -> String {
        var label = input.trimmingCharacters(in: .whitespaces).lowercased()
        let suffix = ".\(MpdVirt.Net.rootDomain)"

        if label.hasSuffix(suffix) {
            label = String(label.dropLast(suffix.count))
        } else if label.contains(".") {
            // A dotted name that isn't under mpd.test would be rejected by
            // the root's name constraint anyway; say so now rather than
            // letting a browser say it later.
            throw Error.outsideRootDomain(name: input)
        }

        guard !label.isEmpty else { throw Error.emptyName }

        // A deeper name would sit under something else's zone, and mpd-virt
        // would have no way to know whose.
        guard !label.contains(".") else {
            throw Error.notASingleLabel(name: input)
        }
        guard !MpdVirt.Net.isVMZoneLabel(label) else {
            throw Error.collidesWithVMZone(label: label)
        }
        // Hostname charset. Enforced because these end up as DNS names and
        // as SANs; openssl would accept far more than a resolver will.
        let allowed = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyz0123456789-")
        guard label.unicodeScalars.allSatisfy({ allowed.contains($0) }),
              !label.hasPrefix("-"), !label.hasSuffix("-")
        else {
            throw Error.badLabel(label: label)
        }
        return label
    }

    /// Accept an IPv4 or IPv6 literal. Anything else — a hostname, a CIDR,
    /// a typo — is refused here rather than written into a hosts file
    /// where it would silently answer nothing.
    static func validateIP(_ ip: String) throws {
        var v4 = in_addr()
        var v6 = in6_addr()
        let ok = ip.withCString { inet_pton(AF_INET, $0, &v4) == 1 }
            || ip.withCString { inet_pton(AF_INET6, $0, &v6) == 1 }
        guard ok else { throw Error.badIP(ip: ip) }
    }

    // MARK: - Hosts rendering

    /// The hosts(5) body for every registered server.
    ///
    /// hosts format rather than something bespoke for one reason: it can be
    /// pasted into `/etc/hosts` verbatim, and it is exactly what dnsmasq's
    /// `hostsdir=` reads at the other end. One format, no conversion at
    /// either boundary.
    static func hostsBody() throws -> String {
        var lines = [
            "# mpd-virt LAN service records.",
            "# Generated by `mpd-virt server`; edits are overwritten.",
        ]
        for entry in try loadAll() {
            // Address first, then the name it answers for — hosts(5) order.
            lines.append("\(entry.ip)\t\(entry.host)")
        }
        return lines.joined(separator: "\n") + "\n"
    }

    /// Write the rendered hosts file to `conf/lan-hosts`, the artifact
    /// `server sync` and `setup` push into VMs.
    @discardableResult
    static func writeHostsFile() throws -> String {
        try FileManager.default.createDirectory(
            atPath: MpdVirt.confDir, withIntermediateDirectories: true
        )
        try hostsBody().write(toFile: lanHostsFile, atomically: true, encoding: .utf8)
        return lanHostsFile
    }

    // MARK: - Errors

    enum Error: Swift.Error, CustomStringConvertible {
        case notKnown(name: String)
        case malformed(name: String, missingKey: String)
        case emptyName
        case outsideRootDomain(name: String)
        case notASingleLabel(name: String)
        case collidesWithVMZone(label: String)
        case badLabel(label: String)
        case badIP(ip: String)
        case alreadyExists(name: String)

        var description: String {
            switch self {
            case .notKnown(let name):
                return "no server named '\(name)' — add it with `mpd-virt server add \(name) --ip <addr>`."
            case .malformed(let name, let key):
                return "server '\(name)' is missing required key '\(key)' in \(MpdVirt.Server.envFile(name))."
            case .emptyName:
                return "empty server name — expected something like `forge` or `forge.\(MpdVirt.Net.rootDomain)`."
            case .outsideRootDomain(let name):
                return """
                    '\(name)' is not under \(MpdVirt.Net.rootDomain).
                    The mpd root CA is name-constrained to \(MpdVirt.Net.rootDomain), so a certificate
                    for this name could not be issued — and would not verify if it were.
                    """
            case .notASingleLabel(let name):
                return """
                    '\(name)' has more than one label under \(MpdVirt.Net.rootDomain).
                    LAN services take a single label (forge.mpd.test). Extra names belong
                    on the certificate as `--san`, not as separate registry entries.
                    """
            case .collidesWithVMZone(let label):
                return """
                    '\(label)' is a 3-digit number, which names a VM zone (\(label).\(MpdVirt.Net.rootDomain)).
                    That zone belongs to VM \(label) and is signed by that VM's own CA;
                    a LAN record here would shadow it. Pick a non-numeric name.
                    """
            case .badLabel(let label):
                return "'\(label)' is not a valid hostname label (use a-z, 0-9 and '-', not leading or trailing '-')."
            case .badIP(let ip):
                return "'\(ip)' is not an IPv4 or IPv6 address."
            case .alreadyExists(let name):
                return "server '\(name)' already exists — remove it first, or edit \(MpdVirt.Server.envFile(name))."
            }
        }
    }
}
