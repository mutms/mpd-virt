// Package paths holds the host-side filesystem locations mpd-virt owns
// under ~/.mpd-virt/.
//
// MPD_VIRT_TEST_ROOT relocates the whole tree — a test-only escape hatch (the
// TEST in the name says so) that keeps the suite out of the developer's real
// ~/.mpd-virt. It is not a supported way to run mpd-virt; production always
// uses ~/.mpd-virt.
package paths

import (
	"os"
	"path/filepath"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// Root is ~/.mpd-virt (or $MPD_VIRT_TEST_ROOT) — everything mpd-virt owns on
// the host. Holds conf/ (identity, survives remove) and per-VM dirs.
func Root() string {
	if r := os.Getenv("MPD_VIRT_TEST_ROOT"); r != "" {
		return r
	}
	return filepath.Join(home(), ".mpd-virt")
}

// Conf is ~/.mpd-virt/conf — identity that survives `remove` (CA, certs).
func Conf() string { return filepath.Join(Root(), "conf") }

// CARoot is ~/.mpd-virt/conf/caroot — the root CA keypair.
func CARoot() string { return filepath.Join(Conf(), "caroot") }

// BackendsDir is ~/.mpd-virt/backends — per-backend config files. One JSON file
// per backend that needs configuring (proxmox today); most backends need none.
// The host-side mirror of the internal/backends code layout.
func BackendsDir() string { return filepath.Join(Root(), "backends") }

// BackendConfig is ~/.mpd-virt/backends/<name>.json — one backend's own config,
// hand-written by the developer (the Proxmox API endpoint + token today). JSON
// so it reviews cleanly like vm.json; not under conf/, which is for what
// mpd-virt generates, not what you edit.
func BackendConfig(name string) string { return filepath.Join(BackendsDir(), name+".json") }

// Config is ~/.mpd-virt/config.json — mpd-virt's own host-side settings (the
// default backend today). Hand-written by the developer, JSON so it reviews
// cleanly like vm.json; at the root rather than conf/, which is for what
// mpd-virt generates, not what you edit.
func Config() string { return filepath.Join(Root(), "config.json") }

// Servers is ~/.mpd-virt/servers — LAN service registry (one dir per host).
func Servers() string { return filepath.Join(Root(), "servers") }

// Assets is ~/.mpd-virt/assets — the developer's own tools and files,
// overlaid onto mpd's own tree at /opt/mpd/assets on every VM (vm/bin on
// PATH, like mpd's own tools). Optional: absent means
// mpd-virt pushes nothing and leaves whatever a VM already has. This Mac is
// the source of truth; the in-VM copy is dev-owned and replaced on overlay.
func Assets() string { return filepath.Join(Root(), "assets") }

// LanHosts is ~/.mpd-virt/conf/lan-hosts — the rendered hosts(5) file that
// `server sync` pushes into every VM so containers resolve LAN names too.
func LanHosts() string { return filepath.Join(Conf(), "lan-hosts") }

// VMEnv is the developer's own general environment file, pushed into every
// VM at /var/lib/mpd/env/vm.env and sourced into its shells (ambient env,
// not an mpd.env config layer). Optional, like Assets: an absent file means
// mpd-virt pushes nothing and leaves whatever a VM already has.
//
// At the root rather than under conf/ because they are the developer's files
// to write, not identity mpd-virt generates and manages on their behalf.
func VMEnv() string { return filepath.Join(Root(), "vm.env") }

// CloudImages is ~/.mpd-virt/conf/cloud-images — the cached cloud-image
// archive(s) the UTM (and future cloud-init) backends materialize VMs from.
func CloudImages() string { return filepath.Join(Conf(), "cloud-images") }

// UTMStaging is ~/.mpd-virt/conf/utm-staging/<name> — where a UTM VM's disk
// + cidata seed are built before UTM imports them into its own bundle.
func UTMStaging(name string) string { return filepath.Join(Conf(), "utm-staging", name) }

// LibvirtDir is /var/lib/mpd-virt/<name> — a libvirt VM's disk + cidata
// seed. Not under ~/.mpd-virt: qemu runs as libvirt-qemu and a Debian home
// is 0700, so it could not open anything there. Created once by hand
// (docs/libvirt.md), dev-user-owned.
func LibvirtDir(name string) string { return filepath.Join("/var/lib/mpd-virt", name) }

// ProxySocket is ~/.mpd-virt/proxy/socket — mpd-proxy's control socket.
// mpd-proxy creates the proxy/ dir (user-owned, 0700) and binds the socket
// there; it derives the same path from the sudo user's home and knows
// nothing of MPD_VIRT_TEST_ROOT, so under a relocated root (tests, dry-runs)
// there is simply no proxy. The socket is ephemeral: it dies with the proxy.
func ProxySocket() string { return filepath.Join(Root(), "proxy", "socket") }

// VMDir is ~/.mpd-virt/<NNN> — per-VM bookkeeping.
func VMDir(id vmid.ID) string { return filepath.Join(Root(), id.String()) }

// VMRecord is ~/.mpd-virt/<NNN>/vm.json — the VM's registry record (identity,
// backend, address, user, and the cached backend notes), a pretty-printed
// JSON mirror of the internal registry.Entry so it opens and reviews cleanly
// in a Finder/editor. The one per-VM file OpenSSH must read raw is known_hosts;
// the CA lives beside this in ca/. See the layout in AGENTS.md.
func VMRecord(id vmid.ID) string { return filepath.Join(VMDir(id), "vm.json") }

// KnownHosts is ~/.mpd-virt/<NNN>/known_hosts — the VM's pinned ssh host
// key, recorded on first contact (adopt/create) under the stable
// HostKeyAlias mpd-<NNN> and refused if it ever changes. Per-VM so
// `remove` retires the pin with the VM and a re-created VM at the same
// id starts a fresh first-contact.
func KnownHosts(id vmid.ID) string { return filepath.Join(VMDir(id), "known_hosts") }

// EnsureKnownHosts is KnownHosts with the VM directory created: ssh
// records a first-contact key into the file but never creates parent
// directories, and the VM dir does not exist yet at the very first
// connection of an adoption or create.
func EnsureKnownHosts(id vmid.ID) string {
	_ = os.MkdirAll(VMDir(id), 0o700)
	return KnownHosts(id)
}

// EnsurePrivate walks ~/.mpd-virt and drops group/other permission bits
// everywhere: directories to 0700, regular files to owner-only (keeping the
// owner's execute bit — assets bin/ scripts carry it into the VMs). Nothing
// under the root is meant to be readable by another user — the CA keys and
// the proxmox token outright must not be. Run on every invocation; errors
// are ignored (a root-owned stray should not brick the CLI) and non-regular
// files (the mpd-proxy socket) are left alone.
func EnsurePrivate() {
	root := Root()
	if _, err := os.Stat(root); err != nil {
		return
	}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		switch {
		case d.IsDir():
			_ = os.Chmod(p, 0o700)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return nil
			}
			mode := info.Mode().Perm()&0o700 | 0o600
			if mode != info.Mode().Perm() {
				_ = os.Chmod(p, mode)
			}
		}
		return nil
	})
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}
