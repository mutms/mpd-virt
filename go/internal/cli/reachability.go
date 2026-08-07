package cli

import (
	"context"
	"fmt"
	"net"
	"time"

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
// adoption is complete regardless, and `mpd-virt start <NNN>` finishes the job
// once the proxy is up. The bool reports whether the overlay was actually
// wired, so callers skip the tunnel verification after a skip instead of
// probing a route that cannot exist yet.
func setupReachability(ctx context.Context, t host.Target, id vmid.ID, ip string) (bool, error) {
	pc := proxy.New(proxy.DefaultSocket)
	proxyPub, err := pc.Pubkey()
	if err != nil {
		fmt.Printf("  … mpd-proxy not detected — skipping WireGuard overlay.\n"+
			"    Start it (`sudo mpd-proxy up`), then: mpd-virt start %s\n", id.Pad())
		printSocksHint(id)
		return false, nil
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
		return false, err
	} else if code != 0 {
		return false, fmt.Errorf("adding mpd-proxy as wg0 peer failed — is wg0 up? (run `mpd --vm-setup`)")
	}

	vmPub, err := t.Line(ctx, "sudo wg show wg0 public-key")
	if err != nil {
		return false, err
	}
	vm := proxy.VM{
		ID:        id.Pad(),
		PublicKey: vmPub,
		Endpoint:  fmt.Sprintf("%s:%d", ip, wgListenPort),
		// Route the whole 10.163.<NNN>.0/24 container subnet over the tunnel —
		// that is the tunnel's entire purpose: the Mac browses project URLs
		// served at runtime-container IPs, and reaches databases and service
		// containers directly. DNS also lives on the gateway .1. The box-side
		// nft rule (mpd vm-setup) exempts wg0 and seals the subnet only from
		// the LAN/public interface.
		AllowedIPs: []string{fmt.Sprintf("10.163.%d.0/24", octet)},
		Resolver:   fmt.Sprintf("10.163.%d.1:53", octet),
	}
	if err := pc.Add(vm); err != nil {
		return false, fmt.Errorf("register with mpd-proxy: %w", err)
	}
	pass("mpd-proxy peer " + vm.Endpoint + " → " + vm.AllowedIPs[0] + " (DNS via " + vm.Resolver + ")")
	return true, nil
}

// printSocksHint tells the user how to reach the box without mpd-proxy: the
// SOCKS-over-plain-SSH tier baked into the box's managed ssh-config block. The
// box's direct IP is reachable independently of the overlay, so this path works
// whenever the overlay does not.
func printSocksHint(id vmid.ID) {
	fmt.Printf(
		"\n  SOCKS over plain SSH:\n"+
			"    1. ssh -N %s\n"+
			"    2. Point a browser at SOCKS5 127.0.0.1:%d with remote DNS enabled —\n"+
			"       Firefox: Settings → Network Settings → Manual proxy, SOCKS v5 host\n"+
			"       127.0.0.1 port %d, and tick \"Proxy DNS when using SOCKS v5\".\n"+
			"    The \"mpd Root CA\" in your Keychain makes https://*.mpd.test trust work.\n",
		sshconfig.SocksAlias(id), sshconfig.SocksPort, sshconfig.SocksPort)
}

// startCmd brings an adopted box into service and points the Mac at it: power
// it on through its backend, resolve its current IP, wire WireGuard
// reachability, and verify the overlay. It re-resolves by name so a box that
// moved (a restarted container's new lease) is found wherever it is now. This
// is the one command to run after a box (re)starts or moves — there is no
// separate "sync".
func startCmd() *cobra.Command {
	var username string
	cmd := &cobra.Command{
		Use:   "start <NNN>",
		Short: "Bring an adopted box into service: resolve its IP, wire reachability, verify",
		Long: "Brings an already-adopted box into service and points the Mac at\n" +
			"it: powers the box on through its backend (container/parallels/utm;\n" +
			"generic and proxmox are assumed already running), finds the IP it\n" +
			"came up on, updates the registry and ~/.ssh/config if it moved,\n" +
			"registers the mpd-proxy WireGuard endpoint + DNS, and verifies\n" +
			"routing + DNS through the tunnel. Run it after a box (re)starts or\n" +
			"moves, or after restarting mpd-proxy.",
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
			// Bring the box up and find where it is: the backend powers it on
			// (a no-op for generic/proxmox) and returns the IP it came up on,
			// waiting while it boots. A moved/restarted box is found wherever it
			// is now — no reachable IP means it did not start.
			ip, err := backend.Start(cmd.Context(), cmd.OutOrStdout(), id, backend.Backend(e.Backend))
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
			t := host.Target{User: user, Host: ip}
			// LAN names, before reachability rather than after: a changed
			// file republishes via `mpd --vm-setup`, which restarts wg0 and
			// would drop the mpd-proxy peer added below. Unchanged — the
			// usual case — this is one remote sha256sum and nothing else.
			// Best-effort: a box that came up fine should not fail to start
			// over a hosts file.
			if changed, err := syncLanHosts(cmd.Context(), t); err != nil {
				fmt.Printf("  ⚠ LAN hosts sync failed: %v\n    run `mpd-virt server sync %s`\n", err, id.Pad())
			} else if changed {
				pass("LAN service names republished")
			}
			wired, err := setupReachability(cmd.Context(), t, id, ip)
			if err != nil {
				return err
			}
			if wired {
				verifyReachable(cmd.Context(), id)
			}
			checkCATrust(cmd.Context(), id)
			return nil
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "dev user on the box (defaults to the registry entry)")
	return cmd
}

// stopCmd takes an adopted box out of service: detach it from the overlay and
// power it off through its backend. The inverse of start.
func stopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <NNN>",
		Short: "Take an adopted box out of service: detach it from the WireGuard overlay",
		Long: "Detaches the box from the overlay — removes its mpd-proxy peer, so\n" +
			"the Mac stops routing to its 10.163.<NNN>.x network — and powers the\n" +
			"box off through its backend (container/parallels/utm; a no-op for\n" +
			"generic/proxmox, which keep running so `start` re-attaches them).",
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
			// Detach from the overlay: drop the mpd-proxy peer. If mpd-proxy is
			// down there is nothing to detach, which is fine.
			pc := proxy.New(proxy.DefaultSocket)
			if err := pc.Remove(id.Pad()); err != nil {
				fmt.Printf("  … mpd-proxy not reachable — nothing to detach (%v)\n", err)
			} else {
				pass("detached from overlay (mpd-proxy peer removed)")
			}
			// Power the box off through its backend (a no-op for generic/proxmox).
			return backend.Stop(cmd.Context(), cmd.OutOrStdout(), id, backend.Backend(e.Backend))
		},
	}
	return cmd
}

// verifyReachable confirms the overlay actually works after wiring it, by
// asking the box's own dnsmasq (10.163.<NNN>.1:53, reachable only through the
// tunnel) to resolve the box's zone. Success proves both that the WireGuard
// path routes and that the in-VM resolver answers. It is a health check, not a
// gate: a failure warns rather than fails start, since the peer is registered
// regardless and a fresh tunnel can take a moment to settle (hence the retry).
func verifyReachable(ctx context.Context, id vmid.ID) {
	gateway := fmt.Sprintf("10.163.%d.1", int(id))
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 3 * time.Second}
			return d.DialContext(ctx, network, net.JoinHostPort(gateway, "53"))
		},
	}
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Second)
		}
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		addrs, err := r.LookupHost(c, id.Zone())
		cancel()
		if err == nil {
			for _, a := range addrs {
				if a == gateway {
					pass("overlay verified: " + id.Zone() + " → " + gateway + " (routing + DNS through the tunnel)")
					return
				}
			}
		}
	}
	fmt.Printf("  ⚠ overlay check: could not resolve %s → %s through the tunnel yet.\n"+
		"    Give it a moment and retry, or check `sudo mpd-proxy` and wg0 on the box.\n", id.Zone(), gateway)
}
