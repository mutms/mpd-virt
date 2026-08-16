package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// boxTarget is the one way the cli package reaches an adopted (or
// being-adopted) box: user@ip with the box's host key pinned in its per-box
// known_hosts file under the stable alias mpd-<NNN>. First contact records
// the key (accept-new); any later change is refused, which is the property
// the whole adoption flow leans on — takeover pushes CA material to
// whatever answers, so "whatever answers" must be provably the same box
// every time after the first.
func boxTarget(id vmid.ID, user, ip string) host.Target {
	return host.Target{
		User:           user,
		Host:           ip,
		KnownHostsFile: paths.EnsureKnownHosts(id),
		HostKeyAlias:   id.Name(),
	}
}

// printHostKeyFingerprint shows the pinned key for a box's alias — the
// takeover-time companion of first-contact pinning: TOFU is only as good as
// the developer's chance to compare the fingerprint against the box's
// console, so print it while they are looking.
func printHostKeyFingerprint(ctx context.Context, id vmid.ID) {
	r, err := exec.Capture(ctx, exec.Cmd{
		Name: "ssh-keygen", Args: []string{"-l", "-f", paths.KnownHosts(id)},
	})
	if err != nil || r.Failed() {
		return // best-effort: no fingerprint is a cosmetic loss, not a failure
	}
	for _, line := range strings.Split(r.Stdout, "\n") {
		if strings.Contains(line, " "+id.Name()+" ") {
			fmt.Printf("  ✓ host key pinned: %s\n", strings.TrimSpace(line))
		}
	}
}
