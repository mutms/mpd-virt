// Package vmid derives everything predictable about a box from its id NNN:
// the hostname mpd-<NNN> and the DNS zone <NNN>.mpd.test.
//
// The id is a plain identifier over 001-254, not a class marker. Reachability
// and IP resolution are uniform now, so the id carries no backend meaning —
// which platform a box runs on is an explicit --backend value (see
// internal/backend), not something read off the id's range. Its canonical
// form is the zero-padded three-digit string (mpd-001, 001.mpd.test); the raw
// integer survives only because it doubles as the third octet of the box's
// internal 10.163.<NNN>.0/24 subnet.
//
// The host IP is deliberately NOT here either — it is found by name
// (resolving mpd-<NNN>) with the last known address as a fallback, or given
// to takeover explicitly. See internal/backend.
package vmid

import (
	"fmt"
	"strconv"
)

// ID is a validated box id: the third octet of 10.163.<NNN>.0/24, so 1-254.
type ID int

// Parse validates an id string and returns it. The id is the box's subnet
// octet, so it must be 1-254 (0 is the network address, 255 the broadcast).
// Unpadded input is accepted ("5" == "005"); the canonical form is always
// padded.
func Parse(s string) (ID, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("id %q must be digits (e.g. 001, 130)", s)
	}
	if n < 1 || n > 254 {
		return 0, fmt.Errorf("id %d out of range — a box id is 001-254 (the third octet of 10.163.<NNN>.0/24)", n)
	}
	return ID(n), nil
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
