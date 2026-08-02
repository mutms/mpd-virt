package cli

import (
	"context"
	"fmt"

	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/proxy"
	"github.com/mutms/mpd-virt/go/internal/registry"
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

	// Bring up wg0 on the VM: install wireguard, generate the VM's key if it
	// has none, write a config authorizing mpd-proxy, enable at boot, (re)start.
	// The VM's private key is read on the VM and never leaves it.
	script := fmt.Sprintf(`set -e
sudo apt-get install -y wireguard-tools >/dev/null
umask 077
sudo install -d -m 700 /etc/wireguard
[ -f /etc/wireguard/mpd.key ] || wg genkey | sudo tee /etc/wireguard/mpd.key >/dev/null
sudo chmod 600 /etc/wireguard/mpd.key
printf '[Interface]\nAddress = 10.163.0.%d/32\nListenPort = %d\nPrivateKey = %%s\n\n[Peer]\nPublicKey = %s\nAllowedIPs = 10.163.0.1/32\n' "$(sudo cat /etc/wireguard/mpd.key)" | sudo tee /etc/wireguard/wg0.conf >/dev/null
sudo chmod 600 /etc/wireguard/wg0.conf
sudo systemctl enable wg-quick@wg0 >/dev/null 2>&1 || true
sudo systemctl restart wg-quick@wg0
`, octet, wgListenPort, proxyPub)
	if err := t.WriteRemote(ctx, script, "/tmp/mpd-wg-setup.sh", "0700"); err != nil {
		return fmt.Errorf("push WG setup: %w", err)
	}
	if code, err := t.Stream(ctx, "bash /tmp/mpd-wg-setup.sh"); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("WG endpoint setup failed (exit %d)", code)
	}

	vmPub, err := t.Line(ctx, "sudo cat /etc/wireguard/mpd.key | wg pubkey")
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

// syncCmd re-runs reachability for an already-adopted box — the command you run
// after (re)starting mpd-proxy to re-register a VM's peer + DNS.
func syncCmd() *cobra.Command {
	var username string
	cmd := &cobra.Command{
		Use:   "sync <NNN>",
		Short: "(Re)register an adopted box's WireGuard peer + DNS with mpd-proxy",
		Args:  cobra.ExactArgs(1),
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
			return setupReachability(cmd.Context(), host.Target{User: user, Host: e.IP}, id, e.IP)
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "dev user on the box (defaults to the registry entry)")
	return cmd
}
