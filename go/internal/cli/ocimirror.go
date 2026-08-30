package cli

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/config"
	"github.com/mutms/mpd-virt/go/internal/host"
)

// The developer's OCI pull-through cache. When oci_mirror_location is set in
// the VM's backend file (~/.mpd-virt/backends/<backend>.json), the VM's podman
// is pointed at it for the registries mpd pulls from, so an image is fetched
// from upstream once and served from the LAN thereafter. Per backend because
// the cache is a property of the network the VMs live on: the Proxmox box at
// home shares a LAN with the cache, a laptop's own VMs on the road do not.
// The cache host is the developer's own (site specific); mpd-virt only
// carries the setting and writes the drop-in.
//
// A /etc/containers/registries.conf.d drop-in, not a code path: podman reads
// it natively, so image references stay unchanged and a pull falls back to
// upstream when the cache is down. No `insecure` — an mpd LAN service serves a
// cert the VM already trusts.
const registryMirrorPath = "/etc/containers/registries.conf.d/50-mpd-mirror.conf"

// mirrorUpstreams are the registries mpd's runtimes pull from; only these gain
// a mirror. Anything else a project pulls goes straight upstream.
var mirrorUpstreams = []string{"docker.io", "ghcr.io"}

// A cache location is host[:port][/path]; it lands in a TOML string and a sudo
// command, so nothing outside this charset is accepted.
var validMirror = regexp.MustCompile(`^[a-zA-Z0-9.-]+(:[0-9]{1,5})?(/[a-zA-Z0-9._/-]+)?$`)

// registryMirrorConf renders the drop-in mirroring each upstream to location.
func registryMirrorConf(location string) string {
	var b strings.Builder
	b.WriteString("# Managed by mpd-virt — do not edit.\n")
	b.WriteString("# Points podman at the OCI pull-through cache set as oci_mirror_location\n")
	b.WriteString("# in ~/.mpd-virt/backends/<backend>.json on the developer's Mac.\n")
	for _, up := range mirrorUpstreams {
		b.WriteString("\n[[registry]]\nlocation = \"" + up + "\"\n")
		b.WriteString("[[registry.mirror]]\nlocation = \"" + location + "\"\n")
	}
	return b.String()
}

// pushOCIMirror converges the mirror drop-in on one VM from its backend's
// config file: it writes the block when a location is set, and removes the
// managed file when it is not — so clearing the setting is honoured on the
// next lifecycle verb. Returns the location it applied, "" when none.
//
// Root-owned under /etc, so it is staged as the dev user and installed with
// sudo (`install -D` creates registries.conf.d if a bare image lacks it).
func pushOCIMirror(ctx context.Context, t host.Target, backend string) (string, error) {
	mirror, err := config.BackendMirror(backend)
	if err != nil {
		return "", err
	}
	loc := strings.TrimSpace(mirror)
	if loc == "" {
		// Not configured: drop any block a previous config left behind.
		_, _ = t.Run(ctx, "sudo rm -f "+registryMirrorPath)
		return "", nil
	}
	if !validMirror.MatchString(loc) {
		return "", fmt.Errorf("oci_mirror_location %q is not a valid host[:port] value", loc)
	}
	staged := "/tmp/mpd-mirror.conf"
	if err := t.WriteRemote(ctx, registryMirrorConf(loc), staged, "0644"); err != nil {
		return "", err
	}
	cmd := fmt.Sprintf("sudo install -D -o root -g root -m 0644 %s %s && rm -f %s",
		staged, registryMirrorPath, staged)
	if r, err := t.Run(ctx, cmd); err != nil {
		return "", err
	} else if r.Failed() {
		return "", fmt.Errorf("install %s: %s", registryMirrorPath, strings.TrimSpace(r.Stderr))
	}
	return loc, nil
}

// syncOCIMirror is the best-effort wrapper the lifecycle verbs use: a cache is
// a convenience, so a failure warns and never fails an adoption or an update.
func syncOCIMirror(ctx context.Context, t host.Target, backend, idPad string) {
	loc, err := pushOCIMirror(ctx, t, backend)
	if err != nil {
		fmt.Printf("  ⚠ podman mirror config failed: %v\n    retry with: mpd-virt update %s\n", err, idPad)
		return
	}
	if loc != "" {
		pass("podman mirror → " + loc + "  (" + strings.Join(mirrorUpstreams, ", ") + ")")
	}
}
