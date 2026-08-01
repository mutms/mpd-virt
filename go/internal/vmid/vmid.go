// Package vmid derives everything predictable about a box from its id NNN:
// the hostname mpd-<NNN>, the class (which reachability block it falls in),
// and the DNS zone.
//
// The host IP is deliberately NOT here. Under Apple's vmnet a container
// cannot hold an address it wasn't leased, so the IP is assigned (read
// back from `container inspect`) or supplied to takeover — never derived
// from the id. See docs/proposals/apple-container-backend.md §4.
package vmid

import (
	"fmt"
	"strconv"
)

// ID is a validated box id in the managed range.
type ID int

// Class is how the Mac reaches a box, which the id's block decides.
type Class string

const (
	// General — 128-159: adopted VMs (formerly Parallels). IP supplied.
	General Class = "general"
	// Container — 160-191: native Apple containers. IP leased by vmnet.
	Container Class = "container"
	// Proxmox — 192-223: Proxmox VMs. IP derived from the id.
	Proxmox Class = "proxmox"
)

// Parse validates an id string and returns it. It must be a number in a
// managed class block (128-223); the free blocks (100-127, 224-255) are
// not adoptable.
func Parse(s string) (ID, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("id %q is not a number", s)
	}
	id := ID(n)
	if _, ok := id.class(); !ok {
		return 0, fmt.Errorf("id %d is outside the managed range 128-223", n)
	}
	return id, nil
}

// Pad is the id as a zero-padded three-digit string ("135", "042") — the
// form used for hostnames, the registry directory, and ssh aliases.
func (id ID) Pad() string { return fmt.Sprintf("%03d", int(id)) }

// Name is the box's hostname, mpd-<NNN>, zero-padded to three digits to
// match the sibling mpd's vmName.
func (id ID) Name() string { return "mpd-" + id.Pad() }

// Zone is the box's DNS zone, <NNN>.mpd.test.
func (id ID) Zone() string { return fmt.Sprintf("%d.mpd.test", int(id)) }

// Class returns the reachability class of the id.
func (id ID) Class() Class {
	c, _ := id.class()
	return c
}

func (id ID) class() (Class, bool) {
	switch n := int(id); {
	case n >= 128 && n <= 159:
		return General, true
	case n >= 160 && n <= 191:
		return Container, true
	case n >= 192 && n <= 223:
		return Proxmox, true
	default:
		return "", false
	}
}
