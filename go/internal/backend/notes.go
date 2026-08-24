package backend

import (
	"context"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// Notes returns the VM's free-form notes as its backend records them — the
// Proxmox VM's Notes field (the API's config "description") today, and "" for
// every other backend: the laptop hypervisors carry no comparable per-VM note
// and a generic VM has no backend to ask. Best-effort like PowerState: any
// failure (backend unconfigured, API unreachable, VM unknown, no notes set) is
// "", never an error, so a listing degrades to a blank cell rather than break.
// The raw value may span several lines and hold markdown; trimming it to one
// display cell is the caller's job (see the list verb's shortNotes).
func Notes(ctx context.Context, id vmid.ID, be Backend) string {
	if be == Proxmox {
		return proxmoxNotes(ctx, id)
	}
	return ""
}
