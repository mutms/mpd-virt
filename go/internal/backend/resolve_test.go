package backend

import (
	"strings"
	"testing"

	"github.com/mutms/mpd-virt-macos/go/internal/vmid"
)

func mustID(t *testing.T, s string) vmid.ID {
	t.Helper()
	id, err := vmid.Parse(s)
	if err != nil {
		t.Fatalf("vmid.Parse(%q): %v", s, err)
	}
	return id
}

// Proxmox is the one class whose address is derived from the id, so it
// resolves with no hypervisor round-trip — the branch worth pinning.
func TestResolveIPProxmoxDerived(t *testing.T) {
	for _, tc := range []struct{ id, want string }{
		{"192", "10.212.56.192"},
		{"200", "10.212.56.200"},
		{"223", "10.212.56.223"},
	} {
		got, err := ResolveIP(t.Context(), mustID(t, tc.id))
		if err != nil {
			t.Fatalf("ResolveIP(%s): %v", tc.id, err)
		}
		if got != tc.want {
			t.Errorf("ResolveIP(%s) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

// A generic box has no discoverable address; resolution must fail with a
// message that tells the user to pass one, not silently guess.
func TestResolveIPGenericHasNoAddress(t *testing.T) {
	_, err := ResolveIP(t.Context(), mustID(t, "005"))
	if err == nil {
		t.Fatal("ResolveIP(005): want an error for a generic box, got nil")
	}
	if !strings.Contains(err.Error(), "takeover 005 <IP>") {
		t.Errorf("error should tell the user to pass an explicit IP, got: %v", err)
	}
}
