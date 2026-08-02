// Package backend resolves the address mpd-virt should reach a box at.
//
// The address is found uniformly by *name*, not per hypervisor. Parallels
// and Apple's `container` both register a box's short name mpd-<NNN> into the
// macOS resolver, a box running mDNS advertises itself the same way, and
// nothing else can own that private, non-routable name — mpd's own resolvers
// do not serve it and it cannot be public. So resolution is:
//
//  1. look up mpd-<NNN> through the system resolver, then
//  2. fall back to the last address the registry recorded for the box.
//
// The registry fallback is what makes Proxmox and every re-takeover work
// without a hypervisor round-trip: the previous IP is already on file, and a
// Proxmox box behind warp keeps a predictable address. Each candidate must
// answer on ssh to be returned, so a stale name record or an old registry
// entry is skipped rather than handed back — and takeover confirms the box is
// really mpd-<NNN> by its hostname before touching it, so a wrong resolution
// fails safe. When nothing answers, the caller is told to pass the IP.
package backend

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// sshPort is the door whose reachability tells us a resolved address is a
// live box and not a stale record — the same door takeover walks through next.
const sshPort = "22"

// probeTimeout bounds each name lookup and each ssh-port dial. Short on
// purpose: these are LAN round-trips, and the point is to fail over to the
// next candidate quickly rather than hang on a dead one.
const probeTimeout = 2 * time.Second

// resolveHost and sshReachable are the two effects ResolveIP has on the world
// — a name lookup and a port probe. They are package vars only so tests can
// substitute them; nothing else reassigns them.
var (
	resolveHost  = systemLookup
	sshReachable = dialSSH
)

// ResolveIP finds the address for a box id when takeover is invoked without an
// explicit one. It gathers candidates — the addresses mpd-<NNN> resolves to,
// then the last address on file — and returns the first that answers on ssh.
// A stale name record or an old registry entry that no longer answers is
// skipped. If no candidate exists it says so; if candidates exist but none
// answer, it lists them. Either error tells the user to pass the IP.
func ResolveIP(ctx context.Context, id vmid.ID) (string, error) {
	type candidate struct{ ip, label string }
	var cands []candidate
	seen := map[string]bool{}
	add := func(ip, source string) {
		if ip == "" || seen[ip] {
			return
		}
		seen[ip] = true
		cands = append(cands, candidate{ip, ip + " (" + source + ")"})
	}

	for _, ip := range resolveHost(ctx, id.Name()) {
		add(ip, "dns")
	}
	if e, err := registry.Load(id); err == nil {
		add(e.IP, "last")
	}

	if len(cands) == 0 {
		return "", fmt.Errorf("could not find %s by name and no IP is on file — pass it explicitly:\n"+
			"    mpd-virt takeover %s <IP>", id.Name(), id.Pad())
	}

	labels := make([]string, 0, len(cands))
	for _, c := range cands {
		if sshReachable(ctx, c.ip) {
			return c.ip, nil
		}
		labels = append(labels, c.label)
	}
	return "", fmt.Errorf("found %s at %s but nothing answered on ssh — is the box up? pass the IP explicitly:\n"+
		"    mpd-virt takeover %s <IP>", id.Name(), strings.Join(labels, ", "), id.Pad())
}

// systemLookup resolves the box's short name through the system resolver.
// PreferGo:false forces cgo getaddrinfo, which applies the resolver search
// list and does mDNS/.local exactly as `ping mpd-<NNN>` does — the mechanism
// Parallels/container name registration and avahi both surface through. The
// pure-Go resolver reads only resolv.conf and would miss both.
//
// Only IPv4 answers are kept: the box's reachable LAN/vmnet address is v4
// here, and mDNS often also returns a link-local v6 that ssh cannot use.
// Errors are swallowed — an unresolvable name is the normal "not registered
// under this backend" case, and ResolveIP falls through to the registry.
func systemLookup(ctx context.Context, name string) []string {
	r := &net.Resolver{PreferGo: false}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	addrs, err := r.LookupHost(ctx, name)
	if err != nil {
		return nil
	}
	var v4 []string
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			v4 = append(v4, a)
		}
	}
	return v4
}

// dialSSH reports whether ip accepts a TCP connection on the ssh port. A TCP
// dial, not an ICMP ping: it needs no privilege and confirms the thing we
// actually care about — that ssh (and so takeover) can reach the box — rather
// than mere pingability.
func dialSSH(ctx context.Context, ip string) bool {
	d := net.Dialer{Timeout: probeTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, sshPort))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
