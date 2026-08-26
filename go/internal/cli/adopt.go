package cli

import (
	"context"
	"fmt"
	"net/netip"
	"os/user"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/ca"
	"github.com/mutms/mpd-virt/go/internal/config"
	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/server"
	"github.com/mutms/mpd-virt/go/internal/sshconfig"
	"github.com/mutms/mpd-virt/go/internal/vmid"
	"github.com/spf13/cobra"
)

// bootstrapRef pins mpd's bootstrap scripts — the three steps that are
// piped straight from GitHub into bash with passwordless sudo behind them,
// before any checkout exists on the VM to audit. A commit hash, not a
// branch: what runs is exactly what was reviewed when the pin was bumped,
// not whatever `main` holds at that instant. Bump deliberately, like the
// cloud-image pin, and together with MPD_BOOTSTRAP_REF in
// container/Containerfile, which bakes step 20 into the Apple image at
// the same ref. (The clone itself, and `update`, deliberately track mpd's
// main — that is the platform's own trust decision, and the checkout at
// least leaves auditable history on the VM; see docs/security.md.)
const bootstrapRef = "9dec27a152a02670992d2d4140ef3d8517af74e7"

// bootstrapBaseURL is where the bootstrap steps are fetched from, at the
// pinned ref. Step 30 lands the checkout; everything after runs from it.
const bootstrapBaseURL = "https://raw.githubusercontent.com/mutms/mpd/" + bootstrapRef + "/bootstrap"

// bootstrapStep fetches one step at the pinned ref and runs it. Fetched to
// a file first: `bash <(wget …)` turns a failed download into an empty
// script that exits 0, and the whole bootstrap silently does nothing.
func bootstrapStep(script string) string {
	url := bootstrapBaseURL + "/" + script
	return "f=$(mktemp) && wget -qO \"$f\" " + url + " && bash \"$f\"; rc=$?; rm -f \"$f\"; exit $rc"
}

// adoptCmd adopts a VM as mpd-<NNN>, installing mpd from source.
//
// A adoption target is a stock Debian Trixie VM with only its *identity*
// set up — hostname mpd-<NNN>, the dev user, this Mac's key authorized,
// passwordless sudo. mpd itself is NOT required: adoption clones it from
// GitHub and compiles it in place. What it verifies first is only that
// the VM at the given IP really is mpd-<NNN> — then it refuses to touch
// the wrong one, rather than remediating it.
func adoptCmd() *cobra.Command {
	var username, backendFlag string
	cmd := &cobra.Command{
		Use:   "adopt <NNN> [IP]",
		Short: "Adopt a Debian VM as mpd-<NNN> (IP resolved by name if omitted)",
		Long: "With no IP, mpd-virt finds the VM by name: it resolves mpd-<NNN>\n" +
			"through the system resolver (how Parallels and Apple containers\n" +
			"register a VM, and how a VM running mDNS advertises itself), and\n" +
			"failing that falls back to the last address on file — which covers\n" +
			"Proxmox and any re-adoption. An explicit <IP> always overrides, and\n" +
			"is required for the first adoption of a VM that neither resolves nor\n" +
			"is on file. Either way mpd-virt verifies the VM there really is\n" +
			"mpd-<NNN> by its hostname before touching it. The VM need only be\n" +
			"stock Debian Trixie with its identity set up (hostname, dev user,\n" +
			"authorized key, passwordless sudo) — mpd is cloned from GitHub and\n" +
			"built in place.\n\n" +
			"--backend records which platform the VM runs on\n" +
			"(" + backend.List() + ") for later lifecycle\n" +
			"commands; it does not affect how the VM is reached. It is\n" +
			"required only for the first adoption: a re-adoption of a\n" +
			"registered VM reads it from the registry (pass it to change it).",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := vmid.Parse(args[0])
			if err != nil {
				return err
			}
			if backendFlag == "" {
				// A re-adoption reads the backend recorded at first adoption;
				// a first adoption falls back to the configured default_backend,
				// which is what lets a purged fleet be re-adopted without
				// --backend on every VM.
				if e, err := registry.Load(id); err == nil {
					backendFlag = e.Backend
					fmt.Printf("backend %s (from the registry)\n", backendFlag)
				} else if def, err := configuredDefaultBackend(); err != nil {
					return err
				} else if def != "" {
					backendFlag = string(def)
					fmt.Printf("backend %s (default from config.json)\n", backendFlag)
				} else {
					return fmt.Errorf("--backend is required for the first adoption of %s (one of %s)",
						id.Name(), backend.List())
				}
			}
			be, err := backend.Parse(backendFlag)
			if err != nil {
				return err
			}
			ip := ""
			if len(args) == 2 {
				// The same rule locate() enforces on discovered candidates:
				// only a literal IPv4 address reaches the registry and
				// ~/.ssh/config, even when typed by hand.
				a, err := netip.ParseAddr(args[1])
				if err != nil || !a.Is4() {
					return fmt.Errorf("%q is not an IPv4 address — adopt takes the VM's literal address", args[1])
				}
				ip = a.String()
			} else if ip, err = backend.Start(cmd.Context(), cmd.OutOrStdout(), id, be); err != nil {
				return fmt.Errorf("%w\n    or pass the IP explicitly: mpd-virt adopt %s <IP> --backend %s",
					err, id.String(), be)
			} else {
				fmt.Printf("resolved %s → %s\n", id.Name(), ip)
			}
			return runAdopt(cmd.Context(), id, ip, username, be)
		},
	}
	cmd.Flags().StringVar(&username, "username", defaultUser(),
		"dev user on the VM (defaults to the current macOS user)")
	cmd.Flags().StringVar(&backendFlag, "backend", "",
		"platform the VM runs on ("+backend.List()+") — required for the first adoption, read from the registry after")
	return cmd
}

// configuredDefaultBackend returns the default backend from
// ~/.mpd-virt/config.json (default_backend), or "" when none is set. A
// malformed config or an unknown backend name is an error, so a typo surfaces
// rather than silently falling through to the "--backend required" message.
func configuredDefaultBackend() (backend.Backend, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	if cfg.DefaultBackend == "" {
		return "", nil
	}
	return backend.Parse(cfg.DefaultBackend)
}

// defaultUser mirrors the proposal's rule: the VM's dev user defaults to
// `whoami`, which is what mpd's own identity detection uses.
func defaultUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "skodak"
}

func runAdopt(ctx context.Context, id vmid.ID, ip, username string, be backend.Backend) error {
	if strings.ContainsAny(username, " \t\"") || username == "" {
		// Checked up front: the same rule sshconfig.Write enforces at the
		// end, but failing after the full bootstrap would waste ten minutes.
		return fmt.Errorf("invalid --username %q", username)
	}
	t := vmTarget(id, username, ip)
	fmt.Printf("adopt %s at %s@%s  (backend=%s, zone=%s)\n\n",
		id.Name(), username, ip, be, id.Zone())

	// --- Identity conformance. Key auth + hostname are two independent
	//     confirmations that the vm here is the one meant; refuse on any
	//     mismatch. mpd need NOT be present — adoption installs it.
	//     The first contact records the vm's host key into
	//     ~/.mpd-virt/<NNN>/known_hosts (under the alias mpd-<NNN>); every
	//     later connection — this run's and every verb after — refuses a
	//     changed key. The fingerprint is printed so it can be compared
	//     against the vm's console while the adoption is fresh.
	if err := t.CheckReachable(ctx); err != nil {
		return err
	}
	pass("SSH key auth")
	printHostKeyFingerprint(ctx, id)

	got, err := t.Line(ctx, "hostname")
	if err != nil {
		return err
	}
	if got != id.Name() {
		return fmt.Errorf("hostname mismatch: vm at %s calls itself %q, expected %q — refusing", ip, got, id.Name())
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

	// --- Per-VM CA generated on the Mac. Root key stays here; the
	//     intermediate is name-constrained to this vm's zone.
	if err := ca.LoadOrGenerateRoot(); err != nil {
		return fmt.Errorf("root CA: %w", err)
	}
	if err := ca.LoadOrGenerateVM(id); err != nil {
		return fmt.Errorf("per-VM CA: %w", err)
	}
	pass("per-VM CA generated (" + id.Zone() + " only)")

	// --- Provision. The three bootstrap steps (sudo; OS upgrade + every
	//     package; clone + build), then push the CA in and set up the
	//     platform. All idempotent, so a re-run resumes cleanly — and a
	//     template VM or a pre-baked image that already ran 10 + 20 gets
	//     through them in seconds.
	if err := step(ctx, t, "10-passwordless-sudo", bootstrapStep("10-passwordless-sudo.sh")); err != nil {
		return err
	}
	// Root over SSH off, password auth off. Safe here: key auth was just
	// verified, and the script refuses when no authorized key exists.
	if err := step(ctx, t, "15-secure-ssh (keys only)", bootstrapStep("15-secure-ssh.sh")); err != nil {
		return err
	}
	if err := step(ctx, t, "20-install-software (OS upgrade + package set)",
		bootstrapStep("20-install-software.sh")); err != nil {
		return err
	}
	if err := step(ctx, t, "30-mpd-build (clone mpd → /opt/mpd, make install)",
		bootstrapStep("30-mpd-build.sh")); err != nil {
		return err
	}

	// The CA push needs /var/lib/mpd, which 30-mpd-build creates.
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
	// dnsmasq as part of its DNS reconcile, so an adopted vm answers for
	// forge.mpd.test from the start rather than only after a `server sync`.
	if _, err := pushLanHosts(ctx, t); err != nil {
		return fmt.Errorf("push LAN hosts: %w", err)
	}
	pass("LAN service names pushed → " + server.RemoteLanHostsPath)

	// The developer's own assets, if they keep any. Best-effort: unlike the
	// CA and the LAN names, nothing mpd-virt does depends on them.
	syncAssets(ctx, t, id.String())

	// Their MPD_* defaults, same deal — and before the `mpd --vm-setup`
	// below, which seeds that same path from mpd's own template only when
	// nothing is there. Pushing first means an adopted vm starts out
	// already agreeing with every other vm this Mac owns.
	syncEnv(ctx, t, id.String())

	// Point podman at the configured OCI pull-through cache, if any. Before
	// `mpd --vm-setup` on purpose: its base-image pre-warm is then the first
	// pull to ride the cache.
	syncOCIMirror(ctx, t, id.String())

	// mpd derives its identity from the hostname (mpd-<NNN>) and reads its
	// own IP off the interface.
	if err := step(ctx, t, "mpd --vm-setup", "/opt/mpd/bin/mpd --vm-setup"); err != nil {
		return err
	}

	// --- Record the adopted vm (host-side).
	if err := registry.Save(registry.Entry{ID: id, IP: ip, User: username, Backend: string(be)}); err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	pass("registry " + paths.VMRecord(id))
	// Populate the note now so the fresh record already reads like something,
	// not only after the first `list`. Best-effort and proxmox-only in practice.
	syncNotes(ctx, id, be)
	if err := sshconfig.Write(id, ip, username); err != nil {
		return fmt.Errorf("ssh config: %w", err)
	}
	pass("~/.ssh/config block  (ssh " + id.Name() + " → runtime, " +
		sshconfig.VMAlias(id) + " → the VM)")

	// --- WireGuard reachability via mpd-proxy. Best-effort: adoption is done,
	//     so a proxy hiccup is a warning with a re-run hint, not a failure.
	if _, err := setupReachability(ctx, t, id, ip); err != nil {
		fmt.Printf("  ⚠ WireGuard setup incomplete: %v\n    Re-run once fixed: mpd-virt start %s\n", err, id.String())
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
