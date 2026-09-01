package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/config"
	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/paths"
)

// Third-party installers the developer keeps on the Mac — a JetBrains IDE
// backend is the case this exists for — pushed to the VMs that can run
// them. Held apart from the assets overlay for two reasons: they are
// gigabytes rather than dotfiles, and they are architecture-specific, so
// only ~/.mpd-virt/installers/<arch>/ is pushed and the VM never sees a
// binary it cannot execute.
//
// The VM side is flat: everything lands directly in
// /opt/mpd/assets/installers/, so a tool there opens one path and needs no
// architecture logic of its own.
const (
	installersDir = mpdAssetsDir + "/installers"

	// installersDigestFile records, on the VM, the digest of the set it
	// carries. Same purpose as digestFile and separate from it: the two
	// are pushed independently.
	installersDigestFile = mpdRepoDir + "/.git/info/mpd-virt-installers.manifest"
)

// pushInstallers copies the arch's installers to the VM when they differ
// from what it already has. Absent directory means nothing to do.
func pushInstallers(ctx context.Context, t host.Target, arch string) (assetState, error) {
	local := paths.InstallersFor(arch)
	fi, err := os.Stat(local)
	if err != nil || !fi.IsDir() {
		return assetsNone, nil
	}
	rels, size, err := assetRelPaths(local)
	if err != nil {
		return assetsNone, err
	}
	if len(rels) == 0 {
		return assetsNone, nil
	}

	digest, err := assetDigest(local, rels)
	if err != nil {
		return assetsNone, err
	}
	if r, err := t.Run(ctx, "cat "+installersDigestFile+" 2>/dev/null || true"); err == nil &&
		strings.TrimSpace(r.Stdout) == strings.TrimSpace(digest) && digest != "" {
		return assetsCurrent, nil
	}

	staging, err := t.Line(ctx, "mktemp -d")
	if err != nil {
		return assetsNone, err
	}
	if staging == "" {
		return assetsNone, fmt.Errorf("mktemp -d returned nothing")
	}
	defer func() { _, _ = t.Run(ctx, "rm -rf "+staging) }()

	// Always metered: these are the payloads scpMeterFrom was written for,
	// and a silent multi-gigabyte copy looks like a hung adoption.
	fmt.Printf("  ▶ installers (%s): %d file(s), %s — copying to the VM\n",
		arch, len(rels), humanBytes(size))
	if err := t.ScpTreeLive(ctx, local, staging+"/installers"); err != nil {
		return assetsNone, err
	}
	if err := t.WriteRemote(ctx, digest, staging+"/digest", "0644"); err != nil {
		return assetsNone, err
	}

	if r, err := t.Run(ctx, installersScript(staging)); err != nil {
		return assetsNone, err
	} else if r.Failed() {
		return assetsNone, fmt.Errorf("installers %s: %s", installersDir, strings.TrimSpace(r.Stderr))
	}
	return assetsPushed, nil
}

// installersScript replaces the directory wholesale. Unlike the assets
// overlay it shares no space with mpd's own files, so there is nothing to
// merge and a rename on the Mac must not leave the old name behind.
func installersScript(staging string) string {
	return "set -eu\n" +
		"STAGED=" + staging + "\n" +
		"DIR=" + installersDir + "\n" +
		"DIGEST=" + installersDigestFile + "\n" +
		`
rm -rf "$DIR"
mkdir -p "$DIR"
find "$STAGED/installers" -name .DS_Store -delete 2>/dev/null || true
cp -a "$STAGED/installers/." "$DIR/"
chmod -R u+rwX,go-w "$DIR"

# Last: the digest is only true once the copy landed.
if [ -d "$(dirname "$DIGEST")" ]; then
    cp "$STAGED/digest" "$DIGEST"
fi
`
}

// syncInstallers is the best-effort wrapper the lifecycle verbs use. Like
// the assets overlay, this is the developer's own material: failing to
// carry it never fails an adoption or an update.
func syncInstallers(ctx context.Context, t host.Target, backend, idPad string) {
	arch, err := config.BackendArch(backend)
	if err != nil {
		fmt.Printf("  ⚠ installers skipped: %v\n", err)
		return
	}
	state, err := pushInstallers(ctx, t, arch)
	if err != nil {
		fmt.Printf("  ⚠ installers push failed: %v\n    retry with: mpd-virt update %s\n", err, idPad)
		return
	}
	switch state {
	case assetsPushed:
		pass("installers pushed → " + installersDir + "  (" + arch + ")")
	case assetsCurrent:
		pass("installers already current → " + installersDir + "  (" + arch + ")")
	}
}
