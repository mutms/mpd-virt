package backend

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// CreateOpts carries what Create needs beyond the id and backend.
type CreateOpts struct {
	Image  string // base image to run (container backend), e.g. mpd-virt-container-apple
	Memory string // memory: container "10g", or a VM RAM like "8g" (utm)
	Disk   string // VM disk size, e.g. "80g" (utm); ignored by the container backend
	User   string // dev account to seed
	PubKey string // the public key to authorize, one line ("ssh-ed25519 …")
}

// Create provisions a fresh VM for the id through its backend and returns the
// IP it came up on, ready for adoption. Container runs the base image, waits
// for systemd, seeds the dev account + sudo + key, and reads the leased IP;
// utm materializes a fresh VM from the Debian cloud image (utm.go); proxmox
// clones the mpd-template VM and points its cloud-init at the VM's address
// (proxmox.go); libvirt defines a KVM VM from the amd64 cloud image on a
// Linux host (libvirt.go). Parallels needs a template clone (not yet). A
// generic VM is adopted, not created.
func Create(ctx context.Context, out io.Writer, id vmid.ID, be Backend, opts CreateOpts) (string, error) {
	switch be {
	case Container:
		return containerCreate(ctx, out, id, opts)
	case UTM:
		return utmCreate(ctx, out, id, opts)
	case Proxmox:
		return proxmoxCreate(ctx, out, id, opts)
	case Libvirt:
		return libvirtCreate(ctx, out, id, opts)
	case Parallels:
		return "", fmt.Errorf("create is not implemented for the %s backend yet (needs a template clone + cloud-init) — create the box yourself, then `mpd-virt adopt`", be)
	default:
		return "", fmt.Errorf("a %s box is adopted, not created — use `mpd-virt adopt %s <IP> --backend %s`", be, id.String(), be)
	}
}

// DefaultContainerImage is the published, pre-baked base image
// `create --backend=container` runs (built from container/Containerfile):
// `container run` pulls it, so a fresh VM costs a pull rather than the
// apt run adoption would otherwise do. The tag is <debian point
// release>.<build>; bump the build number here when the image is
// republished because Debian has drifted far enough to slow adoption's
// package step again (see the Containerfile header). Only the Apple
// `container` runtime is supported; --image overrides it, e.g. for a
// locally built tag.
func DefaultContainerImage() string {
	return "ghcr.io/mutms/mpd-virt-container-apple:13.6.1" // darwin/Apple `container`
}

// The guest's DNS needs no help from here. The container runtime writes
// /etc/resolv.conf (the vmnet resolver) on every boot; mpd's dnsmasq on the
// VM forwards to whatever that file says, and the VM resolves *.mpd.test
// from mpd's block in its own /etc/hosts. Nothing routes through
// systemd-resolved, so nothing has to be seeded into it.

// shellQuote single-quotes a value for safe use inside `sh -c`.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
