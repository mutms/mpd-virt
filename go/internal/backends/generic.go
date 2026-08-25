package backends

import (
	"context"
	"fmt"
	"io"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// generic is an already-running VM adopted by IP — a cloud VM, bare metal.
// mpd-virt has no control over it: no power, no create, no delete, no note,
// and no address source of its own (locate finds it by name and the last
// recorded IP). It is the deliberate no-op backend, and the shape every other
// backend's unsupported operations degrade to.
type generic struct{}

func init() { backend.Register(backend.Generic, generic{}) }

func (generic) State(context.Context, vmid.ID) backend.State { return backend.StateUnknown }

func (generic) Power(context.Context, io.Writer, vmid.ID, string, backend.State) bool {
	return true // nothing to power
}

func (generic) Candidates(context.Context, vmid.ID) []string { return nil }

func (generic) Create(_ context.Context, _ io.Writer, id vmid.ID, _ backend.CreateOpts) (string, error) {
	return "", fmt.Errorf("a generic box is adopted, not created — use `mpd-virt adopt %s <IP> --backend generic`", id.String())
}

func (generic) Delete(context.Context, io.Writer, vmid.ID) error {
	return fmt.Errorf("--full can delete Apple containers, libvirt and proxmox VMs only; a generic VM is not mpd-virt's to delete")
}

func (generic) Notes(context.Context, vmid.ID) string { return "" }
func (generic) Managed() bool                         { return false }
func (generic) Deletable() bool                       { return false }
