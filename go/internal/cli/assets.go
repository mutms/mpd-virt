package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/paths"
)

// The developer's own tools and files — private hacks, experiments, site
// wiring — overlaid from ~/.mpd-virt/assets onto mpd's own asset tree at
// /opt/mpd/assets on every VM mpd-virt owns. mpd-virt carries them and
// nothing more: it never runs them, never reads them, has no opinion on
// what is in there. A one-off fix belongs in the developer's own tree, not
// as a feature here.
//
// They land in /opt/mpd/assets so mpd's own wiring carries them for free:
// a tool in vm/bin is on the VM's PATH, a tool in runtime/bin is on the
// runtime containers' PATH (through the read-only /opt/mpd mount), exactly
// like mpd's own tools — one search path, VM and runtimes, no second
// profile.d drop-in. The Mac layout mirrors mpd's: a VM tool goes in
// ~/.mpd-virt/assets/vm/bin, a runtime tool in ~/.mpd-virt/assets/runtime/bin.
//
// An overlay, not a mirror: mpd's own files under /opt/mpd/assets are never
// touched, and the files this drops are recorded in a managed block in
// /opt/mpd/.git/info/exclude — so they stay out of the checkout's `git
// status`, and the block tells the next push which files to remove when the
// developer deletes one on the Mac. Overlaying into a git checkout is safe
// because mpd upgrades with `git pull --ff-only`, which never touches
// untracked files. A dev tool must not share a name with one of mpd's own
// under the same path — that would overwrite a tracked file rather than add.
const (
	mpdRepoDir   = "/opt/mpd"
	mpdAssetsDir = mpdRepoDir + "/assets"

	excludeBegin = "# BEGIN mpd-virt dev assets (managed — do not edit)"
	excludeEnd   = "# END mpd-virt dev assets"
)

// pushAssets overlays the developer's assets onto one VM's /opt/mpd/assets.
// It reports whether any file was pushed.
//
// No assets directory on the Mac is "nothing to do" — not "remove them from
// the VM". Absence is the default state for every VM that never wanted any,
// and making it destructive would mean a Mac that lost ~/.mpd-virt/assets
// silently wiping every VM's overlay on the next start. An *empty* directory
// is different: it is a deliberate "I removed my tools", so the overlay runs
// and clears whatever a prior push left.
func pushAssets(ctx context.Context, t host.Target) (bool, error) {
	local := paths.Assets()
	fi, err := os.Stat(local)
	if err != nil || !fi.IsDir() {
		return false, nil
	}
	rels, err := assetRelPaths(local)
	if err != nil {
		return false, err
	}

	staging, err := t.Line(ctx, "mktemp -d")
	if err != nil {
		return false, err
	}
	if staging == "" {
		return false, fmt.Errorf("mktemp -d returned nothing")
	}
	defer func() { _, _ = t.Run(ctx, "rm -rf "+staging) }()

	// The tree the developer keeps, plus a manifest (what to make executable
	// and normalise) and the fresh exclude block (what to record).
	if err := t.ScpTree(ctx, local, staging+"/assets"); err != nil {
		return false, err
	}
	manifest := ""
	if len(rels) > 0 {
		manifest = strings.Join(rels, "\n") + "\n"
	}
	if err := t.WriteRemote(ctx, manifest, staging+"/manifest", "0644"); err != nil {
		return false, err
	}
	if len(rels) > 0 {
		if err := t.WriteRemote(ctx, excludeBlock(rels), staging+"/exclude.block", "0644"); err != nil {
			return false, err
		}
	}

	if r, err := t.Run(ctx, overlayScript(staging)); err != nil {
		return false, err
	} else if r.Failed() {
		return false, fmt.Errorf("overlay %s: %s", mpdAssetsDir, strings.TrimSpace(r.Stderr))
	}
	return len(rels) > 0, nil
}

// assetRelPaths lists the developer's files as slash-separated paths
// relative to the assets root (vm/bin/foo, runtime/bin/bar). Directories and
// macOS litter are left out — they carry no meaning in the manifest.
func assetRelPaths(root string) ([]string, error) {
	var rels []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() == ".DS_Store" {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	return rels, err
}

// excludeBlock renders the managed .git/info/exclude region: the markers
// around one repo-relative /assets/… pattern per dropped file.
func excludeBlock(rels []string) string {
	var b strings.Builder
	b.WriteString(excludeBegin + "\n")
	for _, r := range rels {
		b.WriteString("/assets/" + r + "\n")
	}
	b.WriteString(excludeEnd + "\n")
	return b.String()
}

// overlayScript removes the files the previous overlay dropped (recorded in
// the managed exclude block), copies the current tree over mpd's assets
// additively, makes bin/ tools executable, and re-records the block. It
// runs as the dev user — /opt/mpd is dev-owned — so no sudo is involved.
func overlayScript(staging string) string {
	return "set -eu\n" +
		"STAGED=" + staging + "\n" +
		"ASSETS=" + mpdAssetsDir + "\n" +
		"GITDIR=" + mpdRepoDir + "/.git\n" +
		"EXCL=\"$GITDIR/info/exclude\"\n" +
		"BEG='" + excludeBegin + "'\n" +
		"END='" + excludeEnd + "'\n" +
		`
# 1. Remove what the last overlay dropped, pruning dirs that go empty —
#    mpd's own populated dirs are kept.
if [ -f "$EXCL" ]; then
    awk -v b="$BEG" -v e="$END" '$0==b{f=1;next} $0==e{f=0} f' "$EXCL" \
    | while IFS= read -r p; do
        case "$p" in
            /assets/*)
                rm -f "/opt/mpd$p"
                rmdir -p --ignore-fail-on-non-empty "$(dirname "/opt/mpd$p")" 2>/dev/null || true
                ;;
        esac
      done
fi

# 2. Overlay the current tree, additively, onto mpd's assets.
find "$STAGED/assets" -name .DS_Store -delete 2>/dev/null || true
cp -a "$STAGED/assets/." "$ASSETS/"

# 3. Normalise perms and make bin/ tools executable, from the manifest so
#    only the dropped files are touched — never mpd's own.
while IFS= read -r rel; do
    [ -n "$rel" ] || continue
    f="$ASSETS/$rel"
    chmod u+rwX,go-w "$f" 2>/dev/null || true
    case "$rel" in
        */bin/*) chmod u+x "$f" 2>/dev/null || true ;;
    esac
done < "$STAGED/manifest"

# 4. Re-record the managed block in the checkout's local exclude, so the
#    dropped files stay out of git status. Skipped off a git checkout.
if [ -d "$GITDIR" ]; then
    mkdir -p "$GITDIR/info"
    tmp="$EXCL.mpd-virt.new"
    if [ -f "$EXCL" ]; then
        awk -v b="$BEG" -v e="$END" '$0==b{s=1} s!=1{print} $0==e{s=0}' "$EXCL" > "$tmp"
    else
        : > "$tmp"
    fi
    if [ -f "$STAGED/exclude.block" ]; then
        cat "$STAGED/exclude.block" >> "$tmp"
    fi
    mv "$tmp" "$EXCL"
fi
`
}

// syncAssets is the best-effort wrapper the lifecycle verbs use. The assets
// are the developer's own material, so a failure to push them is never a
// reason to fail an adoption or an update — it warns and says how to retry.
func syncAssets(ctx context.Context, t host.Target, idPad string) {
	pushed, err := pushAssets(ctx, t)
	if err != nil {
		fmt.Printf("  ⚠ assets overlay failed: %v\n    retry with: mpd-virt update %s\n", err, idPad)
		return
	}
	if pushed {
		pass("assets overlaid → " + mpdAssetsDir + "  (vm/bin, runtime/bin on PATH)")
	}
}
