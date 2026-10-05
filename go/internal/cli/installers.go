package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
const installersDir = mpdAssetsDir + "/installers"

// pushInstallers copies to the VM the arch's installers it does not have
// yet. Presence is by name and nothing else: no hashing of gigabytes on
// every verb, and a VM that made an archive itself (mpd's *-archive-app
// writes straight into this directory) is not sent it back. Nothing is ever
// removed or replaced: an archive is a seed, and the IDE it installs updates
// itself.
// Absent directory on the Mac means nothing to do.
func pushInstallers(ctx context.Context, t host.Target, arch string) (assetState, error) {
	local := paths.InstallersFor(arch)
	entries, err := os.ReadDir(local)
	if err != nil {
		return assetsNone, nil
	}
	var names []string
	for _, e := range entries {
		// Flat by design; the name goes to a remote shell, so it must be plain.
		if e.Type().IsRegular() && plainName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return assetsNone, nil
	}

	r, err := t.Run(ctx, "mkdir -p "+installersDir+" && ls -1 "+installersDir)
	if err != nil {
		return assetsNone, err
	}
	if r.Failed() {
		return assetsNone, fmt.Errorf("listing %s: %s", installersDir, strings.TrimSpace(r.Stderr))
	}
	have := map[string]bool{}
	for _, name := range strings.Split(r.Stdout, "\n") {
		have[strings.TrimSpace(name)] = true
	}

	state := assetsCurrent
	for _, name := range names {
		if have[name] {
			continue
		}
		// Metered: a silent multi-gigabyte copy looks like a hung adoption.
		// Under a temporary name first, so an interrupted copy is not taken
		// for the file on the next run.
		fmt.Printf("  ▶ installer %s (%s) — copying to the VM\n", name, arch)
		dest := installersDir + "/" + name
		if err := t.ScpFileLive(ctx, filepath.Join(local, name), dest+".partial"); err != nil {
			return assetsNone, err
		}
		if r, err := t.Run(ctx, "mv -f "+dest+".partial "+dest); err != nil {
			return assetsNone, err
		} else if r.Failed() {
			return assetsNone, fmt.Errorf("installer %s: %s", name, strings.TrimSpace(r.Stderr))
		}
		state = assetsPushed
	}
	return state, nil
}

// plainName is a file name safe to pass to a remote shell unquoted. Dotfiles
// (.DS_Store) and scp's own .partial leftovers fall outside it.
var plainName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

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
