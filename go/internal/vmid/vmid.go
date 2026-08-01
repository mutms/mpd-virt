// Package vmid derives everything predictable about a box from its id NNN:
// the hostname mpd-<NNN>, the class (which reachability block it falls in),
// and the DNS zone.
//
// The host IP is deliberately NOT here. How a box's IP is found depends on
// its class: derived from the id for Proxmox, looked up for Parallels
// (prlctl) and native containers (`container inspect`), or supplied to
// takeover for generic adopted boxes. See internal/backend.
//
// The id is an identifier, not a number — its canonical form is the
// zero-padded three-digit string (mpd-001, 001.mpd.test). The raw integer
// value survives only because it doubles as the third octet of the box's
// internal 10.163.<NNN>.0/24 subnet.
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
	// Generic — 001-064: manually registered / adopted boxes (demos like
	// mpd-001). IP is supplied to takeover; there is nothing to look up.
	Generic Class = "generic"
	// Parallels — 128-159: Parallels VMs. IP is dynamic (DHCP), looked up
	// via prlctl.
	Parallels Class = "parallels"
	// Container — 160-191: native Apple / WSL containers. IP leased by
	// vmnet, read via `container inspect`.
	Container Class = "container"
	// Proxmox — 192-223: Proxmox VMs. IP derived from the id (10.212.56.<NNN>).
	Proxmox Class = "proxmox"
)

// Parse validates an id string and returns it. It must fall in a managed
// class block (001-064 generic, 128-159 parallels, 160-191 container,
// 192-223 proxmox); the gaps (065-127, 224-254) are reserved. Unpadded
// input is accepted ("5" == "005"); the canonical form is always padded.
func Parse(s string) (ID, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("id %q must be digits (e.g. 001, 130)", s)
	}
	id := ID(n)
	if _, ok := id.class(); !ok {
		return 0, fmt.Errorf("id %d is not in a managed block (001-064, 128-159, 160-191, 192-223)", n)
	}
	return id, nil
}

// Pad is the id as a zero-padded three-digit string ("135", "042") — the
// form used for hostnames, the registry directory, and ssh aliases.
func (id ID) Pad() string { return fmt.Sprintf("%03d", int(id)) }

// Name is the box's hostname, mpd-<NNN>, zero-padded to three digits to
// match the sibling mpd's vmName.
func (id ID) Name() string { return "mpd-" + id.Pad() }

// Zone is the box's DNS zone, <NNN>.mpd.test — zero-padded, matching the
// sibling mpd's net.Zone(). (For ids >= 100 padded and unpadded coincide.)
func (id ID) Zone() string { return id.Pad() + ".mpd.test" }

// Class returns the reachability class of the id.
func (id ID) Class() Class {
	c, _ := id.class()
	return c
}

func (id ID) class() (Class, bool) {
	switch n := int(id); {
	case n >= 1 && n <= 64:
		return Generic, true
	case n >= 128 && n <= 159:
		return Parallels, true
	case n >= 160 && n <= 191:
		return Container, true
	case n >= 192 && n <= 223:
		return Proxmox, true
	default:
		return "", false
	}
}
