// Package paths holds the host-side filesystem locations mpd-virt owns
// under ~/.mpd-virt/.
//
// MPD_VIRT_ROOT overrides the root, which keeps tests (and dry-runs) out
// of the developer's real ~/.mpd-virt.
package paths

import (
	"os"
	"path/filepath"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// Root is ~/.mpd-virt (or $MPD_VIRT_ROOT) — everything mpd-virt owns on
// the host. Holds conf/ (identity, survives delete) and per-box dirs.
func Root() string {
	if r := os.Getenv("MPD_VIRT_ROOT"); r != "" {
		return r
	}
	return filepath.Join(home(), ".mpd-virt")
}

// Conf is ~/.mpd-virt/conf — identity that survives `delete` (CA, certs).
func Conf() string { return filepath.Join(Root(), "conf") }

// CARoot is ~/.mpd-virt/conf/caroot — the root CA keypair.
func CARoot() string { return filepath.Join(Conf(), "caroot") }

// ProxmoxEnv is ~/.mpd-virt/conf/backends/proxmox.env — the Proxmox API
// endpoint + token the proxmox backend drives power through (docs/PROXMOX.md).
func ProxmoxEnv() string { return filepath.Join(Conf(), "backends", "proxmox.env") }

// Servers is ~/.mpd-virt/servers — LAN service registry (one dir per host).
func Servers() string { return filepath.Join(Root(), "servers") }

// Assets is ~/.mpd-virt/assets — the developer's own scripts and files,
// mirrored into every box at /opt/mpd-virt/assets. Optional: absent means
// mpd-virt pushes nothing and leaves whatever a box already has. This Mac
// is the source of truth; the in-VM copy is root-owned and read-only.
func Assets() string { return filepath.Join(Root(), "assets") }

// LanHosts is ~/.mpd-virt/conf/lan-hosts — the rendered hosts(5) file that
// `server sync` pushes into every VM so containers resolve LAN names too.
func LanHosts() string { return filepath.Join(Conf(), "lan-hosts") }

// MpdEnv is ~/.mpd-virt/mpd-virt.env — the developer's own MPD_* defaults,
// pushed into every box at /var/lib/mpd/env/mpd-virt.env, where mpd layers
// it beneath each project's own mpd.env. Optional, like Assets: absent
// means mpd-virt pushes nothing and leaves whatever a box already has.
//
// At the root rather than under conf/ because it is the developer's file
// to write, not identity mpd-virt generates and manages on their behalf.
func MpdEnv() string { return filepath.Join(Root(), "mpd-virt.env") }

// CloudImages is ~/.mpd-virt/conf/cloud-images — the cached cloud-image
// archive(s) the UTM (and future cloud-init) backends materialize VMs from.
func CloudImages() string { return filepath.Join(Conf(), "cloud-images") }

// UTMStaging is ~/.mpd-virt/conf/utm-staging/<name> — where a UTM VM's disk
// + cidata seed are built before UTM imports them into its own bundle.
func UTMStaging(name string) string { return filepath.Join(Conf(), "utm-staging", name) }

// ProxySocket is ~/.mpd-virt/proxy/socket — mpd-proxy's control socket.
// mpd-proxy creates the proxy/ dir (user-owned, 0700) and binds the socket
// there; it derives the same path from the sudo user's home and knows
// nothing of MPD_VIRT_ROOT, so under a relocated root (tests, dry-runs)
// there is simply no proxy. The socket is ephemeral: it dies with the proxy.
func ProxySocket() string { return filepath.Join(Root(), "proxy", "socket") }

// VMDir is ~/.mpd-virt/<NNN> — per-box bookkeeping.
func VMDir(id vmid.ID) string { return filepath.Join(Root(), id.String()) }

// VMEnv is ~/.mpd-virt/<NNN>/env — the registry entry for a box.
func VMEnv(id vmid.ID) string { return filepath.Join(VMDir(id), "env") }

func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}
