package cli

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/exec"
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
// once the proxy is up.
func setupReachability(ctx context.Context, t host.Target, id vmid.ID, ip string) error {
	pc := proxy.New(proxy.DefaultSocket)
	proxyPub, err := pc.Pubkey()
	if err != nil {
		fmt.Printf("  … mpd-proxy not reachable — skipping WireGuard.\n"+
			"    Start it (`sudo mpd-proxy up`), then: mpd-virt start %s\n", id.Pad())
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
			"it: powers the box on through its backend (not yet — assumed already\n" +
			"running for every backend today), resolves its current IP by name\n" +
			"(falling back to the last known one), updates the registry and\n" +
			"~/.ssh/config if it moved, registers the mpd-proxy WireGuard endpoint\n" +
			"+ DNS, and verifies routing + DNS through the tunnel. Run it after a\n" +
			"box (re)starts or moves, or after restarting mpd-proxy.",
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
			// Power the box on through its backend (no-op for now — see powerOn).
			if err := powerOn(cmd.Context(), e); err != nil {
				return err
			}
			// Re-resolve where the box is now rather than trusting the stored
			// IP: a restarted container or a moved VM may have a new lease.
			// ResolveIP falls back to the stored IP when the name does not
			// resolve. When mpd-virt just powered the box on, wait for it to
			// finish booting before giving up on resolving it.
			ip, err := resolveReady(cmd.Context(), id, powerArgv(e, "start") != nil)
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
			if err := setupReachability(cmd.Context(), host.Target{User: user, Host: ip}, id, ip); err != nil {
				return err
			}
			verifyReachable(cmd.Context(), id)
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
			"box off through its backend (not yet; a no-op for every backend\n" +
			"today, so the box keeps running and `start` re-attaches it).",
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
			// Power the box off through its backend (no-op for now — see powerOff).
			return powerOff(cmd.Context(), e)
		},
	}
	return cmd
}

// powerOn brings the box up through its backend before reachability is wired.
// mpd-virt controls power only for the backends whose CLI runs on this Mac:
// Apple containers (`container start`) and Parallels VMs (`prlctl start`).
// generic and proxmox boxes are assumed already running (nothing to do here —
// proxmox lives on a remote host, generic is hand-managed). A start that fails
// is not fatal: most often the box is already running, and the reachability
// check that follows is the real test of whether it is up.
func powerOn(ctx context.Context, e registry.Entry) error {
	return power(ctx, e, "start", "running")
}

// powerOff stops the box through its backend after it is detached from the
// overlay. Same backend rule as powerOn; a stop that fails (already stopped)
// is not fatal.
func powerOff(ctx context.Context, e registry.Entry) error {
	return power(ctx, e, "stop", "stopped")
}

// power runs one backend power verb ("start"/"stop"). It is best-effort and
// never fatal: whether the box actually came up is decided by the reachability
// check that follows, not here. A non-zero exit usually means the box is
// already in the target state; a launch error means the backend CLI is not on
// this machine (mpd-virt may be driving a box powered elsewhere) — both are
// reported and swallowed.
func power(ctx context.Context, e registry.Entry, verb, already string) error {
	argv := powerArgv(e, verb)
	if argv == nil {
		return nil // backend mpd-virt does not power
	}
	fmt.Printf("  ▶ %s\n", strings.Join(argv, " "))
	r, err := exec.Capture(ctx, exec.Cmd{Name: argv[0], Args: argv[1:]})
	if err != nil {
		fmt.Printf("    … %s unavailable here (%v) — assuming the box is managed elsewhere\n", argv[0], err)
		return nil
	}
	if r.Failed() {
		fmt.Printf("    … %s (continuing — the box may already be %s)\n", shortErr(r), already)
	}
	return nil
}

// powerArgv is the backend's CLI invocation for a power verb, or nil for a
// backend mpd-virt does not power. The Apple `container` and Parallels
// `prlctl` CLIs both take the box name (mpd-<NNN>).
func powerArgv(e registry.Entry, verb string) []string {
	switch e.Backend {
	case string(backend.Container):
		return []string{"container", verb, e.ID.Name()}
	case string(backend.Parallels):
		return []string{"prlctl", verb, e.ID.Name()}
	default:
		return nil
	}
}

// shortErr collapses a failed command's output to a single line for a warning.
func shortErr(r exec.Result) string {
	s := strings.TrimSpace(r.Stderr)
	if s == "" {
		s = strings.TrimSpace(r.Stdout)
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "non-zero exit"
	}
	return s
}

// resolveReady resolves the box's IP, waiting for it to become reachable when
// mpd-virt just powered it on: a booting VM/container takes time to get an
// address and open ssh. A box mpd-virt does not power is resolved once — it is
// expected to be up already, and waiting on a down one would only stall.
func resolveReady(ctx context.Context, id vmid.ID, powered bool) (string, error) {
	attempts := 1
	if powered {
		attempts = 30 // ~90s at 3s spacing — generous for a cold VM boot
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i == 1 {
			fmt.Printf("  … waiting for %s to come up\n", id.Name())
		}
		if i > 0 {
			time.Sleep(3 * time.Second)
		}
		ip, err := backend.ResolveIP(ctx, id)
		if err == nil {
			return ip, nil
		}
		lastErr = err
	}
	return "", lastErr
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
