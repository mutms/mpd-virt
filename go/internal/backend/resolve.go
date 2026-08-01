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

	"github.com/mutms/mpd-virt-macos/go/internal/exec"
	"github.com/mutms/mpd-virt-macos/go/internal/vmid"
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
//
// Apple's `container inspect <name>` returns a JSON array of objects, each
// carrying a networks list whose address is CIDR form ("192.168.64.24/24").
// (Field names confirmed against the live tool on the Mac host.)
func containerIP(ctx context.Context, name string) (string, error) {
	res, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{"inspect", name}})
	if err != nil {
		return "", err
	}
	if res.Failed() {
		return "", fmt.Errorf("`container inspect %s` failed: %s", name, oneLine(res.Stderr))
	}
	var boxes []struct {
		Networks []struct {
			Address string `json:"address"`
		} `json:"networks"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &boxes); err != nil {
		return "", fmt.Errorf("parsing `container inspect %s`: %w", name, err)
	}
	for _, b := range boxes {
		for _, n := range b.Networks {
			if ip := stripMask(n.Address); ip != "" {
				return ip, nil
			}
		}
	}
	return "", fmt.Errorf("no address in `container inspect %s` — is it running?", name)
}

// parallelsIP reads the DHCP address Parallels handed a VM. Parallels VMs use
// dynamic leases, so the address is looked up via prlctl rather than derived.
//
// `prlctl list <name> -f --json` returns a JSON array; the guest-reported
// address is the "ip_configured" field. (Confirmed against the live tool on
// the Mac host.)
func parallelsIP(ctx context.Context, name string) (string, error) {
	res, err := exec.Capture(ctx, exec.Cmd{Name: "prlctl", Args: []string{"list", name, "-f", "--json"}})
	if err != nil {
		return "", err
	}
	if res.Failed() {
		return "", fmt.Errorf("`prlctl list %s` failed: %s", name, oneLine(res.Stderr))
	}
	var vms []struct {
		IPConfigured string `json:"ip_configured"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &vms); err != nil {
		return "", fmt.Errorf("parsing `prlctl list %s --json`: %w", name, err)
	}
	for _, vm := range vms {
		for _, field := range strings.Fields(strings.ReplaceAll(vm.IPConfigured, ",", " ")) {
			if ip := stripMask(field); ip != "" && ip != "-" {
				return ip, nil
			}
		}
	}
	return "", fmt.Errorf("`prlctl list %s` reported no address — is the VM running with guest tools?", name)
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
