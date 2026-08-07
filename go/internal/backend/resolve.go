// Package backend drives a box's power and address through its backend. IP
// detection lives here, with the backend, because a running box always has an
// address and each backend knows how to find its own: Apple containers from
// `container inspect`, Parallels VMs from `prlctl list` (when Parallels Tools
// report one), and generic/adopted boxes from name resolution with the last
// recorded address as a fallback. That is why Start returns the IP — no
// reachable IP means the box did not come up.
package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// sshPort is the door whose reachability tells us a candidate address is a live
// box and not a stale record — the same door takeover and start walk through.
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

// locate finds the box's current IP for one backend in a single pass. It
// gathers candidates in priority order — the backend's own source first
// (authoritative), then name resolution, then the last recorded IP — and
// returns the first that answers on ssh. The managed backends' sources are how
// a churned container/VM address is found; the name/last-IP fallback covers
// generic boxes, Parallels without Tools, and a backend CLI absent on this
// machine. It errors when no candidate answers.
func locate(ctx context.Context, id vmid.ID, be Backend) (string, error) {
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

	switch be {
	case Container:
		add(containerIP(ctx, id.Name()), "container")
	case Parallels:
		add(parallelsIP(ctx, id.Name()), "prlctl")
	case UTM:
		// UTM exposes no clean guest-IP query, so the box is pinned to its
		// canonical vmnet address; trust it only when the VM actually exists,
		// and let the ssh probe below confirm it is up.
		if utmVMExists(ctx, id.Name()) {
			add(utmCanonicalIP(id), "utm")
		}
	}
	for _, ip := range resolveHost(ctx, id.Name()) {
		add(ip, "dns")
	}
	// mDNS needs the .local suffix spelled out: macOS does not append it
	// to a bare single-label name. A prepared box advertises itself via
	// avahi (the prep script and bootstrap set it up), so this is the
	// discovery path for generic boxes never adopted before.
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

// containerIP reads a native container's current vmnet address from
// `container inspect` — the authoritative source, since the address changes on
// every start and the name does not resolve through the OS. Empty on any
// failure (runtime absent, container stopped), so locate falls back.
func containerIP(ctx context.Context, name string) string {
	res, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{"inspect", name}})
	if err != nil || res.Failed() {
		return ""
	}
	ip, _ := parseContainerIP(res.Stdout)
	return ip
}

// parseContainerIP pulls the running vmnet address out of `container inspect`
// JSON — an array whose live address is at status.networks[].ipv4Address in
// CIDR form ("192.168.64.26/24"). configuration.networks carries no address.
func parseContainerIP(stdout string) (string, error) {
	var boxes []struct {
		Status struct {
			Networks []struct {
				IPv4Address string `json:"ipv4Address"`
			} `json:"networks"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(stdout), &boxes); err != nil {
		return "", fmt.Errorf("parsing container inspect JSON: %w", err)
	}
	for _, b := range boxes {
		for _, n := range b.Status.Networks {
			if ip := stripMask(n.IPv4Address); ip != "" {
				return ip, nil
			}
		}
	}
	return "", fmt.Errorf("no ipv4Address in inspect output")
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
			if ip := stripMask(field); ip != "" && ip != "-" {
				return ip, nil
			}
		}
	}
	return "", fmt.Errorf("prlctl reported no address")
}

// stripMask drops a "/24"-style suffix, returning the bare IP.
func stripMask(addr string) string {
	addr = strings.TrimSpace(addr)
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		addr = addr[:i]
	}
	return addr
}

// systemLookup resolves a name through the system resolver (cgo getaddrinfo on
// macOS: the resolver search list, and mDNS when the name ends in .local —
// callers must spell that suffix out, as `ping` must). The query is IPv4-only,
// and not just because the box's reachable address is v4: mpd boxes run with
// IPv6 disabled, so an A+AAAA lookup over mDNS stalls ~5s waiting for the AAAA
// answer that never comes — past probeTimeout — while an A-only query returns
// in milliseconds. Errors are swallowed: an unresolvable name is the normal
// case for a box that does not advertise one, and locate falls through to the
// registry.
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
// ssh (and so takeover/start) can reach the box.
func dialSSH(ctx context.Context, ip string) bool {
	d := net.Dialer{Timeout: probeTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, sshPort))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
