// mpd-virt — Local root CA for *.mpd.test.
//
// One CA per Mac, generated on first `mpd-virt setup`, persisted under
// ~/.mpd-virt/conf/caroot/. **Name-constrained** to the `mpd.test` DNS
// tree so even if the trust store entry is ever abused it can only sign
// certs for *.mpd.test — never google.com, never anything outside the
// dev tree. This is the property that lets us put the CA in the macOS
// System Keychain without it being a real security risk.
//
// Implementation shells out to `/usr/bin/openssl` (LibreSSL on macOS).
// Reasons:
//   - Apple ships it. Zero install footprint.
//   - X.509 name-constraints + custom extensions are a one-liner in
//     openssl-conf. Reimplementing ASN.1 encoding in pure Swift is
//     possible but a lot of code for no real benefit.
//   - The cert never gets validated by openssl — it just needs to be
//     a well-formed X.509 the Security framework + the in-VM trust
//     stores accept. LibreSSL produces that.
//
// File layout under `caRootDir`:
//   rootCA.pem      — public cert. Pushed to VMs, trusted in System Keychain.
//   rootCA-key.pem  — private key. NEVER leaves the Mac.
//   rootCA.srl      — serial counter, so no two issued certs collide.
//
// "NEVER leaves the Mac" is literal, and it is why there is a second CA
// here. Each VM gets its own intermediate under `~/.mpd-virt/<NNN>/ca/`,
// signed by the root and name-constrained to that VM's zone alone:
//
//   mpd Root CA                         (key: this Mac, and only this Mac)
//   └── mpd VM 200 CA                   (key: pushed to VM 200)
//         permitted;DNS:200.mpd.test
//         └── 200.mpd.test, moodle.200.mpd.test, …   signed inside the VM
//
// The VM signs its own service and project certificates, as it always
// did, but with a CA that cannot name anything outside its own zone. A
// compromised VM can therefore forge `*.200.mpd.test` and nothing else —
// not another VM's zone, and not the names this Mac issues directly for
// LAN services under `mpd.test`. See `generateVMCA`.

import Foundation

extension MpdVirt.CA {

    /// Friendly subject name. Used as the cert's CN and as the lookup
    /// key for `security delete-certificate -c "..."` in uninstall.
    static let commonName = "mpd Root CA"

    /// Subject DN. OpenSSL formats it lazily from these components.
    private static let subject = "/CN=\(commonName)/OU=mpd-virt/O=mpd local development"

    /// Validity. 365 days — capped because macOS's trust evaluator
    /// rejects user-installed roots with long validity windows (Apple
    /// has progressively tightened cert-lifetime policy; even when the
    /// root itself isn't formally capped, longer windows trip warnings
    /// or outright rejection in Safari / `security verify-cert`). The
    /// practical implication is **annual rotation**: re-run
    /// `mpd-virt setup` (or, once implemented, `mpd-virt refresh-trust`)
    /// every ~11 months to regenerate + redistribute the CA.
    ///
    /// `mpd-virt doctor` will eventually warn when the on-disk CA is
    /// within 30 days of expiry so the rotation isn't a surprise.
    private static let validityDays = 365

    /// Path to the persisted public cert (PEM).
    static var certPath: String { "\(MpdVirt.caRootDir)/rootCA.pem" }

    /// Path to the persisted private key (PEM). Mode 0600.
    static var keyPath: String { "\(MpdVirt.caRootDir)/rootCA-key.pem" }

    /// Are both files present?
    static var exists: Bool {
        let fm = FileManager.default
        return fm.fileExists(atPath: certPath) && fm.fileExists(atPath: keyPath)
    }

    /// Return the on-disk CA, generating + persisting one if absent.
    /// Idempotent: repeat calls return the same files.
    static func loadOrGenerate() throws {
        if exists { return }
        try generate()
    }

    /// Force-generate a new CA. Overwrites any existing files. Used by
    /// `setup` only via loadOrGenerate; exposed mainly for future
    /// `refresh-trust`.
    static func generate() throws {
        let fm = FileManager.default
        try fm.createDirectory(
            atPath: MpdVirt.caRootDir, withIntermediateDirectories: true
        )

        // openssl-conf with NameConstraints. `permitted;DNS:mpd.test`
        // covers both `mpd.test` itself and all `*.mpd.test` subdomains.
        let confBody = """
            [ req ]
            distinguished_name = req_dn
            x509_extensions    = v3_ca
            prompt             = no

            [ req_dn ]
            CN = \(commonName)
            OU = mpd-virt
            O  = mpd local development

            [ v3_ca ]
            subjectKeyIdentifier   = hash
            authorityKeyIdentifier = keyid:always
            basicConstraints       = critical, CA:TRUE
            keyUsage               = critical, keyCertSign, cRLSign
            nameConstraints        = critical, permitted;DNS:mpd.test
            """

        // Write the openssl conf to a temp file so we don't leave it
        // lying around alongside the CA material.
        let confURL = try writeTempFile(named: "mpd-virt-ca.cnf", body: confBody)
        defer { try? fm.removeItem(at: confURL) }

        // openssl req invocation. -newkey rsa:4096 generates the
        // keypair in the same call as the self-sign, eliminating an
        // intermediate keyfile-on-disk step.
        let argv: [String] = [
            "/usr/bin/openssl", "req",
            "-x509",
            "-newkey", "rsa:4096",
            "-sha256",
            "-days", String(validityDays),
            "-nodes",                       // unencrypted private key
            "-keyout", keyPath,
            "-out",    certPath,
            "-subj",   subject,
            "-extensions", "v3_ca",
            "-config", confURL.path,
        ]

        let r = try MpdVirt.Host.Ssh.runProcess(argv: argv)
        guard r.ok else {
            // Clean up partial files so a retry starts clean.
            try? fm.removeItem(atPath: certPath)
            try? fm.removeItem(atPath: keyPath)
            throw MpdVirt.BackendError.other("""
                CA generation failed (openssl exit \(r.exitCode)):
                \(r.stderr.trimmingCharacters(in: .whitespacesAndNewlines))
                """)
        }

        // Tighten permissions on the private key.
        try fm.setAttributes([.posixPermissions: 0o600], ofItemAtPath: keyPath)
        try fm.setAttributes([.posixPermissions: 0o644], ofItemAtPath: certPath)
    }

    // MARK: - Per-VM signing CA

    /// Longest an intermediate may live. 397 days is the macOS leaf
    /// ceiling; the intermediate is not a leaf, but nothing signed by it
    /// may outlive it, so keeping the two in step avoids a CA that is
    /// alive while every certificate under it has expired.
    private static let maxIntermediateDays = 397

    /// Per-VM CA directory: `~/.mpd-virt/<NNN>/ca/`.
    ///
    /// Under the per-VM directory rather than `conf/`, so
    /// `Registry.remove(octet:)` takes it with the VM. That is the
    /// intended lifetime: the certificates this CA signed all named a VM
    /// that no longer exists, and a re-created VM at the same octet gets
    /// a fresh CA and fresh leaves.
    static func vmCaDir(octet: Int) -> String { "\(MpdVirt.vmDir(octet: octet))/ca" }

    /// This VM's signing certificate — public, pushed to the VM, and
    /// served alongside every leaf the VM signs.
    static func vmCertPath(octet: Int) -> String { "\(vmCaDir(octet: octet))/vmCA.pem" }

    /// This VM's signing key. Mode 0600 here and on the VM. Unlike the
    /// root key, this one is *meant* to travel — that is the point.
    static func vmKeyPath(octet: Int) -> String { "\(vmCaDir(octet: octet))/vmCA-key.pem" }

    /// Are both halves of this VM's CA present?
    static func vmExists(octet: Int) -> Bool {
        let fm = FileManager.default
        return fm.fileExists(atPath: vmCertPath(octet: octet))
            && fm.fileExists(atPath: vmKeyPath(octet: octet))
    }

    /// Return this VM's signing CA, generating + persisting one if absent.
    /// Idempotent, like `loadOrGenerate`.
    static func loadOrGenerateVMCA(octet: Int) throws {
        if vmExists(octet: octet) { return }
        try generateVMCA(octet: octet)
    }

    /// Force-generate this VM's signing CA, overwriting any existing
    /// files. Signed by the root, name-constrained to this VM's zone.
    ///
    /// The constraint is the entire point of the exercise. VM 200's CA
    /// carries `permitted;DNS:200.mpd.test`, so a leaf it signs for
    /// `201.mpd.test` — or for `forge.mpd.test` — is rejected by every
    /// verifier that implements RFC 5280, which includes the macOS
    /// Security framework and OpenSSL/LibreSSL. A rooted VM can therefore
    /// mint certificates for its own names and for nothing else, which is
    /// what makes it safe to hand `*.mpd.test` names to real machines on
    /// the LAN.
    static func generateVMCA(octet: Int) throws {
        let fm = FileManager.default
        try loadOrGenerate()
        try assertRootCanSignIntermediates()

        // Nothing may outlive its issuer: a certificate valid past the
        // date its CA expires simply stops verifying, on a day nobody
        // wrote down. Cap to whatever the root has left.
        let rootDaysLeft = try daysUntilExpiry()
        guard rootDaysLeft > 0 else {
            throw MpdVirt.BackendError.other("""
                The mpd root CA at \(certPath) expired \(-rootDaysLeft) day(s) ago.
                Regenerate and re-trust it before setting up a VM.
                """)
        }
        let days = min(maxIntermediateDays, rootDaysLeft)

        try fm.createDirectory(
            atPath: vmCaDir(octet: octet), withIntermediateDirectories: true
        )
        try fm.setAttributes(
            [.posixPermissions: 0o700], ofItemAtPath: vmCaDir(octet: octet)
        )

        let zone = MpdVirt.Net.zone(octet: octet)
        let subject =
            "/CN=mpd VM \(MpdVirt.vmId(octet: octet)) CA/OU=mpd-virt/O=mpd local development"

        // pathlen:0 — this CA signs leaves and never another CA.
        let extBody = """
            [ v3_intermediate ]
            subjectKeyIdentifier   = hash
            authorityKeyIdentifier = keyid:always,issuer
            basicConstraints       = critical, CA:TRUE, pathlen:0
            keyUsage               = critical, keyCertSign, cRLSign
            nameConstraints        = critical, permitted;DNS:\(zone)
            """
        let extURL = try writeTempFile(named: "mpd-virt-vmca.cnf", body: extBody)
        let csrURL = FileManager.default.temporaryDirectory
            .appendingPathComponent("mpd-virt-vmca-\(UUID().uuidString).csr")
        defer {
            try? fm.removeItem(at: extURL)
            try? fm.removeItem(at: csrURL)
        }

        // Two calls rather than one: the root signs this, so it cannot be
        // a self-signing `req -x509` the way the root itself was.
        let csr = try MpdVirt.Host.Ssh.runProcess(argv: [
            "/usr/bin/openssl", "req",
            "-new",
            "-newkey", "rsa:4096",
            "-nodes",
            "-keyout", vmKeyPath(octet: octet),
            "-out", csrURL.path,
            "-subj", subject,
        ])
        guard csr.ok else {
            try? fm.removeItem(atPath: vmKeyPath(octet: octet))
            throw MpdVirt.BackendError.other("""
                VM CA request failed (openssl exit \(csr.exitCode)):
                \(csr.stderr.trimmingCharacters(in: .whitespacesAndNewlines))
                """)
        }

        // The serial file lives beside the root, which is the one place
        // that sees every certificate the root has ever signed — keeping
        // it there is what stops two VMs being issued the same serial.
        let sign = try MpdVirt.Host.Ssh.runProcess(argv: [
            "/usr/bin/openssl", "x509",
            "-req",
            "-in", csrURL.path,
            "-CA", certPath,
            "-CAkey", keyPath,
            "-CAserial", "\(MpdVirt.caRootDir)/rootCA.srl",
            "-CAcreateserial",
            "-sha256",
            "-days", String(days),
            "-out", vmCertPath(octet: octet),
            "-extfile", extURL.path,
            "-extensions", "v3_intermediate",
        ])
        guard sign.ok else {
            try? fm.removeItem(atPath: vmKeyPath(octet: octet))
            try? fm.removeItem(atPath: vmCertPath(octet: octet))
            throw MpdVirt.BackendError.other("""
                VM CA signing failed (openssl exit \(sign.exitCode)):
                \(sign.stderr.trimmingCharacters(in: .whitespacesAndNewlines))
                """)
        }

        try fm.setAttributes(
            [.posixPermissions: 0o600], ofItemAtPath: vmKeyPath(octet: octet)
        )
        try fm.setAttributes(
            [.posixPermissions: 0o644], ofItemAtPath: vmCertPath(octet: octet)
        )
    }

    /// Refuse to sign an intermediate under a root that forbids one.
    ///
    /// A root carrying `pathlen:0` can sign leaves and nothing else, so
    /// the intermediate would be issued happily by openssl and then fail
    /// to verify in every client — a confusing failure that shows up as
    /// broken HTTPS long after the cause. mpd-virt's own roots have never
    /// set pathlen, but `mpd`'s in-VM generator does (`cert/ca.go`), and
    /// an adopted VM can bring one with it.
    private static func assertRootCanSignIntermediates() throws {
        let r = try MpdVirt.Host.Ssh.runProcess(argv: [
            "/usr/bin/openssl", "x509", "-in", certPath, "-noout", "-text",
        ])
        guard r.ok else {
            throw MpdVirt.BackendError.other(
                "openssl could not read the root CA at \(certPath): \(r.stderr)"
            )
        }
        // LibreSSL renders the extension as `CA:TRUE, pathlen:0`.
        guard !r.stdout.contains("pathlen:0") else {
            throw MpdVirt.BackendError.other("""
                The root CA at \(certPath) carries a pathlen:0 basic constraint,
                so it cannot sign the per-VM intermediate mpd-virt issues. This
                root was generated by an older in-VM `mpd` (cert/ca.go) rather
                than by mpd-virt.

                Regenerate the root and re-trust it:

                    mpd-virt uninstall     # removes the CA + keychain entry
                    mpd-virt setup <NNN>   # generates a fresh, unconstrained root
                """)
        }
    }

    // MARK: - Leaf certificates for LAN services

    /// Longest a leaf may live. macOS rejects leaf certificates valid for
    /// 398 days or more, so a longer one would be untrusted on the very
    /// Mac that issued it.
    private static let maxLeafDays = 397

    /// Sign a leaf for a LAN service, directly with the root.
    ///
    /// Directly rather than via an intermediate because this signing
    /// happens here, on the Mac, where the root key already lives. The
    /// per-VM intermediates exist to keep a signing key off a machine we
    /// do not control; that reasoning does not apply to a certificate
    /// mpd-virt hands to the user on a USB-stick basis.
    ///
    /// `sans` is the full SAN list; the first entry becomes the CN.
    static func issueLeaf(sans: [String], certPath: String, keyPath: String) throws {
        guard let cn = sans.first else {
            throw MpdVirt.BackendError.other("issueLeaf: no SANs given")
        }
        let fm = FileManager.default
        try loadOrGenerate()

        // Same rule as the per-VM CA: nothing outlives its issuer.
        let rootDaysLeft = try daysUntilExpiry()
        guard rootDaysLeft > 0 else {
            throw MpdVirt.BackendError.other("""
                The mpd root CA at \(certPath) expired \(-rootDaysLeft) day(s) ago.
                Regenerate and re-trust it before issuing certificates.
                """)
        }
        let days = min(maxLeafDays, rootDaysLeft)

        try fm.createDirectory(
            atPath: (certPath as NSString).deletingLastPathComponent,
            withIntermediateDirectories: true
        )

        // DNS SANs only. The root constrains dNSName, but under RFC 5280 a
        // name type absent from permittedSubtrees is *unconstrained* — so
        // an IP SAN would be the one field of this certificate the name
        // constraint does not cover.
        let sanList = sans.map { "DNS:\($0)" }.joined(separator: ", ")
        let extBody = """
            [ v3_leaf ]
            subjectKeyIdentifier   = hash
            authorityKeyIdentifier = keyid,issuer
            basicConstraints       = critical, CA:FALSE
            keyUsage               = critical, digitalSignature, keyEncipherment
            extendedKeyUsage       = serverAuth
            subjectAltName         = \(sanList)
            """
        let extURL = try writeTempFile(named: "mpd-virt-leaf.cnf", body: extBody)
        let csrURL = fm.temporaryDirectory
            .appendingPathComponent("mpd-virt-leaf-\(UUID().uuidString).csr")
        defer {
            try? fm.removeItem(at: extURL)
            try? fm.removeItem(at: csrURL)
        }

        let csr = try MpdVirt.Host.Ssh.runProcess(argv: [
            "/usr/bin/openssl", "req",
            "-new",
            "-newkey", "rsa:2048",
            "-nodes",
            "-keyout", keyPath,
            "-out", csrURL.path,
            "-subj", "/CN=\(cn)",
        ])
        guard csr.ok else {
            try? fm.removeItem(atPath: keyPath)
            throw MpdVirt.BackendError.other("""
                Certificate request failed (openssl exit \(csr.exitCode)):
                \(csr.stderr.trimmingCharacters(in: .whitespacesAndNewlines))
                """)
        }

        let sign = try MpdVirt.Host.Ssh.runProcess(argv: [
            "/usr/bin/openssl", "x509",
            "-req",
            "-in", csrURL.path,
            "-CA", self.certPath,
            "-CAkey", self.keyPath,
            "-CAserial", "\(MpdVirt.caRootDir)/rootCA.srl",
            "-CAcreateserial",
            "-sha256",
            "-days", String(days),
            "-out", certPath,
            "-extfile", extURL.path,
            "-extensions", "v3_leaf",
        ])
        guard sign.ok else {
            try? fm.removeItem(atPath: keyPath)
            try? fm.removeItem(atPath: certPath)
            throw MpdVirt.BackendError.other("""
                Certificate signing failed (openssl exit \(sign.exitCode)):
                \(sign.stderr.trimmingCharacters(in: .whitespacesAndNewlines))
                """)
        }

        try fm.setAttributes([.posixPermissions: 0o600], ofItemAtPath: keyPath)
        try fm.setAttributes([.posixPermissions: 0o644], ofItemAtPath: certPath)
    }

    /// Days until an arbitrary certificate expires. Negative once past.
    static func daysUntilExpiry(of path: String) throws -> Int {
        guard FileManager.default.fileExists(atPath: path) else {
            throw MpdVirt.BackendError.other("certificate missing: \(path)")
        }
        let r = try MpdVirt.Host.Ssh.runProcess(argv: [
            "/usr/bin/openssl", "x509", "-in", path, "-noout", "-enddate",
        ])
        guard r.ok else {
            throw MpdVirt.BackendError.other("openssl could not read \(path): \(r.stderr)")
        }
        let line = r.stdout.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let eq = line.firstIndex(of: "=") else {
            throw MpdVirt.BackendError.other("unexpected openssl output: \(line)")
        }
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = TimeZone(identifier: "GMT")
        formatter.dateFormat = "MMM d HH:mm:ss yyyy zzz"
        guard let notAfter = formatter.date(from: String(line[line.index(after: eq)...])) else {
            throw MpdVirt.BackendError.other("could not parse cert expiry from '\(line)'")
        }
        return Int(notAfter.timeIntervalSinceNow / 86_400)
    }

    // MARK: - Expiry

    /// Days remaining until the on-disk CA expires. Negative if already
    /// expired. Throws if the cert is missing or unreadable by openssl.
    /// Used by `doctor` to warn at the 30-day threshold (the macOS
    /// 1-year-cap discussion in CA.swift's validityDays comment).
    static func daysUntilExpiry() throws -> Int {
        guard FileManager.default.fileExists(atPath: certPath) else {
            throw MpdVirt.BackendError.other("CA missing: \(certPath)")
        }
        let r = try MpdVirt.Host.Ssh.runProcess(argv: [
            "/usr/bin/openssl", "x509", "-in", certPath, "-noout", "-enddate",
        ])
        guard r.ok else {
            throw MpdVirt.BackendError.other("openssl could not read \(certPath): \(r.stderr)")
        }
        // Output shape: `notAfter=May 24 09:21:03 2027 GMT`
        let line = r.stdout.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let eq = line.firstIndex(of: "=") else {
            throw MpdVirt.BackendError.other("unexpected openssl output: \(line)")
        }
        let dateString = String(line[line.index(after: eq)...])
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = TimeZone(identifier: "GMT")
        // openssl's default format: `MMM d HH:mm:ss yyyy zzz`
        formatter.dateFormat = "MMM d HH:mm:ss yyyy zzz"
        guard let notAfter = formatter.date(from: dateString) else {
            throw MpdVirt.BackendError.other("could not parse cert expiry '\(dateString)'")
        }
        let interval = notAfter.timeIntervalSinceNow
        return Int(interval / 86_400)
    }

    // MARK: - Helpers

    private static func writeTempFile(named: String, body: String) throws -> URL {
        let dir = FileManager.default.temporaryDirectory
        let url = dir.appendingPathComponent("\(named).\(UUID().uuidString)")
        try body.write(to: url, atomically: true, encoding: .utf8)
        return url
    }
}
