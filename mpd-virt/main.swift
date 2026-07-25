// mpd-virt — CLI entry point.
//
// Verb surface (step 1 scaffold — most verbs print "not implemented"
// with the correct dispatch already in place):
//
//   mpd-virt create <NNN>        --backend= --username= --vm-disk= --vm-ram= --yes
//   mpd-virt clone  <NNN>        --backend= --template= --username= --vm-disk= --vm-ram= --yes
//   mpd-virt setup  <NNN>        --backend= --ip= --username= --yes
//   mpd-virt delete <NNN>        --keep-vm --yes
//   mpd-virt start  <NNN>
//   mpd-virt stop   <NNN>        --kill
//   mpd-virt list                --json
//   mpd-virt diag   <NNN>        --non-interactive
//   mpd-virt update <NNN>
//   mpd-virt uninstall           --force --yes
//   mpd-virt backend list
//   mpd-virt backend set-default <name>
//
// The 3-digit octet NNN is the canonical key for every VM (VM name
// mpd-<NNN>, IP 10.211.55.<NNN>, registry dir ~/.mpd-virt/<NNN>/, …).
// `--backend=` may be passed on any verb; when omitted, the default
// from ~/.mpd-virt/conf/backend.env is used.

import ArgumentParser
import Foundation

// MARK: - Top-level

struct MpdVirtCLI: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "mpd-virt",
        abstract: "macOS host-side orchestrator for mpd VMs (Parallels, UTM, or pre-existing).",
        version: "0.1.0-dev",
        subcommands: [
            CreateCmd.self,
            CloneCmd.self,
            SetupCmd.self,
            DeleteCmd.self,
            StartCmd.self,
            StopCmd.self,
            ListCmd.self,
            DiagCmd.self,
            UpdateCmd.self,
            UninstallCmd.self,
            BackendCmd.self,
            ServerCmd.self,
            CaCmd.self,
        ],
        defaultSubcommand: ListCmd.self
    )
}

// MARK: - Provisioning verbs (create, clone)

struct CreateCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "create",
        abstract: "Materialize a fresh VM from scratch (no template). Initial scope: UTM."
    )

    @Argument(help: "Last IP octet on the Parallels Shared network (100–254). VM name becomes mpd-<NNN>.")
    var octet: Int

    @Option(name: .customLong("backend"), help: "Backend name (parallels|utm|general). If omitted, the default from ~/.mpd-virt/conf/backend.env is used.")
    var backend: String?

    @Option(name: .customLong("username"), help: "Dev user inside the VM (defaults to the current macOS user).")
    var username: String?

    @Option(name: .customLong("vm-disk"), help: "VM disk size (e.g. 80G). Backend default if omitted.")
    var vmDisk: String?

    @Option(name: .customLong("vm-ram"), help: "VM RAM size (e.g. 8G). Backend default if omitted.")
    var vmRam: String?

    @Flag(name: .customLong("yes"), help: "Skip confirmation prompts.")
    var assumeYes: Bool = false

    @Flag(name: .customLong("debug"), help: "Trace every external command to stderr.")
    var debug: Bool = false

    func run() throws {
        MpdVirt.Debug.enabled = debug
        try MpdVirt.Create.run(
            octet: octet,
            backendFlag: backend,
            username: username ?? defaultUsername(),
            vmDisk: vmDisk,
            vmRam: vmRam,
            assumeYes: assumeYes
        )
    }
}

struct CloneCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "clone",
        abstract: "Clone an existing template/VM into a new mpd-<NNN>. Initial scope: Parallels."
    )

    @Argument(help: "Last IP octet on the Parallels Shared network (100–254). VM name becomes mpd-<NNN>.")
    var octet: Int

    @Option(name: .customLong("backend"), help: "Backend name (parallels|utm|general). If omitted, the default from ~/.mpd-virt/conf/backend.env is used.")
    var backend: String?

    @Option(name: .customLong("template"), help: "Source template name in the hypervisor (e.g. mpd-template-trixie). Required.")
    var template: String

    @Option(name: .customLong("username"), help: "Dev user inside the VM (defaults to the current macOS user).")
    var username: String?

    @Option(name: .customLong("vm-disk"), help: "VM disk size (e.g. 80G). Template default if omitted.")
    var vmDisk: String?

    @Option(name: .customLong("vm-ram"), help: "VM RAM size (e.g. 8G). Template default if omitted.")
    var vmRam: String?

    @Flag(name: .customLong("yes"), help: "Skip confirmation prompts.")
    var assumeYes: Bool = false

    @Flag(name: .customLong("debug"), help: "Trace every external command to stderr.")
    var debug: Bool = false

    func run() throws {
        MpdVirt.Debug.enabled = debug
        try MpdVirt.Clone.run(
            octet: octet,
            backendFlag: backend,
            template: template,
            username: username ?? defaultUsername(),
            vmDisk: vmDisk,
            vmRam: vmRam,
            assumeYes: assumeYes
        )
    }
}

// MARK: - Universal verb (setup)

struct SetupCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "setup",
        abstract: "Idempotent post-provisioning. Fixes a known VM (registry entry exists) or adopts a new one at --ip with --backend."
    )

    @Argument(help: "Last IP octet (100–254). Used as canonical VM identifier mpd-<NNN>.")
    var octet: Int

    @Option(name: .customLong("ip"), help: "Reachable IP of the VM. Required for first-time adoption; optional override for fix-known mode.")
    var ip: String?

    @Option(name: .customLong("backend"), help: "Backend name (parallels|utm|general). Required for first-time adoption unless a default is set; ignored if the VM is already registered.")
    var backend: String?

    @Option(name: .customLong("username"), help: "Dev user inside the VM (defaults to the current macOS user — same convention as create/clone).")
    var username: String?

    @Flag(name: .customLong("debug"), help: "Trace every external command (ssh/scp/sudo/openssl/etc.) to stderr.")
    var debug: Bool = false

    func run() throws {
        MpdVirt.Debug.enabled = debug
        try MpdVirt.Setup.runCLI(
            octet: octet,
            ipFlag: ip,
            backendFlag: backend,
            usernameFlag: username ?? defaultUsername()
        )
        // Mandatory mac-side sanity check, no prompts.
        try MpdVirt.Diag.run(octet: octet, nonInteractive: true)
    }
}

// MARK: - Lifecycle verbs

struct DeleteCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "delete",
        abstract: "Remove an mpd VM. With --keep-vm, only the bookkeeping is wiped (re-add with `setup`)."
    )

    @Argument(help: "Octet (100–254).")
    var octet: Int

    @Flag(name: .customLong("keep-vm"), help: "Skip the backend's destroy step; only wipe ~/.mpd-virt/<NNN>/ and host config.")
    var keepVM: Bool = false

    @Flag(name: .customLong("yes"), help: "Skip confirmation prompt.")
    var assumeYes: Bool = false

    func run() throws {
        try MpdVirt.Delete.run(octet: octet, keepVM: keepVM, assumeYes: assumeYes)
    }
}

struct StartCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "start",
        abstract: "Start an mpd-<NNN> VM via its backend."
    )

    @Argument(help: "Octet (100–254).")
    var octet: Int

    func run() throws { try MpdVirt.Start.run(octet: octet) }
}

struct StopCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "stop",
        abstract: "Suspend an mpd-<NNN> VM. With --kill, hard-stop."
    )

    @Argument(help: "Octet (100–254).")
    var octet: Int

    @Flag(name: .customLong("kill"), help: "Hard stop instead of suspend.")
    var kill: Bool = false

    func run() throws { try MpdVirt.Stop.run(octet: octet, kill: kill) }
}

// MARK: - Read-only verbs

struct ListCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "list",
        abstract: "List every registered mpd VM with live backend state."
    )

    @Flag(name: .customLong("json"), help: "Emit machine-readable JSON instead of a table.")
    var json: Bool = false

    func run() throws { try MpdVirt.List.run(json: json) }
}

struct DiagCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "diag",
        abstract: "Per-VM diagnostic: registry → backend → ping → platform.env compare → SSH alias. Interactive by default; --non-interactive stops before the optional macOS DNS / routing check."
    )

    @Argument(help: "Octet (100–254).")
    var octet: Int

    @Flag(name: .customLong("non-interactive"), help: "Run only the mandatory checks (registry → backend → ping → platform.env → SSH). Skip the macOS-side DNS / routing walkthrough.")
    var nonInteractive: Bool = false

    func run() throws { try MpdVirt.Diag.run(octet: octet, nonInteractive: nonInteractive) }
}

struct UpdateCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "update",
        abstract: "Pull latest mpd source on the VM, rebuild the in-VM `mpd` binary, re-run `mpd --vm-setup`. Runs /opt/mpd/bootstrap/99-update.sh over SSH."
    )

    @Argument(help: "Octet (100–254).")
    var octet: Int

    @Flag(name: .customLong("debug"), help: "Trace every external command to stderr.")
    var debug: Bool = false

    func run() throws {
        MpdVirt.Debug.enabled = debug
        try MpdVirt.Update.run(octet: octet)
    }
}

struct UninstallCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "uninstall",
        abstract: "Remove the persistent host-side trust material (CA in System Keychain, ~/.mpd-virt/conf/, every /etc/resolver/<id>.mpd.test)."
    )

    @Flag(name: .customLong("force"), help: "Proceed even when VMs are still registered (leaves SSH config blocks dangling).")
    var force: Bool = false

    @Flag(name: .customLong("yes"), help: "Skip confirmation prompts.")
    var assumeYes: Bool = false

    func run() throws { try MpdVirt.Uninstall.run(force: force, assumeYes: assumeYes) }
}

// MARK: - Backend admin

struct BackendCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "backend",
        abstract: "Manage the default backend.",
        subcommands: [BackendListCmd.self, BackendSetDefaultCmd.self],
        defaultSubcommand: BackendListCmd.self
    )
}

struct BackendListCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "list",
        abstract: "List backends compiled into this binary + capabilities + current default."
    )

    func run() throws { try MpdVirt.BackendAdmin.list() }
}

struct BackendSetDefaultCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "set-default",
        abstract: "Persist the default backend to ~/.mpd-virt/conf/backend.env."
    )

    @Argument(help: "Backend name (parallels|utm|general).")
    var name: String

    func run() throws { try MpdVirt.BackendAdmin.setDefault(name) }
}

// MARK: - LAN servers (machines that are not mpd VMs)

struct ServerCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "server",
        abstract: "Manage LAN machines with names under mpd.test (proxmox, forge, runner, …).",
        subcommands: [
            ServerAddCmd.self, ServerListCmd.self, ServerDeleteCmd.self,
            ServerCertCmd.self, ServerDeployCmd.self, ServerSyncCmd.self,
        ],
        defaultSubcommand: ServerListCmd.self
    )
}

struct ServerAddCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "add",
        abstract: "Register a LAN server. Name is a single label under mpd.test (`forge` → forge.mpd.test)."
    )

    @Argument(help: "Server name: `forge` or `forge.mpd.test`. Numeric names are VM zones and are refused.")
    var name: String

    @Option(name: .customLong("ip"), help: "IPv4 or IPv6 address on the LAN.")
    var ip: String

    @Option(name: .customLong("kind"), help: "proxmox|forgejo|caddy|generic — selects the deploy hints.")
    var kind: String = "generic"

    @Option(name: .customLong("ssh"), help: "Optional user@host, used to address the deploy recipe.")
    var ssh: String?

    func run() throws {
        try MpdVirt.ServerAdmin.add(name: name, ip: ip, kind: kind, ssh: ssh)
    }
}

struct ServerListCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "list",
        abstract: "List registered LAN servers with address, kind and certificate state."
    )

    @Flag(name: .customLong("etc-hosts"),
          help: "Emit only the hosts(5) block, ready to paste into /etc/hosts on this Mac.")
    var etcHosts: Bool = false

    func run() throws { try MpdVirt.ServerAdmin.list(etcHosts: etcHosts) }
}

struct ServerDeleteCmd: ParsableCommand {
    // `delete`, not `rm`, to match `mpd-virt delete <NNN>` for VMs — one
    // word for "remove a thing mpd-virt tracks". `rm` stays as an alias so
    // the muscle memory of anyone who used the earlier spelling still works.
    static let configuration = CommandConfiguration(
        commandName: "delete",
        abstract: "Forget a LAN server and delete its certificate + key.",
        aliases: ["rm"]
    )

    @Argument(help: "Server name.")
    var name: String

    @Flag(name: .customLong("yes"), help: "Skip the confirmation prompt.")
    var assumeYes: Bool = false

    func run() throws { try MpdVirt.ServerAdmin.delete(name: name, assumeYes: assumeYes) }
}

struct ServerCertCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "cert",
        abstract: "Issue (or renew) this server's TLS certificate, signed by the mpd root CA."
    )

    @Argument(help: "Server name.")
    var name: String

    @Option(name: .customLong("san"),
            parsing: .singleValue,
            help: "Extra DNS name under mpd.test to cover. Repeatable.")
    var sans: [String] = []

    @Flag(name: .customLong("force"),
          help: "Re-issue even when the current certificate has plenty of life left.")
    var force: Bool = false

    @Flag(inversion: .prefixedNo, exclusivity: .exclusive,
          help: "Include the server's IP as an IP SAN, so https://<ip>/ verifies too. Default: on for --kind proxmox.")
    var ip: Bool?

    func run() throws {
        try MpdVirt.ServerAdmin.cert(name: name, extraSans: sans, withIP: ip, force: force)
    }
}

struct ServerDeployCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "deploy",
        abstract: "Print exactly what to install on this server: root CA, certificate, hosts entries."
    )

    @Argument(help: "Server name.")
    var name: String

    func run() throws { try MpdVirt.ServerAdmin.deploy(name: name) }
}

struct ServerSyncCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "sync",
        abstract: "Publish every LAN name into VMs' resolvers, so containers resolve them too."
    )

    @Argument(help: "Octet of a single VM (100–254). Omit (or pass --all) to sync every registered VM.")
    var octet: Int?

    /// Accepted and equivalent to omitting the octet. Every message that
    /// suggests this command spells it `--all`, and a flag that the docs
    /// use but the parser rejects fails as "Unknown option" — which reads
    /// like the command is broken rather than the flag being redundant.
    @Flag(name: .customLong("all"), help: "Sync every registered VM (the default).")
    var all: Bool = false

    func run() throws {
        if all, octet != nil {
            throw ValidationError("pass either an octet or --all, not both.")
        }
        try MpdVirt.ServerAdmin.sync(octet: octet)
    }
}

// MARK: - CA

struct CaCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "ca",
        abstract: "Inspect and export the mpd root CA.",
        subcommands: [CaExportCmd.self],
        defaultSubcommand: CaExportCmd.self
    )
}

struct CaExportCmd: ParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "export",
        abstract: "Print the root CA's public certificate, for installing on a LAN host's trust store."
    )

    @Option(name: .customLong("path"), help: "Write here instead of stdout.")
    var path: String?

    func run() throws { try MpdVirt.ServerAdmin.caExport(path: path) }
}

// MARK: - Helpers

/// Current macOS user — used as the default `--username` for create/clone.
/// (For `setup` first-time-adoption we never auto-fill; the user must
/// pass `--username` because the in-VM dev user must match.)
func defaultUsername() -> String {
    NSUserName()
}

MpdVirtCLI.main()
