package backend

// orchestrate.go drives the registered backends uniformly: power, address
// discovery, create/delete. Everything platform-specific is behind the VM
// interface (internal/backends); what stays here is the sequencing every
// backend shares — wait for a boot, probe candidate addresses in priority
// order, skip a power verb the VM does not need.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// PowerState reports the VM's power state as its backend authoritatively knows
// it — "running", "stopped", "suspended", "paused" — or "" when the backend
// cannot say. Callers like `list` use it to skip an SSH dial to a VM the
// hypervisor already reports as off, and fall back to dialing on "".
func PowerState(ctx context.Context, id vmid.ID, be Backend) string {
	return string(probeState(ctx, id, be))
}

// probeState is the package's one look at the outside world for state, a var
// only so tests can substitute it.
var probeState = func(ctx context.Context, id vmid.ID, be Backend) State {
	return backendFor(be).State(ctx, id)
}

// Notes returns the VM's free-form backend note ("" for a backend that carries
// none). Best-effort: any failure inside the backend is "", never an error.
func Notes(ctx context.Context, id vmid.ID, be Backend) string {
	return backendFor(be).Notes(ctx, id)
}

// Deletable reports whether Delete can destroy a VM on this backend.
func Deletable(be Backend) bool { return backendFor(be).Deletable() }

// Create provisions a fresh VM for the id through its backend and returns the
// IP it came up on, ready for adoption. A backend whose VMs are adopted rather
// than created returns an explanatory error.
func Create(ctx context.Context, out io.Writer, id vmid.ID, be Backend, opts CreateOpts) (string, error) {
	return backendFor(be).Create(ctx, out, id, opts)
}

// Delete destroys a VM's hypervisor object — the inverse of Create, so only for
// what Create makes. A backend that does not delete returns an error.
func Delete(ctx context.Context, out io.Writer, id vmid.ID, be Backend) error {
	return backendFor(be).Delete(ctx, out, id)
}

// Start brings a VM up through its backend and returns the IP it came up on.
// It powers the VM on (a no-op for backends mpd-virt does not control), then
// finds its current address, waiting while it boots. A VM with no reachable
// address did not start — that is the returned error, since a running VM
// always has one. Progress and any power-command warnings go to out.
func Start(ctx context.Context, out io.Writer, id vmid.ID, be Backend) (string, error) {
	was := powerOn(ctx, out, id, be)

	budget := 20 * time.Second
	if backendFor(be).Managed() && was != StateRunning {
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

// Stop powers a VM off through its backend (a no-op for backends mpd-virt does
// not control). Detaching it from the overlay is the caller's job.
func Stop(ctx context.Context, out io.Writer, id vmid.ID, be Backend) error {
	powerOff(ctx, out, id, be)
	return nil
}

// powerOn brings a VM up, and returns the state it was in beforehand so Start
// knows whether to wait out a boot. A VM already running is left alone: its
// hypervisor would refuse the verb anyway, and the refusal read as an error in
// what is the ordinary case of re-running `start` on a live VM. The backend's
// Power decides how "start" is issued — Parallels resumes a suspended VM, a
// generic VM does nothing.
func powerOn(ctx context.Context, out io.Writer, id vmid.ID, be Backend) State {
	st := probeState(ctx, id, be)
	if st == StateRunning {
		fmt.Fprintf(out, "  ✓ %s is already running\n", id.Name())
		return st
	}
	backendFor(be).Power(ctx, out, id, "start", st)
	return st
}

// powerOff powers a VM down, skipping a VM that is already off.
func powerOff(ctx context.Context, out io.Writer, id vmid.ID, be Backend) {
	st := probeState(ctx, id, be)
	if st == StateStopped {
		fmt.Fprintf(out, "  ✓ %s is already stopped\n", id.Name())
		return
	}
	backendFor(be).Power(ctx, out, id, "stop", st)
}

// --- address discovery ------------------------------------------------------

// sshPort is the door whose reachability tells us a candidate address is a live
// VM and not a stale record — the same door adopt and start walk through.
const sshPort = "22"

// probeTimeout bounds each name lookup and each ssh-port dial — short, so
// locate fails over to the next candidate quickly rather than hang on a dead one.
const probeTimeout = 2 * time.Second

// resolveHost and sshReachable are the two effects the generic path has on the
// world — a name lookup and a port probe — as package vars only so tests can
// substitute them.
var (
	resolveHost  = systemLookup
	sshReachable = dialSSH
)

// locate finds the VM's current IP for one backend in a single pass. It gathers
// candidates in priority order — the backend's own source first (authoritative,
// via VM.Candidates), then name resolution, then the last recorded IP — and
// returns the first that answers on ssh. The backend's source is how a churned
// container/VM address is found; the name/last-IP fallback covers generic VMs,
// Parallels without Tools, and a backend CLI absent on this machine. It errors
// when no candidate answers.
func locate(ctx context.Context, id vmid.ID, be Backend) (string, error) {
	type candidate struct{ ip, label string }
	var cands []candidate
	seen := map[string]bool{}
	// Every candidate is validated as a literal IPv4 address, whatever its
	// source claimed. Discovery consumes attacker-influenceable strings —
	// prlctl's ip_configured is guest-reported, mDNS answers come from whoever
	// is on the LAN — and the winner ends up in the registry and ~/.ssh/config,
	// so nothing that is not an address may pass this point.
	add := func(ip, source string) {
		a, err := netip.ParseAddr(strings.TrimSpace(ip))
		if err != nil || !a.Is4() {
			return
		}
		ip = a.String()
		if seen[ip] {
			return
		}
		seen[ip] = true
		cands = append(cands, candidate{ip, ip + " (" + source + ")"})
	}

	for _, ip := range backendFor(be).Candidates(ctx, id) {
		add(ip, string(be))
	}
	for _, ip := range resolveHost(ctx, id.Name()) {
		add(ip, "dns")
	}
	// mDNS needs the .local suffix spelled out: macOS does not append it to a
	// bare single-label name. A prepared VM advertises itself via avahi (the
	// prep script and bootstrap set it up), so this is the discovery path for
	// generic VMs never adopted before.
	for _, ip := range resolveHost(ctx, id.Name()+".local") {
		add(ip, "mdns")
	}
	if e, err := registry.Load(id); err == nil {
		add(e.IP, "last")
	}

	if len(cands) == 0 {
		return "", fmt.Errorf("no candidate address for %s", id.Name())
	}
	labels := make([]string, 0, len(cands))
	for _, c := range cands {
		if sshReachable(ctx, c.ip) {
			return c.ip, nil
		}
		labels = append(labels, c.label)
	}
	return "", fmt.Errorf("none of %s answered on ssh", strings.Join(labels, ", "))
}

// systemLookup resolves a name through the system resolver (cgo getaddrinfo on
// macOS: the resolver search list, and mDNS when the name ends in .local —
// callers must spell that suffix out, as `ping` must). The query is IPv4-only,
// and not just because the VM's reachable address is v4: mpd vms run with IPv6
// disabled, so an A+AAAA lookup over mDNS stalls ~5s waiting for the AAAA answer
// that never comes — past probeTimeout — while an A-only query returns in
// milliseconds. Errors are swallowed: an unresolvable name is the normal case
// for a VM that does not advertise one, and locate falls through to the registry.
func systemLookup(ctx context.Context, name string) []string {
	r := &net.Resolver{PreferGo: false}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	ips, err := r.LookupIP(ctx, "ip4", name)
	if err != nil {
		return nil
	}
	var v4 []string
	for _, ip := range ips {
		v4 = append(v4, ip.String())
	}
	return v4
}

// dialSSH reports whether ip accepts a TCP connection on the ssh port. A TCP
// dial, not ICMP: unprivileged, and it confirms the thing we care about — that
// ssh (and so adopt/start) can reach the VM.
func dialSSH(ctx context.Context, ip string) bool {
	d := net.Dialer{Timeout: probeTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, sshPort))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
