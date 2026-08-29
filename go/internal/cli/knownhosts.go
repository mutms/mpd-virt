package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/sshconfig"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// The developer's own ~/.ssh/known_hosts, not the per-VM file mpd-virt
// pins for itself (see vmTarget). `create` writes the entries, `remove`
// and `uninstall` clear them, `adopt` does neither — see AGENTS.md.

// runtimeSSHDir mirrors mpd's podman.RuntimeSSHDir.
const runtimeSSHDir = "/var/lib/mpd/state/runtime-ssh"

// pinHostKeys writes this VM's host keys into the developer's
// ~/.ssh/known_hosts, so the first `ssh mpd-<NNN>` prompts for neither
// hop. Adds no trust create has not already taken: the VM's key is the one
// it pinned, the runtime's is read over that pinned channel. Best-effort.
func pinHostKeys(ctx context.Context, t host.Target, id vmid.ID) {
	lines := append(vmHostKeyLines(id), runtimeHostKeyLines(ctx, t, id)...)
	if len(lines) == 0 {
		return
	}
	// Whatever this number used to be, it is not this machine.
	forgetHostKeys(ctx, id)
	added, err := appendKnownHosts(lines)
	if err != nil {
		fmt.Printf("  ⚠ could not write ~/.ssh/known_hosts: %v\n", err)
		return
	}
	fmt.Printf("  ✓ %d host key(s) added to ~/.ssh/known_hosts (%s) — no first-connect prompt\n",
		added, strings.Join(sshconfig.HostKeyAliases(id), ", "))
}

// vmHostKeyLines reads the key create pinned. The per-VM file is keyed by
// the same HostKeyAlias the developer's config uses, so lines transfer
// verbatim.
func vmHostKeyLines(id vmid.ID) []string {
	data, err := os.ReadFile(paths.KnownHosts(id))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) >= 3 && fields[0] == id.Name() {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// runtimeHostKeyLines reads the runtime's public host keys from the VM's
// stored copy — readable whether or not the runtime is running — and
// labels them with the alias its stanza pins under.
func runtimeHostKeyLines(ctx context.Context, t host.Target, id vmid.ID) []string {
	raw, err := t.Line(ctx, "sudo -n cat "+runtimeSSHDir+"/ssh_host_*_key.pub 2>/dev/null || true")
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil
	}
	alias := id.Name() + "-runtime"
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		// "<type> <base64> <comment>" → "<alias> <type> <base64>".
		f := strings.Fields(line)
		if len(f) < 2 || !(strings.HasPrefix(f[0], "ssh-") || strings.HasPrefix(f[0], "ecdsa-")) {
			continue
		}
		out = append(out, alias+" "+f[0]+" "+f[1])
	}
	return out
}

// appendKnownHosts adds the missing lines, creating ~/.ssh and the file
// with the permissions ssh insists on.
func appendKnownHosts(lines []string) (int, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	path := filepath.Join(dir, "known_hosts")

	existing := map[string]bool{}
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			existing[strings.TrimSpace(line)] = true
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	added := 0
	for _, line := range lines {
		if line == "" || existing[line] {
			continue
		}
		if _, err := fmt.Fprintln(f, line); err != nil {
			return added, err
		}
		existing[line] = true
		added++
	}
	return added, nil
}

// forgetHostKeys drops this VM's entries, so re-adopting the number later
// is a clean first-contact prompt rather than a host-key-changed refusal.
// `ssh-keygen -R` matches the alias exactly and keeps known_hosts.old;
// IP-keyed entries are left alone. Silent — the caller reports.
func forgetHostKeys(ctx context.Context, id vmid.ID) {
	for _, alias := range sshconfig.HostKeyAliases(id) {
		_, _ = exec.Capture(ctx, exec.Cmd{Name: "ssh-keygen", Args: []string{"-R", alias}})
	}
}
