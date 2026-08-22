package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/paths"
)

// The developer's own scripts and files — private hacks, experiments, site
// wiring — mirrored from ~/.mpd-virt/assets into every box mpd-virt owns.
// mpd-virt carries them and nothing more: it never runs them, never reads
// them, and has no opinion on what is in there. That is the point — a
// one-off fix belongs in the developer's own tree, not as a feature here.
//
// Deliberately NOT under /opt/mpd: that is mpd's git checkout, which
// `mpd --vm-upgrade` pulls, so anything dropped in there fights the
// update. /opt/mpd-virt is mpd-virt's own FHS slot on the box.
//
// mpd bind-mounts /opt/mpd read-only into every runtime container but knows
// nothing about /opt/mpd-virt, so these stay VM-only and do not appear
// inside containers.
const (
	remoteAssetsDir  = "/opt/mpd-virt/assets"
	assetsStagedPath = "/tmp/mpd-virt-assets.sh"
	assetsProfile    = "/etc/profile.d/mpd-virt-assets.sh"
)

// assetsProfileBody puts the assets' bin/ on PATH for interactive shells.
//
// Appended, never prepended: a script here must not silently shadow a
// system binary. Guarded by the directory test so the drop-in is inert
// when assets carry no bin/, and written as an if-block rather than a
// `[ -d … ] &&` one-liner, which would return non-zero and upset a login
// shell running under `set -e`.
const assetsProfileBody = `# Managed by mpd-virt — do not edit; the source of truth is
# ~/.mpd-virt/assets on the developer's Mac.
if [ -d ` + remoteAssetsDir + `/bin ]; then
    PATH="$PATH:` + remoteAssetsDir + `/bin"
fi
`

// pushAssets mirrors the developer's assets onto one box and puts their
// bin/ on PATH. It reports whether anything was pushed.
//
// No assets directory on the Mac is "nothing to do" — not "remove them
// from the box". Absence is the default state for every VM that never
// wanted any, and making it destructive would mean a Mac that lost
// ~/.mpd-virt/assets silently wiping every box on the next start.
func pushAssets(ctx context.Context, t host.Target) (bool, error) {
	local := paths.Assets()
	fi, err := os.Stat(local)
	if err != nil || !fi.IsDir() {
		return false, nil
	}
	if err := t.MirrorTree(ctx, local, remoteAssetsDir); err != nil {
		return false, err
	}
	// The PATH drop-in is written on every push rather than once at
	// adoption: it is two cheap commands, and it means a box that predates
	// this (or had /etc wiped by a reinstall) heals on the next start.
	if err := t.WriteRemote(ctx, assetsProfileBody, assetsStagedPath, "0644"); err != nil {
		return false, err
	}
	cmd := fmt.Sprintf("sudo install -o root -g root -m 0644 %s %s && rm -f %s",
		assetsStagedPath, assetsProfile, assetsStagedPath)
	if r, err := t.Run(ctx, cmd); err != nil {
		return false, err
	} else if r.Failed() {
		return false, fmt.Errorf("install %s: %s", assetsProfile, strings.TrimSpace(r.Stderr))
	}
	return true, nil
}

// syncAssets is the best-effort wrapper the lifecycle verbs use. The
// assets are the developer's own material, so a failure to push them is
// never a reason to fail an adoption, a start, or an update — it warns and
// says how to retry.
func syncAssets(ctx context.Context, t host.Target, idPad string) {
	pushed, err := pushAssets(ctx, t)
	if err != nil {
		fmt.Printf("  ⚠ assets push failed: %v\n    retry with: mpd-virt start %s\n", err, idPad)
		return
	}
	if pushed {
		pass("assets mirrored → " + remoteAssetsDir + "  (bin/ on PATH)")
	}
}
