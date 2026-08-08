package backend

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// prlctlListFixture is real `prlctl list -a --json` output from a Mac running
// several boxes at once — the shape a per-name query returns too.
const prlctlListFixture = `[
    { "uuid": "b1c42d97", "status": "running",   "ip_configured": "-", "name": "macOS" },
    { "uuid": "bb586bcf", "status": "suspended", "ip_configured": "-", "name": "mpd-130" },
    { "uuid": "7d740193", "status": "stopped",   "ip_configured": "-", "name": "mpd-130-copy" },
    { "uuid": "5c85feb1", "status": "running",   "ip_configured": "-", "name": "mpd-160" },
    { "uuid": "edde6d3d", "status": "suspended", "ip_configured": "-", "name": "vscode" }
]`

// The box's own status is read, not the first entry's — and a near-miss name
// like mpd-130-copy must not answer for mpd-130.
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

// container inspect carries the state next to the address parseContainerIP reads.
func TestParseContainerState(t *testing.T) {
	if got := parseContainerState(inspectFixture); got != "running" {
		t.Errorf("parseContainerState = %q, want running", got)
	}
	if got := parseContainerState("not json"); got != "" {
		t.Errorf("unparseable output should read as no state, got %q", got)
	}
}

// Settled states normalize; transitional and unrecognized words stay unknown,
// so the power verb is issued rather than skipped on a guess.
func TestNormalizeState(t *testing.T) {
	for word, want := range map[string]vmState{
		"running":   stRunning,
		"started":   stRunning, // UTM's word
		"Stopped":   stStopped,
		"suspended": stSuspended,
		"paused":    stPaused,
		"starting":  stUnknown,
		"stopping":  stUnknown,
		"":          stUnknown,
	} {
		if got := normalizeState(word); got != want {
			t.Errorf("normalizeState(%q) = %q, want %q", word, got, want)
		}
	}
}

// stubState substitutes the backend state probe for one test.
func stubState(t *testing.T, st vmState) {
	t.Helper()
	orig := probeState
	probeState = func(context.Context, vmid.ID, Backend) vmState { return st }
	t.Cleanup(func() { probeState = orig })
}

// A box already running is not started again: no power command is issued, so
// there is no refusal to explain away.
func TestPowerOnSkipsRunningBox(t *testing.T) {
	stubState(t, stRunning)
	id := mustID(t, "160")
	var out bytes.Buffer
	if was := powerOn(context.Background(), &out, id, Parallels); was != stRunning {
		t.Errorf("powerOn should report the prior state %q, got %q", stRunning, was)
	}
	if strings.Contains(out.String(), "prlctl") {
		t.Errorf("a running box should not be started again, got: %s", out.String())
	}
	if !strings.Contains(out.String(), "mpd-160 is already running") {
		t.Errorf("output should say the box is already running, got: %s", out.String())
	}
}

// The mirror case: a box already off is not stopped again.
func TestPowerOffSkipsStoppedBox(t *testing.T) {
	stubState(t, stStopped)
	id := mustID(t, "160")
	var out bytes.Buffer
	powerOff(context.Background(), &out, id, Parallels)
	if strings.Contains(out.String(), "prlctl") {
		t.Errorf("a stopped box should not be stopped again, got: %s", out.String())
	}
	if !strings.Contains(out.String(), "mpd-160 is already stopped") {
		t.Errorf("output should say the box is already stopped, got: %s", out.String())
	}
}

// An unreadable state must not stop the power verb from being tried — that is
// the pre-existing blind behaviour, kept for boxes powered elsewhere.
func TestPowerOnUnknownStateStillTries(t *testing.T) {
	stubState(t, stUnknown)
	id := mustID(t, "160")
	var out bytes.Buffer
	powerOn(context.Background(), &out, id, Parallels)
	if !strings.Contains(out.String(), "prlctl start mpd-160") {
		t.Errorf("an unknown state should still issue the start verb, got: %s", out.String())
	}
}

// A refusal names the state the box was actually in, instead of guessing.
func TestRefusalNote(t *testing.T) {
	id := mustID(t, "160")
	if got := refusalNote(id, stSuspended, "stopped"); got != "mpd-160 is suspended" {
		t.Errorf("refusalNote with a known state = %q", got)
	}
	if got := refusalNote(id, stUnknown, "stopped"); !strings.Contains(got, "may already be stopped") {
		t.Errorf("refusalNote with no state should fall back to the guess, got %q", got)
	}
}
