// Package backend resolves the address mpd-virt should reach a box at,
// dispatching on the box id's class. Proxmox is derivable from the id;
// Parallels and native containers are looked up live from their hypervisor
// (their leases move); a generic adopted box has no discoverable address and
// must be handed one explicitly.
package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// proxmoxSubnet is the /24 that Proxmox mpd VMs sit on behind warp; the id is
// the host octet (see mpd-test docs/servers/warp.md).
const proxmoxSubnet = "10.212.56"

// ResolveIP returns the IP for the box id, chosen by its class. It is the
// fallback used when `takeover` is invoked without an explicit address.
func ResolveIP(ctx context.Context, id vmid.ID) (string, error) {
	switch id.Class() {
	case vmid.Proxmox:
		// Cloud-init gives the VM the id as its host octet — derivable, no
		// hypervisor round-trip.
		return fmt.Sprintf("%s.%d", proxmoxSubnet, int(id)), nil
	case vmid.Container:
		return containerIP(ctx, id.Name())
	case vmid.Parallels:
		return parallelsIP(ctx, id.Name())
	case vmid.Generic:
		return "", fmt.Errorf("%s is a generic box with no discoverable address; pass it explicitly:\n    mpd-virt takeover %s <IP>", id.Name(), id.Pad())
	default:
		return "", fmt.Errorf("no IP resolver for class %q", id.Class())
	}
}

// containerIP reads a native container's vmnet-leased address from
// `container inspect`. The lease changes across restarts, so it is read live.
func containerIP(ctx context.Context, name string) (string, error) {
	res, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{"inspect", name}})
	if err != nil {
		return "", err
	}
	if res.Failed() {
		return "", fmt.Errorf("`container inspect %s` failed: %s", name, oneLine(res.Stderr))
	}
	ip, err := parseContainerIP(res.Stdout)
	if err != nil {
		return "", fmt.Errorf("%w (from `container inspect %s`)", err, name)
	}
	return ip, nil
}

// parseContainerIP pulls the running vmnet address out of `container inspect`
// JSON — an array of objects whose live address is at
// status.networks[].ipv4Address in CIDR form ("192.168.64.26/24").
// configuration.networks carries no address, so the running status is the
// only source.
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
	return "", fmt.Errorf("no ipv4Address in inspect output — is the container running?")
}

// parallelsIP reads the address Parallels handed a VM. Parallels mpd VMs use
// dynamic leases, so the address is looked up via prlctl rather than derived.
func parallelsIP(ctx context.Context, name string) (string, error) {
	res, err := exec.Capture(ctx, exec.Cmd{Name: "prlctl", Args: []string{"list", name, "-f", "--json"}})
	if err != nil {
		return "", err
	}
	if res.Failed() {
		return "", fmt.Errorf("`prlctl list %s` failed: %s", name, oneLine(res.Stderr))
	}
	ip, err := parseParallelsIP(res.Stdout)
	if err != nil {
		return "", fmt.Errorf("%w (from `prlctl list %s`)", err, name)
	}
	return ip, nil
}

// parseParallelsIP pulls the guest-reported address out of
// `prlctl list <name> -f --json` — an array of objects whose "ip_configured"
// field holds the bare IP ("10.211.55.130"), or "-" when Parallels does not
// yet know a lease.
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
	return "", fmt.Errorf("prlctl reported no address — is the VM running with guest tools?")
}

// stripMask drops a "/24"-style suffix, returning the bare IP.
func stripMask(addr string) string {
	addr = strings.TrimSpace(addr)
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		addr = addr[:i]
	}
	return addr
}

// oneLine collapses a captured stderr into a single trimmed line for error
// messages.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
