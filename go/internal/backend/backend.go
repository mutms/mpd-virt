// Package backend is the framework the per-platform backends plug into: the
// Backend identity, the VM interface, the registry, and the orchestration that
// drives them. Implementations live in internal/backends and self-register.
package backend

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// Backend names the platform a VM runs on (--backend, stored in vm.json). Each
// backend declares its own name constant beside its impl and registers under it.
type Backend string

// Parse validates a --backend value against the registry — valid exactly when a
// backend is registered for it, so no second list can drift.
func Parse(s string) (Backend, error) {
	if s == "" {
		return "", fmt.Errorf("a backend is required (%s)", List())
	}
	if _, ok := impls[Backend(s)]; ok {
		return Backend(s), nil
	}
	return "", fmt.Errorf("unknown backend %q — must be one of %s", s, List())
}

// List renders the registered backends, sorted, for flag help and errors.
func List() string {
	names := make([]string, 0, len(impls))
	for be := range impls {
		names = append(names, string(be))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// State is a VM's power state, normalized so `start` can skip a verb the
// hypervisor would refuse (Parallels rejects `prlctl start` on a live VM).
type State string

const (
	// StateUnknown is the backend's honest "can't say"; the verb is issued blind.
	StateUnknown   State = ""
	StateRunning   State = "running"
	StateStopped   State = "stopped"
	StateSuspended State = "suspended" // memory parked on disk
	StatePaused    State = "paused"    // frozen but resident
)

// Normalize maps backends' status words onto State; anything unsettled (a
// transitional word, libvirt's "shut off") stays unknown so the verb is tried.
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

// VM is the behaviour of one backend; an unsupported operation returns the
// neutral result (StateUnknown, nil, a no-op, an error) rather than being special-cased.
type VM interface {
	// State reports the VM's power state, StateUnknown when it cannot say.
	State(ctx context.Context, id vmid.ID) State
	// Power runs one verb (start/stop) best-effort; prior is the state read.
	Power(ctx context.Context, out io.Writer, id vmid.ID, verb string, prior State) bool
	// Candidates returns backend-specific IP candidates for locate, in priority order.
	Candidates(ctx context.Context, id vmid.ID) []string
	// Create provisions a fresh VM and returns its IP; errors for an adopt-only backend.
	Create(ctx context.Context, out io.Writer, id vmid.ID, opts CreateOpts) (string, error)
	// Delete destroys the VM; errors for a backend that does not delete.
	Delete(ctx context.Context, out io.Writer, id vmid.ID) error
	// Notes returns the VM's backend note, "" when it carries none.
	Notes(ctx context.Context, id vmid.ID) string
	// Managed reports whether mpd-virt can power it (so Start waits for a boot).
	Managed() bool
	// Deletable reports whether Delete can destroy its VMs (--full).
	Deletable() bool
}

// impls holds the registered backends, keyed by name; internal/backends fills it.
var impls = map[Backend]VM{}

// Register records a backend implementation, called from a backend's init().
func Register(be Backend, impl VM) { impls[be] = impl }

// backendFor returns a backend's impl, or a safe no-op when none is registered
// — so an unknown backend degrades rather than panics.
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
