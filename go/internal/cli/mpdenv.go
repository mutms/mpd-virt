package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"

	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/paths"
)

// The developer's MPD_* defaults — PHP version, Moodle admin password,
// Behat preferences, the runtime-control switch — carried from
// ~/.mpd-virt/mpd-virt.env into every box mpd-virt owns.
//
// The layer mpd reads this as is scoped to the *developer*, not the box:
// a VM runs one runtime, so "per-VM defaults" was a distinction without a
// difference, while a developer routinely runs several boxes that should
// agree on how they behave. Holding the file here and pushing it is what
// makes one edit reach all of them.
//
// Unlike assets, this lands inside mpd's own /var/lib/mpd/env — the
// directory mpd bind-mounts read-only into every runtime container, which
// is the only way the value reaches the tools that read it. mpd's
// --vm-setup seeds the same path from its own template when nothing is
// there, and never overwrites, so the two writers do not fight.
const remoteMpdEnvPath = "/var/lib/mpd/env/mpd-virt.env"

// pushMpdEnv copies the developer's env file to one box, reporting whether
// it changed anything.
//
// No file on the Mac is "nothing to do", not "remove it from the box" —
// the same rule the assets mirror follows, and here it also protects a
// sandbox box that was adopted later: its own hand-written file survives
// until the developer actually puts one on the Mac.
//
// Digest-guarded for the same reason as the LAN hosts file: this runs on
// every lifecycle verb, and the common case is that nothing moved.
func pushMpdEnv(ctx context.Context, t host.Target) (bool, error) {
	local := paths.MpdEnv()
	body, err := os.ReadFile(local)
	if err != nil {
		return false, nil
	}
	want := fmt.Sprintf("%x", sha256.Sum256(body))
	// `|| true` so a missing remote file is an empty digest rather than an
	// error: absent and stale are the same case, both "push it".
	got, err := t.Line(ctx, "sha256sum "+remoteMpdEnvPath+" 2>/dev/null | cut -d' ' -f1 || true")
	if err != nil {
		return false, err
	}
	if got == want {
		return false, nil
	}
	// Pushed as the dev user, not root: /var/lib/mpd/env is dev-owned, and
	// mpd's own --vm-setup writes there. A root-owned file would leave mpd
	// unable to seed a replacement if the Mac's copy later went away.
	if err := t.Install(ctx, local, remoteMpdEnvPath, "0644"); err != nil {
		return false, err
	}
	return true, nil
}

// syncMpdEnv is the best-effort wrapper the lifecycle verbs use. Like the
// assets push, this is the developer's own material: failing to carry it
// is never a reason to fail an adoption, a start, or an update.
//
// Nothing needs to be restarted or re-run afterwards. mpd re-reads the
// file per invocation, and the runtime sees it through a directory mount,
// so the next command inside the box already has the new values.
func syncMpdEnv(ctx context.Context, t host.Target, idPad string) {
	pushed, err := pushMpdEnv(ctx, t)
	if err != nil {
		fmt.Printf("  ⚠ mpd-virt.env push failed: %v\n    retry with: mpd-virt start %s\n", err, idPad)
		return
	}
	if pushed {
		pass("mpd-virt.env pushed → " + remoteMpdEnvPath)
	}
}
