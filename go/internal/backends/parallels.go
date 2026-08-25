package backends

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// parallels drives Parallels Desktop VMs through prlctl: power on/off and the
// guest-reported address (when Parallels Tools are installed). `create` needs a
// template clone that is not built yet, so a Parallels VM is made by hand and
// adopted; mpd-virt's `remove` only un-adopts, never deletes.
type parallels struct{}

func init() { backend.Register(backend.Parallels, parallels{}) }

func (parallels) State(ctx context.Context, id vmid.ID) backend.State {
	// -a so stopped VMs are listed too; without it Parallels reports only the
	// running ones and every stopped VM would look unknown.
	res, err := exec.Capture(ctx, exec.Cmd{Name: "prlctl", Args: []string{"list", id.Name(), "-a", "--json"}})
	if err != nil || res.Failed() {
		return backend.StateUnknown
	}
	return backend.Normalize(parseParallelsState(res.Stdout, id.Name()))
}

// Power runs a start/stop verb via prlctl. Parallels parks a VM in two states
// its `start` refuses; `resume` is the verb for both, so a "start" of a
// suspended/paused VM is remapped here — which keeps that nuance with the
// backend it belongs to. `start` stays the fallback (which of the two verbs
// takes a *suspended* VM has differed between Parallels releases).
func (parallels) Power(ctx context.Context, out io.Writer, id vmid.ID, verb string, prior backend.State) bool {
	if verb == "start" && (prior == backend.StateSuspended || prior == backend.StatePaused) {
		verb = "resume"
	}
	fmt.Fprintf(out, "  ▶ prlctl %s %s\n", verb, id.Name())
	r, err := exec.Capture(ctx, exec.Cmd{Name: "prlctl", Args: []string{verb, id.Name()}})
	if err != nil {
		fmt.Fprintf(out, "    … prlctl unavailable here (%v) — assuming the VM is managed elsewhere\n", err)
		return false
	}
	if r.Failed() {
		fmt.Fprintf(out, "    … %s (continuing)\n", backend.ShortErr(r))
		return false
	}
	return true
}

func (parallels) Candidates(ctx context.Context, id vmid.ID) []string {
	if ip := parallelsIP(ctx, id.Name()); ip != "" {
		return []string{ip}
	}
	return nil
}

func (parallels) Create(_ context.Context, _ io.Writer, id vmid.ID, _ backend.CreateOpts) (string, error) {
	return "", fmt.Errorf("create is not implemented for the parallels backend yet (needs a template clone + cloud-init) — create the box yourself, then `mpd-virt adopt %s <IP> --backend parallels`", id.String())
}

func (parallels) Delete(context.Context, io.Writer, vmid.ID) error {
	return fmt.Errorf("--full does not delete parallels VMs; delete it in Parallels Desktop")
}

func (parallels) Notes(context.Context, vmid.ID) string { return "" }
func (parallels) Managed() bool                         { return true }
func (parallels) Deletable() bool                       { return false }

// parseParallelsState pulls one VM's "status" out of `prlctl list <name> -a
// --json`. The name is matched rather than the first entry taken: the same JSON
// shape comes back when Parallels lists every VM it knows, and reading another
// VM's state would be worse than reading none.
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

// parallelsIP reads the guest-reported address from `prlctl list`. Parallels
// knows it only when Parallels Tools are installed in the guest; otherwise
// ip_configured is "-" and this returns empty, leaving locate to fall back.
func parallelsIP(ctx context.Context, name string) string {
	res, err := exec.Capture(ctx, exec.Cmd{Name: "prlctl", Args: []string{"list", name, "-f", "--json"}})
	if err != nil || res.Failed() {
		return ""
	}
	ip, _ := parseParallelsIP(res.Stdout)
	return ip
}

// parseParallelsIP pulls the bare "ip_configured" address out of
// `prlctl list <name> -f --json`, skipping the "-" Parallels prints when it
// does not yet know a lease.
func parseParallelsIP(stdout string) (string, error) {
	var vms []struct {
		IPConfigured string `json:"ip_configured"`
	}
	if err := json.Unmarshal([]byte(stdout), &vms); err != nil {
		return "", fmt.Errorf("parsing prlctl JSON: %w", err)
	}
	for _, vm := range vms {
		for _, field := range strings.Fields(strings.ReplaceAll(vm.IPConfigured, ",", " ")) {
			if ip := backend.StripMask(field); ip != "" && ip != "-" {
				return ip, nil
			}
		}
	}
	return "", fmt.Errorf("prlctl reported no address")
}
