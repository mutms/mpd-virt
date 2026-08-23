package cli

import (
	"context"
	"testing"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

func mustID(t *testing.T, s string) vmid.ID {
	t.Helper()
	id, err := vmid.Parse(s)
	if err != nil {
		t.Fatalf("vmid.Parse(%q): %v", s, err)
	}
	return id
}

// stubPowerState substitutes the backend power probe for one test.
func stubPowerState(t *testing.T, state string) {
	t.Helper()
	orig := powerState
	powerState = func(context.Context, vmid.ID, backend.Backend) string { return state }
	t.Cleanup(func() { powerState = orig })
}

// A VM the backend reports off shows that power word verbatim and is never
// dialed — the whole point is to skip the SSH timeout a dead IP would blackhole.
// The blackhole IP proves it: were it dialed, the case would hang, not return
// instantly with the power word.
func TestEntryStateOffSkipsDial(t *testing.T) {
	for _, state := range []string{"stopped", "suspended", "paused"} {
		stubPowerState(t, state)
		e := registry.Entry{ID: mustID(t, "150"), IP: "10.255.255.1", Backend: "proxmox"}
		if got := entryState(context.Background(), e); got != state {
			t.Errorf("power state %q: entryState = %q, want %q (no dial)", state, got, state)
		}
	}
}

// A VM reported running is still dialed — running is not reachable. With no IP
// the dial resolves instantly to "?", which proves the running branch fell
// through to sshState rather than returning the power word.
func TestEntryStateRunningFallsThroughToDial(t *testing.T) {
	stubPowerState(t, "running")
	e := registry.Entry{ID: mustID(t, "151"), IP: "", Backend: "proxmox"}
	if got := entryState(context.Background(), e); got != "?" {
		t.Errorf("running VM should be dialed; with no IP entryState = %q, want %q", got, "?")
	}
}

// The backend that cannot report state — every generic VM, or a failed probe —
// reads as "" and falls through to the dial: the connect-first behaviour.
func TestEntryStateUnknownFallsThroughToDial(t *testing.T) {
	stubPowerState(t, "")
	e := registry.Entry{ID: mustID(t, "152"), IP: "", Backend: "generic"}
	if got := entryState(context.Background(), e); got != "?" {
		t.Errorf("unknown state should be dialed; with no IP entryState = %q, want %q", got, "?")
	}
}
