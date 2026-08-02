package backend

import (
	"strings"
	"testing"

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

// inspectFixture is real `container inspect mpd-181` output (trimmed): the
// live address is under status.networks[].ipv4Address in CIDR form, while
// configuration.networks has no address — so the parser must read status and
// strip the mask.
const inspectFixture = `[
  {
    "configuration" : {
      "id" : "mpd-181",
      "networks" : [ { "network" : "default", "options" : { "hostname" : "mpd-181" } } ]
    },
    "id" : "mpd-181",
    "status" : {
      "networks" : [
        {
          "hostname" : "mpd-181",
          "ipv4Address" : "192.168.64.26/24",
          "ipv4Gateway" : "192.168.64.1",
          "macAddress" : "fe:82:96:82:1b:fd",
          "network" : "default"
        }
      ],
      "state" : "running"
    }
  }
]`

func TestParseContainerIP(t *testing.T) {
	got, err := parseContainerIP(inspectFixture)
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.168.64.26" {
		t.Errorf("parseContainerIP = %q, want 192.168.64.26 (from status.networks, mask stripped)", got)
	}
}

// prlctlFixture is real `prlctl list mpd-130 -f --json` output: the address is
// the bare "ip_configured" string (no CIDR mask).
const prlctlFixture = `[
    {
        "uuid": "bb586bcf-703d-47f9-b902-b60d54504c2a",
        "status": "running",
        "ip_configured": "10.211.55.130",
        "name": "mpd-130"
    }
]`

func TestParseParallelsIP(t *testing.T) {
	got, err := parseParallelsIP(prlctlFixture)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.211.55.130" {
		t.Errorf("parseParallelsIP = %q, want 10.211.55.130", got)
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
