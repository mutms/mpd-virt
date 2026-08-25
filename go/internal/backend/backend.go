// Package backend is the framework the per-platform backends plug into: the
// Backend identity, the VM interface each backend implements, the registry they
// register with, and the orchestration (Start/Stop/Create/Delete/PowerState/
// locate) that drives them uniformly. The implementations live in the sibling
// internal/backends package and register themselves at init; nothing here
// imports them, so this package stays free of any one platform's details.
package backend

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// Backend names the platform a VM runs on. It is supplied explicitly
// (--backend), not derived from the id: reachability and IP resolution are
// uniform now, so the id is a plain identifier that carries no platform
// meaning. The value is recorded so lifecycle commands that DO need the
// hypervisor — start, stop, delete — know which one owns the VM.
type Backend string

const (
	Generic   Backend = "generic"   // adopted / manually managed VM (demos, LAN)
	Parallels Backend = "parallels" // Parallels VM
	Container Backend = "container" // native Apple container
	UTM       Backend = "utm"       // UTM Desktop VM (macOS, osascript-driven)
	Proxmox   Backend = "proxmox"   // Proxmox VM behind warp
	Libvirt   Backend = "libvirt"   // libvirt/KVM VM on a Linux host
)

// backends is the closed set of valid values, in help/order.
var backends = []Backend{Generic, Parallels, Container, UTM, Proxmox, Libvirt}

// Parse validates a --backend value against the known set.
func Parse(s string) (Backend, error) {
	for _, b := range backends {
		if s == string(b) {
			return b, nil
		}
	}
	if s == "" {
		return "", fmt.Errorf("a backend is required (%s)", List())
	}
	return "", fmt.Errorf("unknown backend %q — must be one of %s", s, List())
}

// List renders the valid backends for flag help and error messages.
func List() string {
	names := make([]string, len(backends))
	for i, b := range backends {
		names[i] = string(b)
	}
	return strings.Join(names, ", ")
}

// State is a VM's power state as its backend reports it, normalized to the
// words the power path reasons about. Knowing it first is what keeps `start`
// from firing a power verb the hypervisor will only refuse — Parallels rejects
// `prlctl start` on a VM that is not stopped, and the resulting error read
// like a failure when nothing was actually wrong.
type State string

const (
	// StateUnknown is the honest answer whenever the backend does not tell us:
	// its CLI is not on this host (the VM is powered elsewhere), it does not
	// know the VM, or it reports a transitional state (starting/stopping)
	// that no power decision should be made from. The power verb is then
	// issued blind, exactly as it was before this check existed.
	StateUnknown   State = ""
	StateRunning   State = "running"
	StateStopped   State = "stopped"
	StateSuspended State = "suspended" // memory parked on disk
	StatePaused    State = "paused"    // frozen but resident
)

// Normalize maps the backends' status words onto State. Only the settled states
// are recognized; anything else (a transitional "starting"/"stopping", a word a
// future release invents, libvirt's "shut off") stays unknown, so the caller
// falls back to issuing the power verb rather than acting on a guess.
func Normalize(word string) State {
	switch strings.ToLower(strings.TrimSpace(word)) {
	case "running", "started": // prlctl / container say running, UTM says started
		return StateRunning
	case "stopped":
		return StateStopped
	case "suspended":
		return StateSuspended
	case "paused":
		return StatePaused
	}
	return StateUnknown
}

// CreateOpts carries what Create needs beyond the id and backend.
type CreateOpts struct {
	Image  string // base image to run (container backend), e.g. mpd-virt-container-apple
	Memory string // memory: container "10g", or a VM RAM like "8g" (utm)
	Disk   string // VM disk size, e.g. "80g" (utm); ignored by the container backend
	User   string // dev account to seed
	PubKey string // the public key to authorize, one line ("ssh-ed25519 …")
}

// VM is the behaviour of one backend. Every backend implements the whole
// interface; one that does not support an operation returns the neutral result
// — StateUnknown, no candidates, a no-op power, an explanatory error — so the
// orchestration never has to special-case a platform. Implementations live in
// internal/backends and register themselves via Register.
type VM interface {
	// State reports the VM's power state, StateUnknown when the backend cannot
	// say (CLI/API absent, VM unknown, or a backend with no power model).
	State(ctx context.Context, id vmid.ID) State
	// Power runs one power verb (start/stop) best-effort, reporting success.
	// prior is the state the VM was read in, for the refusal message and for a
	// backend (Parallels) that maps the verb differently by prior state. A
	// backend with no power model does nothing and returns true.
	Power(ctx context.Context, out io.Writer, id vmid.ID, verb string, prior State) bool
	// Candidates returns backend-specific IP candidates for locate, in priority
	// order (empty for a backend that has no address source of its own).
	Candidates(ctx context.Context, id vmid.ID) []string
	// Create provisions a fresh VM and returns its IP; an error for a backend
	// whose VMs are adopted rather than created.
	Create(ctx context.Context, out io.Writer, id vmid.ID, opts CreateOpts) (string, error)
	// Delete destroys the VM (the inverse of Create); an error for a backend
	// whose VMs mpd-virt does not delete.
	Delete(ctx context.Context, out io.Writer, id vmid.ID) error
	// Notes returns the VM's backend note, "" when the backend carries none.
	Notes(ctx context.Context, id vmid.ID) string
	// Managed reports whether mpd-virt can power the backend from this host,
	// which decides whether Start waits out a boot.
	Managed() bool
	// Deletable reports whether Delete can destroy this backend's VMs (--full).
	Deletable() bool
}

// registry holds the backend implementations, keyed by name. The sibling
// internal/backends package fills it from init(); a program that wants the real
// backends imports it (the CLI does so once, centrally).
var impls = map[Backend]VM{}

// Register records a backend implementation. Called from the backends package's
// init(); a second registration for the same name replaces the first.
func Register(be Backend, impl VM) { impls[be] = impl }

// backendFor returns the implementation for a backend, or a safe no-op when
// none is registered — an unknown or unregistered backend then behaves like a
// powerless, un-createable one rather than panicking. That also lets this
// package's own tests exercise the orchestration with no backends imported.
func backendFor(be Backend) VM {
	if impl, ok := impls[be]; ok {
		return impl
	}
	return unregistered{}
}

// unregistered is the neutral fallback backendFor returns for an unknown name.
type unregistered struct{}

func (unregistered) State(context.Context, vmid.ID) State { return StateUnknown }
func (unregistered) Power(context.Context, io.Writer, vmid.ID, string, State) bool {
	return true
}
func (unregistered) Candidates(context.Context, vmid.ID) []string { return nil }
func (unregistered) Create(context.Context, io.Writer, vmid.ID, CreateOpts) (string, error) {
	return "", fmt.Errorf("no backend registered")
}
func (unregistered) Delete(context.Context, io.Writer, vmid.ID) error {
	return fmt.Errorf("no backend registered")
}
func (unregistered) Notes(context.Context, vmid.ID) string { return "" }
func (unregistered) Managed() bool                         { return false }
func (unregistered) Deletable() bool                       { return false }
