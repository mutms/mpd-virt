package cli

import (
	"context"
	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/sshconfig"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// forgetHostKeys drops this VM's entries from the user's own
// ~/.ssh/known_hosts, so re-adopting the number later is a clean
// first-contact prompt rather than the REMOTE HOST IDENTIFICATION HAS
// CHANGED wall.
//
// Safe to do on a removal, and only on a removal: the operator has just
// typed the VM's name to confirm they are discarding it, which is the
// same act as discarding its identity. `ssh-keygen -R` matches the alias
// exactly (mpd-<NNN> does not touch mpd-<NNN>-runtime), rewrites nothing
// else, and leaves the previous file as known_hosts.old. Entries keyed by
// IP are deliberately left alone — an address is not mpd-virt's to claim.
//
// Best-effort and quiet: ssh-keygen exits 0 whether or not the entry was
// there, and non-zero mainly when there is no known_hosts file at all
// (a Mac that has never ssh'd anywhere). Neither is worth a warning.
// Silent: the caller reports, because remove says it once and uninstall
// summarises a whole loop.
func forgetHostKeys(ctx context.Context, id vmid.ID) {
	for _, alias := range sshconfig.HostKeyAliases(id) {
		_, _ = exec.Capture(ctx, exec.Cmd{Name: "ssh-keygen", Args: []string{"-R", alias}})
	}
}
