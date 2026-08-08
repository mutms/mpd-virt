package backend

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// Start brings a box up through its backend and returns the IP it came up on.
// It powers the box on (a no-op for backends mpd-virt does not control), then
// finds its current address, waiting while it boots. A box with no reachable
// address did not start — that is the returned error, since a running box
// always has one. Progress and any power-command warnings go to out.
func Start(ctx context.Context, out io.Writer, id vmid.ID, be Backend) (string, error) {
	was := powerOn(ctx, out, id, be)

	budget := 20 * time.Second
	if managed(be) && was != stRunning {
		budget = 90 * time.Second // a freshly powered VM/container needs to boot
	}
	deadline := time.Now().Add(budget)
	waited := false
	for {
		ip, err := locate(ctx, id, be)
		if err == nil {
			return ip, nil
		}
		if !time.Now().Before(deadline) {
			return "", fmt.Errorf("%s did not come up: %w", id.Name(), err)
		}
		if !waited {
			fmt.Fprintf(out, "  … waiting for %s to come up\n", id.Name())
			waited = true
		}
		time.Sleep(2 * time.Second)
	}
}

// Stop powers a box off through its backend (a no-op for backends mpd-virt does
// not control). Detaching it from the overlay is the caller's job.
func Stop(ctx context.Context, out io.Writer, id vmid.ID, be Backend) error {
	powerOff(ctx, out, id, be)
	return nil
}

// managed reports whether mpd-virt can power a backend from this Mac.
func managed(be Backend) bool { return be == Container || be == Parallels || be == UTM }

// powerOn brings a box up, and returns the state it was in beforehand so Start
// knows whether to wait out a boot. A box already running is left alone: its
// hypervisor would refuse the verb anyway, and the refusal read as an error in
// what is the ordinary case of re-running `start` on a live box.
func powerOn(ctx context.Context, out io.Writer, id vmid.ID, be Backend) vmState {
	st := probeState(ctx, id, be)
	if st == stRunning {
		fmt.Fprintf(out, "  ✓ %s is already running\n", id.Name())
		return st
	}
	// Parallels parks a box in two states its `start` refuses; `resume` is the
	// verb for both. `start` stays as the fallback, since which of the two
	// verbs takes a *suspended* box has differed between Parallels releases.
	if be == Parallels && (st == stSuspended || st == stPaused) {
		if power(ctx, out, id, be, "resume", st, "running") {
			return st
		}
	}
	power(ctx, out, id, be, "start", st, "running")
	return st
}

// powerOff powers a box down, skipping a box that is already off.
func powerOff(ctx context.Context, out io.Writer, id vmid.ID, be Backend) {
	st := probeState(ctx, id, be)
	if st == stStopped {
		fmt.Fprintf(out, "  ✓ %s is already stopped\n", id.Name())
		return
	}
	power(ctx, out, id, be, "stop", st, "stopped")
}

// power runs one backend power verb for the backends whose CLI runs on this
// Mac — Apple `container` and Parallels `prlctl` — and reports whether it
// succeeded. It is best-effort: whether the box actually changed state is
// decided by Start's reachability wait, not here. A non-zero exit means the
// hypervisor refused the verb from the state the box is in (`was`, which is
// what the warning names); a launch error means the CLI is not on this machine
// (mpd-virt may be driving a box powered elsewhere) — both are reported to out
// and swallowed. generic/proxmox have no local power and are skipped.
func power(ctx context.Context, out io.Writer, id vmid.ID, be Backend, verb string, was vmState, want string) bool {
	// UTM is driven through osascript, not a single-verb CLI.
	if be == UTM {
		utmPower(ctx, out, id, verb)
		return true
	}
	argv := powerArgv(id, be, verb)
	if argv == nil {
		return true
	}
	fmt.Fprintf(out, "  ▶ %s\n", strings.Join(argv, " "))
	r, err := exec.Capture(ctx, exec.Cmd{Name: argv[0], Args: argv[1:]})
	if err != nil {
		fmt.Fprintf(out, "    … %s unavailable here (%v) — assuming the box is managed elsewhere\n", argv[0], err)
		return false
	}
	if r.Failed() {
		fmt.Fprintf(out, "    … %s (continuing — %s)\n", shortErr(r), refusalNote(id, was, want))
		return false
	}
	return true
}

// refusalNote explains a refused power verb: the state we read the box in when
// we have one, and the old guess when the backend told us nothing.
func refusalNote(id vmid.ID, was vmState, want string) string {
	if was == stUnknown {
		return "the box may already be " + want
	}
	return fmt.Sprintf("%s is %s", id.Name(), was)
}

// powerArgv is the backend's CLI invocation for a power verb, or nil for a
// backend mpd-virt does not power. The Apple `container` and Parallels `prlctl`
// CLIs both take the box name (mpd-<NNN>).
func powerArgv(id vmid.ID, be Backend, verb string) []string {
	switch be {
	case Container:
		return []string{"container", verb, id.Name()}
	case Parallels:
		return []string{"prlctl", verb, id.Name()}
	default:
		return nil
	}
}

// shortErr collapses a failed command's output to a single line for a warning.
func shortErr(r exec.Result) string {
	s := strings.TrimSpace(r.Stderr)
	if s == "" {
		s = strings.TrimSpace(r.Stdout)
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "non-zero exit"
	}
	return s
}
