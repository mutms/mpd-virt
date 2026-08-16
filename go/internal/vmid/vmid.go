// Package vmid derives everything predictable about a box from its id NNN:
// the hostname mpd-<NNN> and the DNS zone <NNN>.mpd.test.
//
// The id is a plain identifier over 100-254, not a class marker. Reachability
// and IP resolution are uniform, so the id carries no backend meaning — which
// platform a box runs on is an explicit --backend value (see
// internal/backend). The range starts at 100 on purpose: every id is exactly
// three digits, so the very same characters appear everywhere the id does —
// the hostname, the DNS zone, the registry directory, the third octet of the
// box's internal 10.163.<NNN>.0/24 subnet, the last octet of its LAN address
// on the proxmox/utm backends, and the Proxmox VMID (which Proxmox itself
// starts at 100). There is no padded/unpadded duality anywhere, and no
// special ids — sandbox VMs use ordinary ids from the same range.
//
// The host IP is deliberately NOT here either — it is found by name
// (resolving mpd-<NNN>) with the last known address as a fallback, or given
// to adopt explicitly. See internal/backend.
package vmid

import (
	"fmt"
	"strconv"
)

// ID is a validated box id, 100-254.
type ID int

// Parse validates an id string and returns it. An id is 100-254: it doubles
// as the box's subnet octet (255 is the broadcast address), and starting at
// 100 keeps every id exactly three digits — the same string everywhere.
func Parse(s string) (ID, error) {
	n, err := strconv.Atoi(s)
	if err != nil || strconv.Itoa(n) != s {
		return 0, fmt.Errorf("id %q must be a plain number (e.g. 130)", s)
	}
	if n < 100 || n > 254 {
		return 0, fmt.Errorf("id %d out of range — a box id is 100-254", n)
	}
	return ID(n), nil
}

// String is the id's three digits ("135") — the one form used everywhere:
// hostname, DNS zone, registry directory, and both address octets.
func (id ID) String() string { return strconv.Itoa(int(id)) }

// Name is the box's hostname, mpd-<NNN>.
func (id ID) Name() string { return "mpd-" + id.String() }

// Zone is the box's DNS zone, <NNN>.mpd.test.
func (id ID) Zone() string { return id.String() + ".mpd.test" }
