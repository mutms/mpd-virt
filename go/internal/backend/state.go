package backend

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// vmState is a box's power state as its backend reports it, normalized to the
// words the power path reasons about. Knowing it first is what keeps `start`
// from firing a power verb the hypervisor will only refuse — Parallels rejects
// `prlctl start` on a box that is not stopped, and the resulting error read
// like a failure when nothing was actually wrong.
type vmState string

const (
	// stUnknown is the honest answer whenever the backend does not tell us:
	// its CLI is not on this Mac (the box is powered elsewhere), it does not
	// know the box, or it reports a transitional state (starting/stopping)
	// that no power decision should be made from. The power verb is then
	// issued blind, exactly as it was before this check existed.
	stUnknown   vmState = ""
	stRunning   vmState = "running"
	stStopped   vmState = "stopped"
	stSuspended vmState = "suspended" // memory parked on disk
	stPaused    vmState = "paused"    // frozen but resident
)

// probeState is the package's one look at the outside world for state, a var
// only so tests can substitute it.
var probeState = queryState

// queryState asks the backend what state the box is in. Everything it runs is
// read-only and best-effort: any failure — CLI absent, box unknown, output we
// do not recognize — is stUnknown, never an error, because a state we cannot
// read must not stop a power verb from being tried.
func queryState(ctx context.Context, id vmid.ID, be Backend) vmState {
	switch be {
	case Parallels:
		// -a so stopped boxes are listed too; without it Parallels reports
		// only the running ones and every stopped box would look unknown.
		res, err := exec.Capture(ctx, exec.Cmd{Name: "prlctl", Args: []string{"list", id.Name(), "-a", "--json"}})
		if err != nil || res.Failed() {
			return stUnknown
		}
		return normalizeState(parseParallelsState(res.Stdout, id.Name()))
	case Container:
		res, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{"inspect", id.Name()}})
		if err != nil || res.Failed() {
			return stUnknown
		}
		return normalizeState(parseContainerState(res.Stdout))
	case UTM:
		return normalizeState(utmVMStatus(ctx, id.Name()))
	}
	return stUnknown
}

// normalizeState maps the backends' status words onto vmState. Only the settled
// states are recognized; anything else (a transitional "starting"/"stopping", a
// word a future release invents) stays unknown, so the caller falls back to
// issuing the power verb rather than acting on a guess.
func normalizeState(word string) vmState {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "running", "started": // prlctl / container say running, UTM says started
		return stRunning
	case "stopped":
		return stStopped
	case "suspended":
		return stSuspended
	case "paused":
		return stPaused
	}
	return stUnknown
}

// parseParallelsState pulls one box's "status" out of `prlctl list <name> -a
// --json`. The name is matched rather than the first entry taken: the same
// JSON shape comes back when Parallels lists every VM it knows, and reading
// another box's state would be worse than reading none.
func parseParallelsState(stdout, name string) string {
	var vms []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(stdout), &vms); err != nil {
		return ""
	}
	for _, vm := range vms {
		if strings.EqualFold(strings.TrimSpace(vm.Name), name) {
			return vm.Status
		}
	}
	return ""
}

// parseContainerState pulls the live state out of `container inspect` JSON —
// an array whose status.state is "running" or "stopped" (the same status
// object parseContainerIP reads the address from).
func parseContainerState(stdout string) string {
	var boxes []struct {
		Status struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(stdout), &boxes); err != nil {
		return ""
	}
	for _, b := range boxes {
		if b.Status.State != "" {
			return b.Status.State
		}
	}
	return ""
}
