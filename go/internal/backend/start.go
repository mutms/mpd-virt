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
	powerOn(ctx, out, id, be)

	budget := 20 * time.Second
	if managed(be) {
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

func powerOn(ctx context.Context, out io.Writer, id vmid.ID, be Backend) {
	power(ctx, out, id, be, "start", "running")
}

func powerOff(ctx context.Context, out io.Writer, id vmid.ID, be Backend) {
	power(ctx, out, id, be, "stop", "stopped")
}

// power runs one backend power verb ("start"/"stop") for the backends whose CLI
// runs on this Mac — Apple `container` and Parallels `prlctl`. It is
// best-effort: whether the box actually changed state is decided by Start's
// reachability wait, not here. A non-zero exit usually means the box is already
// in the target state; a launch error means the CLI is not on this machine
// (mpd-virt may be driving a box powered elsewhere) — both are reported to out
// and swallowed. generic/proxmox have no local power and are skipped.
func power(ctx context.Context, out io.Writer, id vmid.ID, be Backend, verb, already string) {
	// UTM is driven through osascript, not a single-verb CLI.
	if be == UTM {
		utmPower(ctx, out, id, verb)
		return
	}
	argv := powerArgv(id, be, verb)
	if argv == nil {
		return
	}
	fmt.Fprintf(out, "  ▶ %s\n", strings.Join(argv, " "))
	r, err := exec.Capture(ctx, exec.Cmd{Name: argv[0], Args: argv[1:]})
	if err != nil {
		fmt.Fprintf(out, "    … %s unavailable here (%v) — assuming the box is managed elsewhere\n", argv[0], err)
		return
	}
	if r.Failed() {
		fmt.Fprintf(out, "    … %s (continuing — the box may already be %s)\n", shortErr(r), already)
	}
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
