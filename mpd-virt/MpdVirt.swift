// mpd-virt — Namespace root.
//
// macOS host-side orchestrator for mpd. Drives a hypervisor backend
// (Parallels Desktop Pro, UTM, or — for adoption of pre-existing VMs —
// the backend-less "general" path) to create + manage mpd VMs.
//
// Verb implementations live under Verbs/, backend implementations under
// Backend/. The Backend enum (raw value: backend name) carries both the
// kind tag and the dispatch surface. See Backend/Backend.swift.

import Foundation

enum MpdVirt {

    // MARK: - Persistent paths on the macOS host

    /// User's home directory.
    static var homeDir: String {
        FileManager.default.homeDirectoryForCurrentUser.path
    }

    /// Top-level dir mpd-virt owns on the host: `~/.mpd-virt/`.
    /// Holds `conf/` (identity, survives `delete`) and per-VM bookkeeping.
    static var rootDir: String { "\(homeDir)/.mpd-virt" }

    /// Persistent identity dir: `~/.mpd-virt/conf/`.
    /// CA, service certs, default-backend file. Survives every
    /// `mpd-virt delete <octet>`.
    static var confDir: String { "\(rootDir)/conf" }

    /// CA root keypair: `~/.mpd-virt/conf/caroot/`.
    static var caRootDir: String { "\(confDir)/caroot" }

    /// Service certificate dir: `~/.mpd-virt/conf/service/`.
    static var serviceCertDir: String { "\(confDir)/service" }

    /// Default-backend file: `~/.mpd-virt/conf/backend.env`.
    /// Single line: `MPD_VIRT_DEFAULT_BACKEND=<name>`.
    static var backendConfFile: String { "\(confDir)/backend.env" }

    /// 3-digit string form of an octet (`"159"`, `"100"`, …). Same shape
    /// used for VM names, host directories and SSH aliases.
    static func vmId(octet: Int) -> String { String(format: "%03d", octet) }

    /// Per-VM bookkeeping dir: `~/.mpd-virt/<NNN>/`. The presence of
    /// `<NNN>/env` inside it is what makes a VM "known to the registry".
    /// Removed by `mpd-virt delete <octet>` (the `env` file too).
    static func vmDir(octet: Int) -> String { "\(rootDir)/\(vmId(octet: octet))" }

    /// Per-VM env file: `~/.mpd-virt/<NNN>/env`. See Registry.swift.
    static func vmEnvFile(octet: Int) -> String { "\(vmDir(octet: octet))/env" }

    /// Convention: VM name everywhere is `mpd-<NNN>` (3-digit padded).
    static func vmName(octet: Int) -> String { "mpd-\(vmId(octet: octet))" }

    /// Valid octet range for managed VMs. Clamped to avoid the Parallels
    /// Shared DHCP pool (1–99) and the reserved broadcast/special
    /// addresses (>254).
    static let managedOctetRange: ClosedRange<Int> = 100...254

    // MARK: - Verb namespaces (one per file under Verbs/)

    enum Create {}
    enum Clone {}
    enum Setup {}
    enum Delete {}
    enum Start {}
    enum Stop {}
    enum List {}
    enum Diag {}
    enum Update {}
    enum BackendAdmin {}
    enum ServerAdmin {}
    enum Uninstall {}

    // MARK: - Backend implementation namespaces (one per file under Backend/)

    enum Parallels {}
    enum UTM {}
    enum General {}

    // MARK: - Host helpers (under Host/)

    enum Host {
        enum Ssh {}            // Host/Ssh.swift          — ssh/scp wrapper
        enum SudoRecipe {}     // Host/SudoRecipe.swift   — sudo UX printer
        enum Keychain {}       // Host/Keychain.swift     — CA trust install/remove
        enum SSHConfig {}      // Host/SSHConfig.swift    — ~/.ssh/config managed block
    }

    // MARK: - Bootstrap runner (under Bootstrap/)

    enum Bootstrap {
        enum RunInVM {}        // Bootstrap/RunInVM.swift — runs mpd/bootstrap/{10..50} via SSH
    }

    // MARK: - Other namespaces

    enum Net {}                // Net.swift               — container subnet / DNS addressing
    enum Registry {}
    enum Server {}             // Server.swift            — LAN hosts with names under mpd.test
    enum CA {}                 // CA.swift                — name-constrained CA for *.mpd.test
    enum CloudInit {}          // CloudInit.swift         — cloud image cache + cidata ISO gen
    enum Ui {}                 // Ui.swift                — shared section / ok / warn / fail printers

    // MARK: - Debug tracing

    /// Process-wide debug flag. When true, every external command
    /// (ssh, scp, sudo, openssl, prlctl, …) prints its argv to stderr
    /// before running and its exit code after. Set by the `--debug`
    /// flag on the CLI verbs.
    enum Debug {
        nonisolated(unsafe) static var enabled: Bool = false

        /// Emit a debug line, prefixed so it's easy to grep for.
        static func log(_ msg: String) {
            guard enabled else { return }
            FileHandle.standardError.write(Data("  [debug] \(msg)\n".utf8))
        }
    }
}
