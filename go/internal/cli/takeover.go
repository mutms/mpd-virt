package cli

import (
	"context"
	"fmt"
	"os/user"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/ca"
	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/server"
	"github.com/mutms/mpd-virt/go/internal/sshconfig"
	"github.com/mutms/mpd-virt/go/internal/vmid"
	"github.com/spf13/cobra"
)

// bootstrapBaseURL is where the two wget'able bootstrap steps fetch
// themselves from. The rest run from the checkout 20-git-clone lands.
const bootstrapBaseURL = "https://raw.githubusercontent.com/mutms/mpd/main/bootstrap"

// takeoverCmd adopts a box as mpd-<NNN>, installing mpd from source.
//
// A takeover target is a stock Debian Trixie VM with only its *identity*
// set up — hostname mpd-<NNN>, the dev user, this Mac's key authorized,
// passwordless sudo. mpd itself is NOT required: takeover clones it from
// GitHub and compiles it in place. What it verifies first is only that
// the box at the given IP really is mpd-<NNN> — then it refuses to touch
// the wrong one, rather than remediating it.
func takeoverCmd() *cobra.Command {
	var username, backendFlag string
	cmd := &cobra.Command{
		Use:   "takeover <NNN> [IP]",
		Short: "Adopt a Debian box as mpd-<NNN> (IP resolved by name if omitted)",
		Long: "With no IP, mpd-virt finds the box by name: it resolves mpd-<NNN>\n" +
			"through the system resolver (how Parallels and Apple containers\n" +
			"register a box, and how a box running mDNS advertises itself), and\n" +
			"failing that falls back to the last address on file — which covers\n" +
			"Proxmox and any re-takeover. An explicit <IP> always overrides, and\n" +
			"is required for the first takeover of a box that neither resolves nor\n" +
			"is on file. Either way mpd-virt verifies the box there really is\n" +
			"mpd-<NNN> by its hostname before touching it. The box need only be\n" +
			"stock Debian Trixie with its identity set up (hostname, dev user,\n" +
			"authorized key, passwordless sudo) — mpd is cloned from GitHub and\n" +
			"built in place.\n\n" +
			"--backend records which platform the box runs on\n" +
			"(" + backend.List() + ") for later lifecycle\n" +
			"commands; it does not affect how the box is reached. It is\n" +
			"required only for the first takeover: a re-takeover of a\n" +
			"registered box reads it from the registry (pass it to change it).",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := vmid.Parse(args[0])
			if err != nil {
				return err
			}
			if backendFlag == "" {
				e, err := registry.Load(id)
				if err != nil {
					return fmt.Errorf("--backend is required for the first takeover of %s (one of %s)",
						id.Name(), backend.List())
				}
				backendFlag = e.Backend
				fmt.Printf("backend %s (from the registry)\n", backendFlag)
			}
			be, err := backend.Parse(backendFlag)
			if err != nil {
				return err
			}
			ip := ""
			if len(args) == 2 {
				ip = args[1]
			} else if ip, err = backend.Start(cmd.Context(), cmd.OutOrStdout(), id, be); err != nil {
				return fmt.Errorf("%w\n    or pass the IP explicitly: mpd-virt takeover %s <IP> --backend %s",
					err, id.Pad(), be)
			} else {
				fmt.Printf("resolved %s → %s\n", id.Name(), ip)
			}
			return runTakeover(cmd.Context(), id, ip, username, be)
		},
	}
	cmd.Flags().StringVar(&username, "username", defaultUser(),
		"dev user on the box (defaults to the current macOS user)")
	cmd.Flags().StringVar(&backendFlag, "backend", "",
		"platform the box runs on ("+backend.List()+") — required for the first takeover, read from the registry after")
	return cmd
}

// defaultUser mirrors the proposal's rule: the box's dev user defaults to
// `whoami`, which is what mpd's own identity detection uses.
func defaultUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "skodak"
}

func runTakeover(ctx context.Context, id vmid.ID, ip, username string, be backend.Backend) error {
	t := host.Target{User: username, Host: ip}
	fmt.Printf("takeover %s at %s@%s  (backend=%s, zone=%s)\n\n",
		id.Name(), username, ip, be, id.Zone())

	// --- Identity conformance. Key auth + hostname are two independent
	//     confirmations that the box here is the one meant; refuse on any
	//     mismatch. mpd need NOT be present — takeover installs it.
	if err := t.CheckReachable(ctx); err != nil {
		return err
	}
	pass("SSH key auth")

	got, err := t.Line(ctx, "hostname")
	if err != nil {
		return err
	}
	if got != id.Name() {
		return fmt.Errorf("hostname mismatch: box at %s calls itself %q, expected %q — refusing", ip, got, id.Name())
	}
	pass("hostname " + got)

	if r, err := t.Run(ctx, "sudo -n true"); err != nil {
		return err
	} else if r.Failed() {
		return fmt.Errorf("passwordless sudo not available for %s@%s", username, ip)
	}
	pass("passwordless sudo")

	osID, err := t.Line(ctx, ". /etc/os-release && printf '%s %s' \"$ID\" \"$VERSION_CODENAME\"")
	if err != nil {
		return err
	}
	if osID != "debian trixie" {
		return fmt.Errorf("OS mismatch: %q, expected \"debian trixie\"", osID)
	}
	pass("Debian Trixie")

	// The box must already be on systemd-resolved (the prepare script
	// converts the network stack). Fail fast with the fix rather than
	// after the long clone + build that vm-setup would then reject.
	if r, err := t.Run(ctx, "systemctl is-active --quiet systemd-resolved"); err != nil {
		return err
	} else if r.Failed() {
		return fmt.Errorf("box at %s is not prepared — systemd-resolved is not active.\n"+
			"Run the prepare script on the VM first (follow its reboot prompt until it says ready):\n"+
			"    bash <(wget -qO- https://raw.githubusercontent.com/mutms/mpd/main/setup/mpd-prepare-takeover.sh)", ip)
	}
	pass("network prepared (systemd-resolved active)")

	// --- Per-VM CA generated on the Mac. Root key stays here; the
	//     intermediate is name-constrained to this box's zone.
	if err := ca.LoadOrGenerateRoot(); err != nil {
		return fmt.Errorf("root CA: %w", err)
	}
	if err := ca.LoadOrGenerateVM(id); err != nil {
		return fmt.Errorf("per-VM CA: %w", err)
	}
	pass("per-VM CA generated (" + id.Zone() + " only)")

	// --- Provision. Install mpd from source, push the CA in, build, set
	//     up the platform. All the bootstrap steps are idempotent, so a
	//     re-run resumes cleanly.
	if err := step(ctx, t, "10-passwordless-sudo",
		"bash <(wget -qO- "+bootstrapBaseURL+"/10-passwordless-sudo.sh)"); err != nil {
		return err
	}
	if err := step(ctx, t, "20-git-clone (clone mpd → /opt/mpd)",
		"bash <(wget -qO- "+bootstrapBaseURL+"/20-git-clone.sh)"); err != nil {
		return err
	}

	// The CA push needs /var/lib/mpd, which 20-git-clone creates.
	const caDir = "/var/lib/mpd/conf/caroot"
	for _, p := range []struct{ local, remote, mode string }{
		{ca.RootCertPath(), caDir + "/rootCA.pem", "0644"},
		{ca.VMCertPath(id), caDir + "/vmCA.pem", "0644"},
		{ca.VMKeyPath(id), caDir + "/vmCA-key.pem", "0600"},
	} {
		if err := t.Install(ctx, p.local, p.remote, p.mode); err != nil {
			return fmt.Errorf("push CA: %w", err)
		}
	}
	pass("per-VM CA pushed → " + caDir)

	// LAN service names, same trip and the same precondition. Only the push
	// is needed here: the `mpd --vm-setup` below publishes the file into
	// dnsmasq as part of its DNS reconcile, so an adopted box answers for
	// forge.mpd.test from the start rather than only after a `server sync`.
	if _, err := pushLanHosts(ctx, t); err != nil {
		return fmt.Errorf("push LAN hosts: %w", err)
	}
	pass("LAN service names pushed → " + server.RemoteLanHostsPath)

	// The developer's own assets, if they keep any. Best-effort: unlike the
	// CA and the LAN names, nothing mpd-virt does depends on them.
	syncAssets(ctx, t, id.Pad())

	// Their MPD_* defaults, same deal — and before the `mpd --vm-setup`
	// below, which seeds that same path from mpd's own template only when
	// nothing is there. Pushing first means an adopted box starts out
	// already agreeing with every other box this Mac owns.
	syncMpdEnv(ctx, t, id.Pad())

	if err := step(ctx, t, "40-install-software",
		"bash /opt/mpd/bootstrap/40-install-software.sh"); err != nil {
		return err
	}
	if err := step(ctx, t, "50-build (compile /opt/mpd/bin/mpd)",
		"bash /opt/mpd/bootstrap/50-build.sh"); err != nil {
		return err
	}

	// mpd derives its identity from the hostname (mpd-<NNN>) and reads its
	// own IP off the interface.
	if err := step(ctx, t, "mpd --vm-setup", "/opt/mpd/bin/mpd --vm-setup"); err != nil {
		return err
	}

	// --- Record the adopted box (host-side).
	if err := registry.Save(registry.Entry{ID: id, IP: ip, User: username, Backend: string(be)}); err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	pass("registry " + paths.VMEnv(id))
	if err := sshconfig.Write(id, ip, username); err != nil {
		return fmt.Errorf("ssh config: %w", err)
	}
	pass("~/.ssh/config block  (ssh " + id.Name() + " → runtime, " +
		sshconfig.VMAlias(id) + " → the VM)")

	// --- WireGuard reachability via mpd-proxy. Best-effort: adoption is done,
	//     so a proxy hiccup is a warning with a re-run hint, not a failure.
	if _, err := setupReachability(ctx, t, id, ip); err != nil {
		fmt.Printf("  ⚠ WireGuard setup incomplete: %v\n    Re-run once fixed: mpd-virt start %s\n", err, id.Pad())
	}
	checkCATrust(ctx, id)

	fmt.Printf("\n✓ %s adopted.\n", id.Name())
	return nil
}

// step runs one streamed bootstrap command, printing a heading and
// failing on a non-zero remote exit.
func step(ctx context.Context, t host.Target, title, remote string) error {
	fmt.Printf("\n▶ %s\n", title)
	code, err := t.Stream(ctx, remote)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s failed (exit %d)", title, code)
	}
	return nil
}

func pass(msg string) { fmt.Printf("  ✓ %s\n", msg) }
