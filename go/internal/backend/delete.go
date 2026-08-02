package backend

import (
	"context"
	"fmt"
	"io"

	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// Delete destroys the box's backend resource — the container or VM itself — for
// the backends whose CLI runs on this Mac (container, parallels). A generic box
// has no hypervisor to destroy (nothing to do here); proxmox is remote and not
// wired yet. The caller wipes the host-side bookkeeping (overlay peer,
// ssh-config, registry) regardless of what this returns.
func Delete(ctx context.Context, out io.Writer, id vmid.ID, be Backend) error {
	name := id.Name()
	switch be {
	case Container:
		fmt.Fprintf(out, "  ▶ container delete --force %s\n", name)
		if r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{"delete", "--force", name}}); err != nil {
			return err
		} else if r.Failed() {
			return fmt.Errorf("`container delete %s` failed: %s", name, shortErr(r))
		}
	case Parallels:
		// The VM must be stopped before delete; `stop --kill` is idempotent.
		fmt.Fprintf(out, "  ▶ prlctl stop %s --kill && prlctl delete %s\n", name, name)
		_, _ = exec.Capture(ctx, exec.Cmd{Name: "prlctl", Args: []string{"stop", name, "--kill"}})
		if r, err := exec.Capture(ctx, exec.Cmd{Name: "prlctl", Args: []string{"delete", name}}); err != nil {
			return err
		} else if r.Failed() {
			return fmt.Errorf("`prlctl delete %s` failed: %s", name, shortErr(r))
		}
	case Proxmox:
		return fmt.Errorf("delete is not wired for the proxmox backend yet (remote qm) — destroy the VM on the Proxmox host")
	}
	return nil // generic (or unset): no local backend resource
}
