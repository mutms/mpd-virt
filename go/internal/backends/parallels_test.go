package backends

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// prlctlListFixture is real `prlctl list -a --json` output from a Mac running
// several VMs at once — the shape a per-name query returns too.
const prlctlListFixture = `[
    { "uuid": "b1c42d97", "status": "running",   "ip_configured": "-", "name": "macOS" },
    { "uuid": "bb586bcf", "status": "suspended", "ip_configured": "-", "name": "mpd-130" },
    { "uuid": "7d740193", "status": "stopped",   "ip_configured": "-", "name": "mpd-130-copy" },
    { "uuid": "5c85feb1", "status": "running",   "ip_configured": "-", "name": "mpd-160" },
    { "uuid": "edde6d3d", "status": "suspended", "ip_configured": "-", "name": "vscode" }
]`

// The VM's own status is read, not the first entry's — and a near-miss name like
// mpd-130-copy must not answer for mpd-130.
func TestParseParallelsState(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"mpd-160", "running"},
		{"mpd-130", "suspended"},
		{"mpd-130-copy", "stopped"},
		{"mpd-999", ""}, // not listed
	} {
		if got := parseParallelsState(prlctlListFixture, tc.name); got != tc.want {
			t.Errorf("parseParallelsState(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestParseParallelsStateGarbage(t *testing.T) {
	if got := parseParallelsState("not json", "mpd-160"); got != "" {
		t.Errorf("unparseable output should read as no state, got %q", got)
	}
}

// prlctlFixture is real `prlctl list mpd-130 -f --json` output: the address is
// the bare "ip_configured" string.
const prlctlFixture = `[
    { "uuid": "bb586bcf", "status": "running", "ip_configured": "10.211.55.130", "name": "mpd-130" }
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

// Power issues the prlctl verb (announced before it runs, so the announcement is
// there even where prlctl is absent), and maps a "start" of a parked VM to
// "resume" — the nuance that lives with the backend now.
func TestParallelsPowerVerb(t *testing.T) {
	var out bytes.Buffer
	parallels{}.Power(context.Background(), &out, vmid.ID(160), "start", backend.StateUnknown)
	if !strings.Contains(out.String(), "prlctl start mpd-160") {
		t.Errorf("a plain start should issue `prlctl start`, got: %s", out.String())
	}
	out.Reset()
	parallels{}.Power(context.Background(), &out, vmid.ID(160), "start", backend.StateSuspended)
	if !strings.Contains(out.String(), "prlctl resume mpd-160") {
		t.Errorf("a suspended VM should resume, got: %s", out.String())
	}
}
