// mpd-virt — shared visual primitives for diag / delete / similar.
//
// Verbs that walk a checklist (diag, delete, doctor-style ones) use
// these to render output. Keeping the colour codes + bullets in one
// place means a future visual refresh is one file.

import Foundation

extension MpdVirt.Ui {

    /// `── Title ──────────────────────────────────────`
    static func header(_ s: String) {
        print("\n\u{001B}[1;36m── \(s) \u{001B}[0m"
            + String(repeating: "─", count: max(0, 60 - s.count)))
    }

    /// `▸ Section name` (bold)
    static func section(_ s: String) {
        print("\n\u{001B}[1m▸ \(s)\u{001B}[0m")
    }

    /// `    ✓ message` (green)
    static func ok(_ s: String) {
        print("    \u{001B}[32m✓\u{001B}[0m \(s)")
    }

    /// `    ⚠ message` (yellow). Surface what the dev should know
    /// about but didn't break.
    static func warn(_ s: String) {
        print("    \u{001B}[33m⚠\u{001B}[0m \(s)")
    }

    /// `    ✗ message` (red). Something failed.
    static func fail(_ s: String) {
        print("    \u{001B}[31m✗\u{001B}[0m \(s)")
    }

    /// `    → message` (neutral). Used for informative lines that
    /// aren't success/warn/fail — e.g. "nothing to do here".
    static func info(_ s: String) {
        print("    → \(s)")
    }

    /// Unprefixed indented line (4 spaces). For continuation text
    /// inside a section: details, multi-line instructions, …
    static func indent(_ s: String) {
        print("    \(s)")
    }

    /// Abbreviate the user's home directory to `~` for display.
    ///
    /// Every path mpd-virt prints lives under `~/.mpd-virt/`, so the home
    /// prefix is noise that pushes the interesting part of the line off to
    /// the right — and these lines are meant to be copy-pasted, where `~`
    /// is what a shell expands anyway.
    ///
    /// Display only. Anything handed to a `Process` keeps the real path:
    /// `~` is a shell convention, and we exec without a shell.
    static func path(_ p: String) -> String {
        let home = MpdVirt.homeDir
        guard !home.isEmpty, p == home || p.hasPrefix(home + "/") else { return p }
        return "~" + p.dropFirst(home.count)
    }

    /// Yes/No confirmation prompt. Returns true if `assumeYes` is set
    /// or the user answers y/Y. Empty answer = "no" (safer default
    /// for destructive operations like `delete`).
    static func confirm(_ msg: String, assumeYes: Bool) -> Bool {
        if assumeYes { return true }
        FileHandle.standardError.write(Data("    \(msg) [y/N]: ".utf8))
        guard let line = readLine() else { return false }
        let first = line.trimmingCharacters(in: .whitespaces).lowercased().first
        return first == "y"
    }
}
