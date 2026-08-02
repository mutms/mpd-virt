package cli

import (
	"context"
	"fmt"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/proxy"
	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/sshconfig"
	"github.com/mutms/mpd-virt/go/internal/vmid"
	"github.com/spf13/cobra"
)

// wgListenPort is the UDP port every VM's wg0 endpoint listens on. Fixed, so
// mpd-virt can compose the endpoint from the box's IP alone.
const wgListenPort = 51820

// setupReachability wires an adopted box into the WireGuard overlay through a
// running mpd-proxy: it brings up a wg0 endpoint on the VM (authorized for
// mpd-proxy's key), reads back the VM's key, and registers the peer + DNS route
// with mpd-proxy. After it, this Mac reaches the box's 10.163.<NNN>.x services
// transparently, encrypted, with no per-VM route or resolver file.
//
// If mpd-proxy is not running it is skipped with a hint rather than failing —
// adoption is complete regardless, and `mpd-virt sync <NNN>` finishes the job
// once the proxy is up.
func setupReachability(ctx context.Context, t host.Target, id vmid.ID, ip string) error {
	pc := proxy.New(proxy.DefaultSocket)
	proxyPub, err := pc.Pubkey()
	if err != nil {
		fmt.Printf("  … mpd-proxy not reachable — skipping WireGuard.\n"+
			"    Start it (`sudo mpd-proxy up`), then: mpd-virt sync %s\n", id.Pad())
		return nil
	}

	octet := int(id)
	fmt.Printf("\n▶ WireGuard reachability (via mpd-proxy)\n")

	// vm-setup has already brought up wg0 (its key, interface, ip_forward). Add
	// mpd-proxy as its peer and persist it with `wg-quick save`, then read the
	// VM's public key. The VM's private key never leaves the box.
	//
	// The `ip route` is not redundant with the peer's allowed-ips: `wg set`
	// records 10.163.0.1 in WireGuard's crypto routing (which peer to encrypt
	// to) but never touches the kernel routing table — only `wg-quick up` does,
	// and wg0 was already up before this peer existed. Without the route the VM
	// decrypts our pings fine but sends replies to 10.163.0.1 out its LAN
	// default gateway, where they die. `wg-quick save` writes the peer's
	// allowed-ips into wg0.conf, so a reboot's `wg-quick up` re-adds this route
	// on its own — the explicit add is only for immediate effect at adoption.
	setPeer := fmt.Sprintf(
		"sudo wg set wg0 peer %s allowed-ips 10.163.0.1/32 && "+
			"sudo ip route replace 10.163.0.1/32 dev wg0 && "+
			"sudo wg-quick save wg0",
		proxyPub)
	if code, err := t.Stream(ctx, setPeer); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("adding mpd-proxy as wg0 peer failed — is wg0 up? (run `mpd --vm-setup`)")
	}

	vmPub, err := t.Line(ctx, "sudo wg show wg0 public-key")
	if err != nil {
		return err
	}
	vm := proxy.VM{
		ID:         id.Pad(),
		PublicKey:  vmPub,
		Endpoint:   fmt.Sprintf("%s:%d", ip, wgListenPort),
		AllowedIPs: []string{fmt.Sprintf("10.163.%d.0/24", octet)},
		Resolver:   fmt.Sprintf("10.163.%d.1:53", octet),
	}
	if err := pc.Add(vm); err != nil {
		return fmt.Errorf("register with mpd-proxy: %w", err)
	}
	pass("mpd-proxy peer " + vm.Endpoint + " → " + vm.AllowedIPs[0] + " (DNS via " + vm.Resolver + ")")
	return nil
}

// syncCmd re-points everything at wherever the box is now: it re-resolves the
// box by name (catching a restarted container's new lease), updates the
// registry and ssh-config if it moved, and re-registers the mpd-proxy peer +
// DNS. The command you run after a box restarts/moves, after editing its env
// file, or after restarting mpd-proxy.
func syncCmd() *cobra.Command {
	var username string
	cmd := &cobra.Command{
		Use:   "sync <NNN>",
		Short: "Re-point a box: re-resolve its IP, refresh ssh-config + WireGuard peer/DNS",
		Long: "Re-points everything at wherever the box is now. sync re-resolves\n" +
			"mpd-<NNN> by name (picking up a restarted container's new lease),\n" +
			"falling back to the IP stored in ~/.mpd-virt/<NNN>/env; if it moved\n" +
			"it updates the registry, rewrites the ~/.ssh/config block, and\n" +
			"re-registers the mpd-proxy WireGuard endpoint + DNS. Run it after a\n" +
			"box restarts or moves, after editing its env file, or after\n" +
			"restarting mpd-proxy.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := vmid.Parse(args[0])
			if err != nil {
				return err
			}
			e, err := registry.Load(id)
			if err != nil {
				return fmt.Errorf("%s is not adopted (no registry entry): %w", id.Name(), err)
			}
			user := username
			if user == "" {
				user = e.User
			}
			// Re-resolve where the box is now rather than trusting the stored
			// IP: a restarted container or a moved VM may have a new lease.
			// ResolveIP falls back to the stored IP when the name does not
			// resolve, so a box that has not moved still syncs.
			ip, err := backend.ResolveIP(cmd.Context(), id)
			if err != nil {
				return err
			}
			if ip != e.IP {
				fmt.Printf("  %s is at %s now (was %s) — updating registry\n", id.Name(), ip, e.IP)
				if err := registry.Save(registry.Entry{ID: id, IP: ip, User: user, Backend: e.Backend}); err != nil {
					return fmt.Errorf("registry: %w", err)
				}
			}
			// The IP lives in two host-side places; keep the direct-ssh block in
			// step with the endpoint mpd-proxy is about to get.
			if err := sshconfig.Write(id, ip, user); err != nil {
				return fmt.Errorf("ssh config: %w", err)
			}
			return setupReachability(cmd.Context(), host.Target{User: user, Host: ip}, id, ip)
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "dev user on the box (defaults to the registry entry)")
	return cmd
}
