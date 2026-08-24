package backend

import (
	"fmt"
	"strings"
)

// Backend names the platform a VM runs on. It is supplied explicitly
// (--backend), not derived from the id: reachability and IP resolution are
// uniform now, so the id is a plain identifier that carries no platform
// meaning. The value is recorded so lifecycle commands that DO need the
// hypervisor — start, stop, delete — know which one owns the VM.
type Backend string

const (
	Generic   Backend = "generic"   // adopted / manually managed VM (demos, LAN)
	Parallels Backend = "parallels" // Parallels VM
	Container Backend = "container" // native Apple container
	UTM       Backend = "utm"       // UTM Desktop VM (macOS, osascript-driven)
	Proxmox   Backend = "proxmox"   // Proxmox VM behind warp
	Libvirt   Backend = "libvirt"   // libvirt/KVM VM on a Linux host
)

// backends is the closed set of valid values, in help/order.
var backends = []Backend{Generic, Parallels, Container, UTM, Proxmox, Libvirt}

// Parse validates a --backend value against the known set.
func Parse(s string) (Backend, error) {
	for _, b := range backends {
		if s == string(b) {
			return b, nil
		}
	}
	if s == "" {
		return "", fmt.Errorf("a backend is required (%s)", List())
	}
	return "", fmt.Errorf("unknown backend %q — must be one of %s", s, List())
}

// DefaultBackend reports the backend to assume when --backend is omitted on a
// first adopt/create, and whether one is configured at all. Only proxmox can
// claim it today: it is the only backend with a config file, and the one where
// you routinely stand up (or re-adopt) many VMs at once — `DEFAULT=YES` in
// proxmox.env opts in. With nothing configured there is no default and the
// caller requires the flag, exactly as before. The single return point keeps
// the "which backend is the default" policy here, not scattered across verbs.
func DefaultBackend() (Backend, bool) {
	if proxmoxIsDefault() {
		return Proxmox, true
	}
	return "", false
}

// List renders the valid backends for flag help and error messages.
func List() string {
	names := make([]string, len(backends))
	for i, b := range backends {
		names[i] = string(b)
	}
	return strings.Join(names, ", ")
}
