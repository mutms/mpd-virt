// mpd-virt — `server` verbs: LAN machines with names under mpd.test.
//
//   mpd-virt server add    <name> --ip <addr>
//   mpd-virt server list   [--etc-hosts]
//   mpd-virt server delete <name>            (alias: rm)
//   mpd-virt server cert   <name> [--san <extra>]… [--force]
//   mpd-virt server sync   [<NNN> | --all]
//
// The registry model lives in `Server.swift`; this file is the user
// interface to it. `docs/LAN_SERVERS.md` explains why LAN services get
// root-signed leaves while VMs get intermediates.
//
// There is deliberately no `deploy` verb and no notion of what software a
// server runs. Where a certificate goes, and what to restart afterwards,
// differs per machine and changes over time; encoding it here means a
// second, staler copy of each machine's own runbook.

import Foundation

extension MpdVirt.ServerAdmin {

    private static func ok(_ s: String) { MpdVirt.Ui.ok(s) }
    private static func info(_ s: String) { MpdVirt.Ui.info(s) }
    private static func warn(_ s: String) { MpdVirt.Ui.warn(s) }

    /// Warn at the same threshold `doctor`/`diag` use for the root, so a
    /// dev sees one expiry convention rather than several.
    private static let expiryWarningDays = 30

    // MARK: - add

    static func add(name rawName: String, ip: String) throws {
        let name = try MpdVirt.Server.normalise(rawName)
        try MpdVirt.Server.validateIP(ip)
        guard !MpdVirt.Server.exists(name) else {
            throw MpdVirt.Server.Error.alreadyExists(name: name)
        }

        let entry = MpdVirt.Server.Entry(name: name, ip: ip)
        try MpdVirt.Server.save(entry)
        try MpdVirt.Server.writeHostsFile()

        MpdVirt.Ui.header("server \(entry.host)")
        ok("registered \(entry.host) → \(entry.ip)")
        info("registry: \(MpdVirt.Ui.path(MpdVirt.Server.envFile(name)))")
        print("")
        MpdVirt.Ui.indent("Next:")
        MpdVirt.Ui.indent("  mpd-virt server cert \(name)   # issue its TLS certificate, if it serves any")
        MpdVirt.Ui.indent("  mpd-virt server sync --all    # publish the name inside every VM")
        print("")
        try printEtcHostsReminder()
    }

    // MARK: - list

    static func list(etcHosts: Bool) throws {
        let entries = try MpdVirt.Server.loadAll()

        // --etc-hosts is the paste-ready form and nothing else, so it can
        // be piped: `mpd-virt server list --etc-hosts | sudo tee -a /etc/hosts`.
        if etcHosts {
            print(try MpdVirt.Server.hostsBody(), terminator: "")
            return
        }

        guard !entries.isEmpty else {
            MpdVirt.Ui.header("LAN servers")
            info("none registered — add one with `mpd-virt server add <name> --ip <addr>`")
            return
        }

        MpdVirt.Ui.header("LAN servers")
        let nameWidth = max(4, entries.map(\.host.count).max() ?? 4)
        let ipWidth = max(7, entries.map(\.ip.count).max() ?? 7)
        MpdVirt.Ui.indent(
            "NAME".padded(nameWidth) + "  " + "ADDRESS".padded(ipWidth)
            + "  CERTIFICATE"
        )
        for e in entries {
            MpdVirt.Ui.indent(
                e.host.padded(nameWidth) + "  " + e.ip.padded(ipWidth)
                + "  " + certStatus(e.name)
            )
        }
        print("")
        try printEtcHostsReminder()
    }

    /// Human-readable state of a server's certificate.
    private static func certStatus(_ name: String) -> String {
        let path = MpdVirt.Server.certPath(name)
        guard FileManager.default.fileExists(atPath: path) else {
            // Not a defect. A host that only dials out never presents a
            // certificate, so nagging about it forever would be wrong.
            return "none (only needed if it serves TLS)"
        }
        guard let days = try? MpdVirt.CA.daysUntilExpiry(of: path) else {
            return "unreadable"
        }
        if days < 0 { return "EXPIRED \(-days)d ago — re-issue" }
        if days <= expiryWarningDays { return "expires in \(days)d — re-issue soon" }
        return "valid, \(days)d left"
    }

    // MARK: - delete

    static func delete(name rawName: String, assumeYes: Bool) throws {
        let name = try MpdVirt.Server.normalise(rawName)
        let entry = try MpdVirt.Server.load(name)

        MpdVirt.Ui.header("remove \(entry.host)")
        info("this deletes \(MpdVirt.Ui.path(MpdVirt.Server.dir(name)))/, including its private key")
        guard MpdVirt.Ui.confirm("Remove \(entry.host)?", assumeYes: assumeYes) else {
            info("aborted")
            return
        }
        try MpdVirt.Server.remove(name)
        try MpdVirt.Server.writeHostsFile()
        ok("removed \(entry.host)")
        warn("the certificate stays installed on \(entry.ip) until you remove it there")
        info("VMs keep answering for this name until `mpd-virt server sync --all`")
    }

    // MARK: - cert

    static func cert(name rawName: String, extraSans: [String],
                     force: Bool) throws {
        let name = try MpdVirt.Server.normalise(rawName)
        let entry = try MpdVirt.Server.load(name)

        // Every extra SAN goes through the same rules as the primary name:
        // the root would refuse to have signed it otherwise.
        var sans = [entry.host]
        for raw in extraSans {
            let label = try MpdVirt.Server.normalise(raw)
            let host = MpdVirt.Net.serviceHost(label)
            if !sans.contains(host) { sans.append(host) }
        }

        let certPath = MpdVirt.Server.certPath(name)
        if FileManager.default.fileExists(atPath: certPath), !force {
            let days = (try? MpdVirt.CA.daysUntilExpiry(of: certPath)) ?? -1
            if days > expiryWarningDays {
                MpdVirt.Ui.header("certificate for \(entry.host)")
                info("current certificate is valid for another \(days) day(s) — nothing to do")
                info("re-issue anyway with --force")
                return
            }
        }

        MpdVirt.Ui.header("certificate for \(entry.host)")
        try MpdVirt.CA.issueLeaf(
            sans: sans,
            certPath: certPath,
            keyPath: MpdVirt.Server.keyPath(name)
        )
        let signature = sans.map { "DNS:\($0)" }
        try signature.joined(separator: "\n").appending("\n")
            .write(toFile: MpdVirt.Server.sansPath(name), atomically: true, encoding: .utf8)

        let days = (try? MpdVirt.CA.daysUntilExpiry(of: certPath)) ?? 0
        ok("issued for \(signature.joined(separator: ", "))  (\(days) days)")
        info("cert: \(MpdVirt.Ui.path(certPath))")
        info("key:  \(MpdVirt.Ui.path(MpdVirt.Server.keyPath(name)))")
    }

    // MARK: - sync

    /// Push the rendered hosts file into VMs and have each republish it.
    ///
    /// A VM that is down is reported, not fatal: the records are static
    /// facts about the LAN, and the next `setup`/`update` on that VM picks
    /// them up.
    static func sync(octet: Int?) throws {
        let path = try MpdVirt.Server.writeHostsFile()
        let entries = try MpdVirt.Server.loadAll()

        MpdVirt.Ui.header("publishing \(entries.count) LAN record(s) into VMs")
        for e in entries { info("\(e.host) → \(e.ip)") }

        let octets = try octet.map { [$0] } ?? MpdVirt.Registry.knownOctets()
        guard !octets.isEmpty else {
            warn("no VMs registered — nothing to publish to")
            return
        }

        for o in octets {
            MpdVirt.Ui.section("mpd-\(MpdVirt.vmId(octet: o))")
            do {
                let entry = try MpdVirt.Registry.load(octet: o)
                let target = MpdVirt.Host.Ssh.Target(user: entry.user, host: entry.ip)
                guard MpdVirt.Host.Ssh.reachable(target) else {
                    warn("not reachable at \(entry.ip) — skipped (next `setup` will push it)")
                    continue
                }
                try MpdVirt.Host.Ssh.put(
                    target,
                    localPath: path,
                    remotePath: MpdVirt.Server.remoteLanHostsPath,
                    mode: 0o644
                )
                _ = try MpdVirt.Host.Ssh.stream(target, "mpd --vm-setup >/dev/null")
                ok("published (\(MpdVirt.Server.remoteLanHostsPath))")
            } catch {
                warn("\(error)")
            }
        }
    }

    /// Push the rendered hosts file to one VM, without reconciling.
    ///
    /// Used by `setup`, which runs `mpd --vm-setup` afterwards anyway —
    /// so the records go live as part of the setup that was happening
    /// regardless, and a freshly provisioned VM knows the LAN names
    /// without anyone remembering to run `server sync`.
    ///
    /// Always pushes, even with no servers registered: the file is then
    /// empty of records, which is what retracts names after the last
    /// `server delete`.
    static func pushHosts(to target: MpdVirt.Host.Ssh.Target) throws {
        let path = try MpdVirt.Server.writeHostsFile()
        try MpdVirt.Host.Ssh.put(
            target,
            localPath: path,
            remotePath: MpdVirt.Server.remoteLanHostsPath,
            mode: 0o644
        )
    }

    // MARK: - ca export

    /// Write the root CA's public certificate somewhere it can be copied
    /// to a LAN host. Default stdout, so it pipes.
    static func caExport(path: String?) throws {
        try MpdVirt.CA.loadOrGenerate()
        let pem = try String(contentsOfFile: MpdVirt.CA.certPath, encoding: .utf8)
        guard let path else {
            print(pem, terminator: "")
            return
        }
        try pem.write(toFile: path, atomically: true, encoding: .utf8)
        MpdVirt.Ui.ok("wrote \(MpdVirt.Ui.path(path))")
    }

    // MARK: - /etc/hosts on this Mac

    /// Report which records this Mac is missing.
    ///
    /// mpd-virt does not write `/etc/hosts`: it needs sudo, it is a file
    /// other tools also edit, and an ownership marker in it would need its
    /// own uninstall path. `/etc/resolver/` cannot help either — those
    /// files are per-VM-zone and `forge.mpd.test` matches none of them, so
    /// the lookup goes to the system resolver, which asks the internet
    /// about a reserved TLD. `/etc/hosts` is consulted first, which is why
    /// hand-editing works and is the right answer here.
    private static func printEtcHostsReminder() throws {
        let entries = try MpdVirt.Server.loadAll()
        guard !entries.isEmpty else { return }

        let live = (try? String(contentsOfFile: "/etc/hosts", encoding: .utf8)) ?? ""
        let missing = entries.filter { e in
            !live.split(separator: "\n").contains { line in
                let f = line.split(whereSeparator: \.isWhitespace).map(String.init)
                guard !line.trimmingCharacters(in: .whitespaces).hasPrefix("#"),
                      f.count >= 2 else { return false }
                return f[0] == e.ip && f.dropFirst().contains(e.host)
            }
        }
        guard !missing.isEmpty else {
            ok("/etc/hosts on this Mac already resolves every registered server")
            return
        }

        warn("this Mac cannot resolve \(missing.count) of these names yet")
        MpdVirt.Ui.indent("Add to /etc/hosts (mpd-virt won't edit it for you):")
        print("")
        for e in missing { print("\(e.ip)\t\(e.host)") }
        print("")
        MpdVirt.Ui.indent("    sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder")
        print("")
    }
}

/// Left-pad to a column width. Table rendering only.
private extension String {
    func padded(_ width: Int) -> String {
        count >= width ? self : self + String(repeating: " ", count: width - count)
    }
}
