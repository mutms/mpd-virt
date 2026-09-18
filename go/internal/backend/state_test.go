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
	verb      string
	called    bool
	unmanaged bool
}

func (f *fakePower) Power(_ context.Context, _ io.Writer, _ vmid.ID, verb string, _ State) bool {
	f.verb, f.called = verb, true
	return true
}

func (f *fakePower) Managed() bool { return !f.unmanaged }

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
	f := registerFake(t, "parallels")
	id := mustID(t, "160")
	var out bytes.Buffer
	if was := powerOn(context.Background(), &out, id, "parallels"); was != StateRunning {
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
	f := registerFake(t, "parallels")
	id := mustID(t, "160")
	var out bytes.Buffer
	powerOff(context.Background(), &out, id, "parallels")
	if f.called {
		t.Errorf("a stopped VM should not be stopped again")
	}
	if !strings.Contains(out.String(), "mpd-160 is already stopped") {
		t.Errorf("output should say the VM is already stopped, got: %s", out.String())
	}
}

// stubStateSeq returns running until the Nth probe, then stopped.
func stubStateSeq(t *testing.T, stoppedAt int) {
	t.Helper()
	orig := probeState
	calls := 0
	probeState = func(context.Context, vmid.ID, Backend) State {
		calls++
		if calls >= stoppedAt {
			return StateStopped
		}
		return StateRunning
	}
	t.Cleanup(func() { probeState = orig })
}

// stubGracefulShutdown substitutes the in-guest SSH shutdown for one test.
func stubGracefulShutdown(t *testing.T, ok bool) {
	t.Helper()
	orig := gracefulShutdown
	gracefulShutdown = func(context.Context, io.Writer, vmid.ID) bool { return ok }
	t.Cleanup(func() { gracefulShutdown = orig })
}

// A reachable VM is shut down in-guest; the backend power-off must not run.
func TestPowerOffGracefulThenWaits(t *testing.T) {
	stubStateSeq(t, 2)
	stubGracefulShutdown(t, true)
	f := registerFake(t, "utm")
	var out bytes.Buffer
	powerOff(context.Background(), &out, mustID(t, "160"), "utm")
	if f.called {
		t.Errorf("graceful in-guest shutdown succeeded; the backend hard power-off should not run")
	}
	if strings.Contains(out.String(), "unreachable over SSH") {
		t.Errorf("a reachable guest should not report the fallback, got: %s", out.String())
	}
}

// An unmanaged (generic) VM is shut down in-guest and its off-ness read from
// the ssh port, not a hypervisor power-off there is none of.
func TestPowerOffUnmanagedUsesPort(t *testing.T) {
	stubState(t, StateUnknown) // generic never reports a settled state
	stubGracefulShutdown(t, true)
	f := registerFake(t, "generic")
	f.unmanaged = true
	var out bytes.Buffer
	powerOff(context.Background(), &out, mustID(t, "160"), "generic")
	if f.called {
		t.Errorf("an unmanaged VM has no hypervisor power-off to invoke")
	}
}

// An unreachable guest falls back to the backend's own power verb.
func TestPowerOffFallsBackWhenUnreachable(t *testing.T) {
	stubStateSeq(t, 2)
	stubGracefulShutdown(t, false)
	f := registerFake(t, "utm")
	var out bytes.Buffer
	powerOff(context.Background(), &out, mustID(t, "160"), "utm")
	if !f.called || f.verb != "stop" {
		t.Errorf("an unreachable guest should fall back to the backend stop verb; called=%v verb=%q", f.called, f.verb)
	}
}

// An unreadable state must not stop the power verb from being tried — that is
// the pre-existing blind behaviour, kept for VMs powered elsewhere.
func TestPowerOnUnknownStateStillTries(t *testing.T) {
	stubState(t, StateUnknown)
	f := registerFake(t, "parallels")
	var out bytes.Buffer
	powerOn(context.Background(), &out, mustID(t, "160"), "parallels")
	if !f.called || f.verb != "start" {
		t.Errorf("an unknown state should still issue the start verb; called=%v verb=%q", f.called, f.verb)
	}
}
