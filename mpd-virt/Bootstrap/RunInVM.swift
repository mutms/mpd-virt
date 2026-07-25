// mpd-virt — Bootstrap orchestration over SSH.
//
// Drives the mpd/bootstrap/{10..60}-*.sh pipeline against a Debian
// Trixie VM. The bootstrap scripts themselves live in the sibling
// `mpd` repo and are idempotent; this file just sequences them via
// SSH, pushes the host-generated CA material into the VM at the
// right moments, and handles the SSH-drop-and-reconnect dance that
// `30-networking.sh` triggers when it pins the static IP.
//
// Flow against a freshly-prepared VM (whatever its initial IP):
//
//   on initial-ip:
//     10-passwordless-sudo.sh   — wget'd; interactive (-t)
//     20-git-clone.sh           — wget'd; clones to /opt/mpd
//     push CA cert              → /var/lib/mpd/conf/caroot/rootCA.pem
//     30-networking.sh <NNN>    — local; SSH session drops here
//
//   on new-ip (10.211.55.<NNN>):
//     waitUntilReachable
//     40-install-software.sh    — local
//     50-build.sh               — local
//     mpd --vm-setup            — initializes the in-VM mpd platform
//
// Output of each step is streamed live to the user's terminal.

import Foundation

extension MpdVirt.Bootstrap.RunInVM {

    /// GitHub raw URL the wgettable bootstrap steps fetch themselves
    /// from. The branch is `main`; if we ever ship release tags this
    /// becomes a settable knob.
    static let bootstrapBaseURL = "https://raw.githubusercontent.com/mutms/mpd/main/bootstrap"

    /// Errors that the orchestrator surfaces. Each carries the step
    /// title so the user sees which one blew up.
    enum Failure: Error, CustomStringConvertible {
        case stepFailed(title: String, exitCode: Int32)
        case scpFailed(title: String, underlying: Error)
        case reconnectTimeout(ip: String)

        var description: String {
            switch self {
            case .stepFailed(let title, let code):
                return "bootstrap step '\(title)' failed (exit \(code))."
            case .scpFailed(let title, let err):
                return "bootstrap push '\(title)' failed: \(err)"
            case .reconnectTimeout(let ip):
                return "VM never came back up at \(ip) after the static-IP pin. Check the Parallels console."
            }
        }
    }

    // MARK: - Entry point

    /// Run the full bootstrap pipeline against a VM.
    /// - Parameters:
    ///   - octet: canonical NNN (becomes the VM's hostname + static IP suffix).
    ///   - initialIP: the IP we currently reach the VM at.
    ///   - canonicalIP: the IP the VM moves to after `30-networking.sh`
    ///     pins it. Backend-dependent (Parallels=`10.211.55.<NNN>`,
    ///     UTM=`192.168.64.<NNN>`); resolved by the caller via
    ///     `Backend.canonicalIP(octet:)`.
    ///   - username: dev user inside the VM (must already have SSH key
    ///     trust on the host's id_ed25519 / agent).
    ///   - caCertPath: local path to the root CA public cert — the trust
    ///     anchor. Public material; the root's private key stays on the Mac.
    ///   - vmCaCertPath: local path to this VM's signing certificate.
    ///   - vmCaKeyPath: local path to this VM's signing key. This one is
    ///     private and does travel — it is name-constrained to this VM's
    ///     zone, which is the point.
    static func run(
        octet: Int,
        initialIP: String,
        canonicalIP: String,
        username: String,
        caCertPath: String,
        vmCaCertPath: String,
        vmCaKeyPath: String,
        /// Fires the instant the VM is confirmed reachable at the
        /// canonical IP. This is the "point of no return" — the VM has
        /// the right hostname, the right IP, and is committed to being
        /// mpd-<NNN>. Setup uses this hook to persist the registry
        /// entry; before this point the VM isn't really "ours" yet.
        onCanonicalIPReady: () throws -> Void
    ) throws {
        let initialTarget = MpdVirt.Host.Ssh.Target(user: username, host: initialIP)
        let postRenameIP = canonicalIP
        let postRenameTarget = MpdVirt.Host.Ssh.Target(user: username, host: postRenameIP)

        // --- Phase A: at initial IP, before the rename ---

        step(1, "SSH key auth @ \(initialIP)")
        try MpdVirt.Host.Ssh.ensureKeyAuth(initialTarget)

        // mpd-virt's ssh runs without a local PTY (Process() doesn't
        // hand over the controlling terminal). That makes any remote
        // prompt — like `su -c` asking for the root password —
        // **echo in plaintext**, because the remote pty can't push
        // noecho state back to a local pty that doesn't exist.
        //
        // Bootstrap step 10 prompts only when `sudo -n true` fails on
        // the VM. Probe that first: if sudo is already passwordless,
        // step 10 is a no-op and we can run it safely. Otherwise we
        // refuse and tell the dev to run step 10 themselves in their
        // shell (where their bash gives ssh a real PTY and noecho
        // works as designed).
        try ensurePasswordlessSudo(initialTarget)

        try runRemoteScript(
            initialTarget,
            title: "10-passwordless-sudo.sh (no-op verification)",
            command: "bash <(wget -qO- \(bootstrapBaseURL)/10-passwordless-sudo.sh)"
        )

        try runRemoteScript(
            initialTarget,
            title: "20-git-clone.sh (wget)",
            command: "bash <(wget -qO- \(bootstrapBaseURL)/20-git-clone.sh)"
        )

        // Push host-generated material into the VM. /var/lib/mpd is
        // dev-user-owned after step 20.
        // The trust anchor. Public material — this is what the VM's three
        // trust stores (system, NSS, Firefox policy) are told to trust.
        try pushFile(
            initialTarget,
            title: "push root CA cert",
            localPath: caCertPath,
            remotePath: "/var/lib/mpd/conf/caroot/rootCA.pem",
            mode: 0o644
        )
        // The signing CA. `mpd --vm-setup` signs the service certificate
        // caddy serves the zone apex and adminer with, plus a leaf per
        // project, so a private key has to be here — but it is this VM's
        // own intermediate, name-constrained to `<NNN>.mpd.test`, not the
        // root. Rooting this VM therefore buys the ability to forge names
        // in a zone the attacker already controls, and nothing more. The
        // root's private key has never been on a VM and never will be.
        try pushFile(
            initialTarget,
            title: "push VM CA cert",
            localPath: vmCaCertPath,
            remotePath: "/var/lib/mpd/conf/caroot/vmCA.pem",
            mode: 0o644
        )
        try pushFile(
            initialTarget,
            title: "push VM CA key",
            localPath: vmCaKeyPath,
            remotePath: "/var/lib/mpd/conf/caroot/vmCA-key.pem",
            mode: 0o600
        )

        // The script renames the host, pins the static IP, and drops
        // the SSH session in the process. We don't get a clean exit
        // code back. Treat exit != 0 as expected when followed by the
        // new IP coming online.
        step(2, "30-networking.sh \(MpdVirt.vmId(octet: octet))  (SSH will drop)")
        _ = try MpdVirt.Host.Ssh.stream(
            initialTarget,
            "bash /opt/mpd/bootstrap/30-networking.sh \(MpdVirt.vmId(octet: octet))"
        )
        // We deliberately don't check the exit code here — the SSH
        // session dies as part of the IP rename and Process sees a
        // non-zero status. The next step verifies the rename
        // succeeded by trying to reach the new IP.

        // --- Phase B: at the new IP, after the rename ---

        step(3, "waiting for VM to come back up @ \(postRenameIP)")
        guard MpdVirt.Host.Ssh.waitUntilReachable(postRenameTarget, timeoutSeconds: 180) else {
            throw Failure.reconnectTimeout(ip: postRenameIP)
        }

        // The VM is now at its canonical IP with its canonical hostname.
        // This is the point at which it becomes "ours" — let Setup persist
        // the registry entry before we continue with the remaining (slow)
        // bootstrap steps. If 40/50/60/mpd--setup fails after this, a
        // subsequent `mpd-virt setup <NNN>` will find the registry and
        // resume in fix-known mode.
        try onCanonicalIPReady()

        try runRemoteScript(
            postRenameTarget,
            title: "40-install-software.sh",
            command: "bash /opt/mpd/bootstrap/40-install-software.sh"
        )

        try runRemoteScript(
            postRenameTarget,
            title: "50-build.sh",
            command: "bash /opt/mpd/bootstrap/50-build.sh"
        )

        // Names for LAN machines that are not VMs. Pushed before
        // `--vm-setup` so that run publishes them into the VM's resolver,
        // rather than needing a second pass.
        step(0, "push LAN host records → \(MpdVirt.Server.remoteLanHostsPath)")
        try MpdVirt.ServerAdmin.pushHosts(to: postRenameTarget)

        try runRemoteScript(
            postRenameTarget,
            title: "mpd --vm-setup",
            command: "mpd --vm-setup"
        )

        step(4, "VM-side bootstrap complete.")
    }

    // MARK: - Pre-flight: passwordless sudo

    /// Verify the dev user already has passwordless sudo on the VM.
    /// If not, walk the dev through the one-shot manual setup —
    /// print the `ssh -t … bash <(wget …)` line, wait for them to
    /// run it in another terminal, re-probe and continue.
    ///
    /// Why not just run it ourselves: mpd-virt's ssh has no local PTY,
    /// so the remote `su` would fall back to reading from stdin
    /// without setting noecho — root password would echo in plaintext.
    /// Same shape as `Ssh.ensureKeyAuth`'s manual ssh-copy-id path.
    private static func ensurePasswordlessSudo(_ target: MpdVirt.Host.Ssh.Target) throws {
        step(0, "probe: sudo -n true (does \(target.user) have passwordless sudo?)")
        if (try? MpdVirt.Host.Ssh.exec(target, "sudo -n true 2>/dev/null"))?.ok == true { return }

        FileHandle.standardError.write(Data("""

              ▸ \(target.user)@\(target.host) doesn't have passwordless sudo yet.
                Open ANOTHER terminal window and run:

                    ssh -t \(target.user)@\(target.host) 'bash <(wget -qO- \(bootstrapBaseURL)/10-passwordless-sudo.sh)'

                (The `-t` forces a remote PTY so the password prompt uses noecho —
                your root password will NOT echo to the screen.)

                When the script finishes, come back here and press Enter — I'll
                re-test and continue.

            """.utf8))
        FileHandle.standardError.write(Data(
            "    Press Enter when 10-passwordless-sudo.sh is done (Ctrl-C to abort): ".utf8
        ))
        guard readLine() != nil else {
            throw MpdVirt.BackendError.other("aborted — no input received.")
        }

        // Re-probe.
        if (try? MpdVirt.Host.Ssh.exec(target, "sudo -n true 2>/dev/null"))?.ok == true {
            FileHandle.standardError.write(Data(
                "  ✓ passwordless sudo works now — continuing.\n".utf8
            ))
            return
        }

        throw MpdVirt.BackendError.other("""
            Passwordless sudo still not configured on \(target.user)@\(target.host).
            Sanity-check the manual run:

                ssh \(target.user)@\(target.host) 'sudo -n true'

            If that prints nothing and exits 0 it's working; otherwise re-run the
            10-passwordless-sudo.sh line above. Then re-run `mpd-virt setup`.
            """)
    }

    // MARK: - Step primitives

    /// Run one bootstrap script (or any remote command), streaming
    /// output. Throws if it exits non-zero.
    private static func runRemoteScript(
        _ target: MpdVirt.Host.Ssh.Target,
        title: String,
        command: String,
        requestTTY: Bool = false
    ) throws {
        step(0, title)
        let code = try MpdVirt.Host.Ssh.stream(target, command, requestTTY: requestTTY)
        if code != 0 {
            throw Failure.stepFailed(title: title, exitCode: code)
        }
    }

    private static func pushFile(
        _ target: MpdVirt.Host.Ssh.Target,
        title: String,
        localPath: String,
        remotePath: String,
        mode: Int
    ) throws {
        step(0, "\(title) → \(remotePath)")
        do {
            try MpdVirt.Host.Ssh.put(target, localPath: localPath, remotePath: remotePath, mode: mode)
        } catch {
            throw Failure.scpFailed(title: title, underlying: error)
        }
    }

    /// Print a section heading to stderr. The leading bullet aligns
    /// with Setup's host-side info() output for visual continuity.
    private static func step(_ phase: Int, _ msg: String) {
        let prefix = phase == 0 ? "  →" : "  ▶"
        FileHandle.standardError.write(Data("\(prefix) \(msg)\n".utf8))
    }
}
