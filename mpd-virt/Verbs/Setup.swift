// mpd-virt — `setup <NNN>` verb.
//
// Universal post-provisioning core. Idempotent. Decides between
// fix-known mode (registry entry exists → reuse stored backend/IP/user)
// and first-time adoption (no entry → requires --ip and a backend)
// entirely from registry presence — no guessing, no scanning.
//
// Order (VM-side only — every macOS-side artifact lives in diag):
//   1. SSH key auth check. ssh-keygen hint if no key; ssh-copy-id hint
//      if VM doesn't accept it yet.
//   2. CA load-or-generate (host-side; needed to push to VM).
//   3. VM-side bootstrap: scp CA cert + key, run bootstrap
//      10..50, run `mpd --vm-setup`. Registry entry is persisted from
//      inside the bootstrap's onCanonicalIPReady callback, the moment
//      the VM is at the canonical IP — before that point the VM
//      isn't really "ours".

import Foundation

extension MpdVirt.Setup {

    /// CLI entry. Dispatch is registry-presence based. Setup is a
    /// non-interactive verb — all inputs come from the CLI, the
    /// registry, or `backend.locate(...)`. No prompts. (Clone/Create
    /// are the user-friendly verbs; doctor is the diagnostic one.)
    static func runCLI(
        octet: Int,
        ipFlag: String?,
        backendFlag: String?,
        usernameFlag: String?
    ) throws {
        try validateOctet(octet)

        if MpdVirt.Registry.exists(octet: octet) {
            let entry = try MpdVirt.Registry.load(octet: octet)
            let ip = ipFlag ?? entry.ip
            let backend = try (backendFlag.map(MpdVirt.Backend.parse) ?? entry.backend)
            let user = usernameFlag ?? entry.user
            try run(
                octet: octet, ip: ip, backend: backend, username: user,
                uuid: entry.uuid, disk: entry.disk, ram: entry.ram
            )
            return
        }

        // First-time adoption. Ask the backend what it knows about
        // this NNN — `parallels` looks up `mpd-<NNN>` via prlctl,
        // `general` just trusts the --ip hint, `utm` likewise.
        let backend = try MpdVirt.resolveBackend(flag: backendFlag)
        guard let user = usernameFlag else {
            throw MpdVirt.BackendError.other("""
                no registry entry for \(MpdVirt.vmId(octet: octet)). First-time setup \
                requires --username=<dev-user-in-vm>.
                """)
        }

        guard let located = try backend.locate(octet: octet, ipHint: ipFlag) else {
            throw MpdVirt.BackendError.other("""
                no registry entry for \(MpdVirt.vmId(octet: octet)), and backend=\(backend.rawValue) \
                can't locate '\(MpdVirt.vmName(octet: octet))' on its own. \
                Pass --ip=<vm-ip>, or (for Parallels/UTM) create the VM with name \
                '\(MpdVirt.vmName(octet: octet))' and re-run.
                """)
        }
        FileHandle.standardError.write(Data(
            "  • backend=\(backend.rawValue) located \(MpdVirt.vmName(octet: octet)) at \(located.ip)\n".utf8
        ))

        try run(
            octet: octet, ip: located.ip, backend: backend, username: user,
            uuid: located.uuid, disk: nil, ram: nil
        )
    }

    /// Core entry. Also invoked by `create`/`clone` after the backend
    /// hands off a fresh VM. Non-interactive throughout.
    static func run(
        octet: Int,
        ip: String,
        backend: MpdVirt.Backend,
        username: String,
        uuid: String?,
        disk: String?,
        ram: String?
    ) throws {
        let name = MpdVirt.vmName(octet: octet)
        info("setting up \(name) (backend=\(backend.rawValue), ip=\(ip), user=\(username))")

        // Note: backend preflight belongs in Clone/Create (they're
        // about to materialize a brand-new VM and a name collision
        // is fatal). Setup is the *adoption* verb — a VM already
        // named `mpd-<NNN>` in the hypervisor is exactly what we
        // intend to consume.

        // 1. SSH key auth.
        let initialTarget = MpdVirt.Host.Ssh.Target(user: username, host: ip)
        info("checking SSH key auth against \(username)@\(ip) …")
        try MpdVirt.Host.Ssh.ensureKeyAuth(initialTarget)
        info("SSH: key auth works")

        // 2. CA — generate on first run, reuse thereafter. The root stays
        // here; what goes to the VM is a per-VM intermediate constrained
        // to this VM's zone, so the VM can sign its own certs without
        // being able to name anything outside `<NNN>.mpd.test`.
        try MpdVirt.CA.loadOrGenerate()
        info("CA: \(MpdVirt.CA.certPath)")
        try MpdVirt.CA.loadOrGenerateVMCA(octet: octet)
        info("VM CA: \(MpdVirt.CA.vmCertPath(octet: octet)) (\(MpdVirt.Net.zone(octet: octet)) only)")

        // 3. VM-side bootstrap. Skipped only when the registry already
        // exists (the VM was previously claimed) AND it's reachable at
        // the canonical IP (it's still up). Both conditions mean
        // bootstrap has already landed once; re-running it is safe but
        // slow, so we skip it on idempotent re-runs.
        //
        // The registry entry itself is written from inside Bootstrap's
        // `onCanonicalIPReady` callback — not before, not after, but
        // exactly when the VM moves to its canonical IP. That's the
        // point at which the VM is "ours"; before, it's just any old
        // box at some random IP.
        let canonicalIP = backend.canonicalIP(octet: octet)
        let canonicalTarget = MpdVirt.Host.Ssh.Target(user: username, host: canonicalIP)
        let alreadyProvisioned = MpdVirt.Registry.exists(octet: octet)
            && MpdVirt.Host.Ssh.reachable(canonicalTarget)
        if alreadyProvisioned {
            info("VM reachable at \(canonicalIP) and registry entry present — skipping bootstrap")
        } else {
            info("running VM-side bootstrap pipeline …")
            try MpdVirt.Bootstrap.RunInVM.run(
                octet: octet,
                initialIP: ip,
                canonicalIP: canonicalIP,
                username: username,
                caCertPath: MpdVirt.CA.certPath,
                vmCaCertPath: MpdVirt.CA.vmCertPath(octet: octet),
                vmCaKeyPath: MpdVirt.CA.vmKeyPath(octet: octet),
                onCanonicalIPReady: {
                    // Backend-specific post-rename work (Parallels:
                    // rename Parallels VM to mpd-<NNN> so the GUI
                    // label matches the guest hostname).
                    try backend.afterCanonicalIPReady(octet: octet, hint: uuid, user: username)

                    let entry = MpdVirt.Registry.Entry(
                        octet: octet,
                        name: name,
                        backend: backend,
                        ip: canonicalIP,
                        user: username,
                        uuid: uuid,
                        disk: disk,
                        ram: ram
                    )
                    try MpdVirt.Registry.save(entry)
                    info("registry: \(MpdVirt.vmEnvFile(octet: octet))")
                }
            )
        }

        // 4. Done. Setup is VM-side only — the SSH config block,
        // CA trust, DNS resolver and other macOS-side artifacts are
        // diag's job. SetupCmd in main.swift always follows up with
        // `diag --non-interactive`; create/clone follow up with
        // interactive `diag`.
        FileHandle.standardError.write(Data("\n✓ setup \(name) complete.\n".utf8))
    }

    // MARK: - Logging helper

    private static func info(_ msg: String) {
        FileHandle.standardError.write(Data("  • \(msg)\n".utf8))
    }
}
