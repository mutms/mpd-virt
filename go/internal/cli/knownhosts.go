package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/sshconfig"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// Every host key mpd-virt knows about lives in the per-VM
// ~/.mpd-virt/<NNN>/known_hosts, and the developer's own ~/.ssh/known_hosts
// is never written: the managed ssh-config stanzas point UserKnownHostsFile
// at that file. First contact pins the VM's key there (see vmTarget); this
// file adds the runtime container's. See docs/security.md.

// runtimeSSHDir mirrors mpd's podman.RuntimeSSHDir.
const runtimeSSHDir = "/var/lib/mpd/state/runtime-ssh"

// pinRuntimeHostKey records the runtime container's host keys, so the first
// `ssh mpd-<NNN>` prompts for neither hop. Adds no trust the caller has not
// already taken: the keys are read over the channel the VM's own pinned key
// authenticates. Best-effort — an unpinned runtime is a prompt, not a
// failure.
func pinRuntimeHostKey(ctx context.Context, t host.Target, id vmid.ID) {
	lines := runtimeHostKeyLines(ctx, t, id)
	if len(lines) == 0 {
		return
	}
	if err := replaceEntries(id, sshconfig.RuntimeKeyAlias(id), lines); err != nil {
		fmt.Printf("  ⚠ could not pin the runtime host key: %v\n", err)
		return
	}
	pass(fmt.Sprintf("runtime host key pinned (%s) — no first-connect prompt",
		sshconfig.RuntimeKeyAlias(id)))
}

// runtimeHostKeyLines reads the runtime's public host keys from the VM's
// stored copy — readable whether or not the runtime is running — and
// labels them with the alias its stanza pins under.
func runtimeHostKeyLines(ctx context.Context, t host.Target, id vmid.ID) []string {
	// The glob runs in a root shell: the directory is 0700 root-owned, so
	// expanding it in the dev user's shell yields the literal pattern.
	raw, err := t.Line(ctx, "sudo -n bash -c 'cat "+runtimeSSHDir+"/ssh_host_*_key.pub' 2>/dev/null || true")
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil
	}
	alias := sshconfig.RuntimeKeyAlias(id)
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

// replaceEntries rewrites one alias's lines in the VM's known_hosts,
// leaving every other alias alone. Replace, not append: a rebuilt runtime
// generates a new key, and a stale line beside it is a refused connection.
func replaceEntries(id vmid.ID, alias string, lines []string) error {
	path := paths.EnsureKnownHosts(id)

	var kept []string
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if f := strings.Fields(line); len(f) > 0 && f[0] == alias {
				continue
			}
			if strings.TrimSpace(line) != "" {
				kept = append(kept, line)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	body := strings.Join(append(kept, lines...), "\n") + "\n"
	return os.WriteFile(path, []byte(body), 0o600)
}
