package backend

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// Settled states normalize; transitional and unrecognized words stay unknown,
// so the power verb is issued rather than skipped on a guess.
func TestNormalize(t *testing.T) {
	for word, want := range map[string]State{
		"running":   StateRunning,
		"started":   StateRunning, // UTM's word
		"Stopped":   StateStopped,
		"suspended": StateSuspended,
		"paused":    StatePaused,
		"starting":  StateUnknown,
		"stopping":  StateUnknown,
		"shut off":  StateUnknown, // libvirt's word for off — deliberately unknown
		"":          StateUnknown,
	} {
		if got := Normalize(word); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", word, got, want)
		}
	}
}

// stubState substitutes the backend state probe for one test.
func stubState(t *testing.T, st State) {
	t.Helper()
	orig := probeState
	probeState = func(context.Context, vmid.ID, Backend) State { return st }
	t.Cleanup(func() { probeState = orig })
}

// fakePower is a test backend that records the power verb it was asked for.
// Embedding unregistered gives it the rest of the VM interface as no-ops.
type fakePower struct {
	unregistered
	verb   string
	called bool
}

func (f *fakePower) Power(_ context.Context, _ io.Writer, _ vmid.ID, verb string, _ State) bool {
	f.verb, f.called = verb, true
	return true
}

// registerFake wires a fake backend in for one test and removes it after.
func registerFake(t *testing.T, be Backend) *fakePower {
	t.Helper()
	f := &fakePower{}
	Register(be, f)
	t.Cleanup(func() { delete(impls, be) })
	return f
}

// A VM already running is not powered again: powerOn reports the prior state and
// the backend's Power is never called, so there is no refusal to explain away.
func TestPowerOnSkipsRunningVms(t *testing.T) {
	stubState(t, StateRunning)
	f := registerFake(t, Parallels)
	id := mustID(t, "160")
	var out bytes.Buffer
	if was := powerOn(context.Background(), &out, id, Parallels); was != StateRunning {
		t.Errorf("powerOn should report the prior state %q, got %q", StateRunning, was)
	}
	if f.called {
		t.Errorf("a running VM should not be powered again")
	}
	if !strings.Contains(out.String(), "mpd-160 is already running") {
		t.Errorf("output should say the VM is already running, got: %s", out.String())
	}
}

// The mirror case: a VM already off is not stopped again.
func TestPowerOffSkipsStoppedVms(t *testing.T) {
	stubState(t, StateStopped)
	f := registerFake(t, Parallels)
	id := mustID(t, "160")
	var out bytes.Buffer
	powerOff(context.Background(), &out, id, Parallels)
	if f.called {
		t.Errorf("a stopped VM should not be stopped again")
	}
	if !strings.Contains(out.String(), "mpd-160 is already stopped") {
		t.Errorf("output should say the VM is already stopped, got: %s", out.String())
	}
}

// An unreadable state must not stop the power verb from being tried — that is
// the pre-existing blind behaviour, kept for VMs powered elsewhere.
func TestPowerOnUnknownStateStillTries(t *testing.T) {
	stubState(t, StateUnknown)
	f := registerFake(t, Parallels)
	var out bytes.Buffer
	powerOn(context.Background(), &out, mustID(t, "160"), Parallels)
	if !f.called || f.verb != "start" {
		t.Errorf("an unknown state should still issue the start verb; called=%v verb=%q", f.called, f.verb)
	}
}
