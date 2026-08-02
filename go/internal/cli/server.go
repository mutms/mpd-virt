package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/ca"
	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/server"
	"github.com/mutms/mpd-virt/go/internal/vmid"
	"github.com/spf13/cobra"
)

// expiryWarningDays is the threshold below which `server cert` re-issues
// without --force — the same convention the CA uses elsewhere.
const expiryWarningDays = 30

// serverCmd is the `server` verb group: LAN machines that are not mpd VMs
// but get a name under mpd.test. There is deliberately no `deploy` verb and
// no notion of what software a server runs — where a certificate goes and
// what to restart afterwards is each machine's own runbook, not a second
// staler copy here. Ported from ServerAdmin.swift.
func serverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Manage LAN machines with names under mpd.test — name, address, certificate",
		Long: "A \"server\" is a real host on the LAN that gets a name in the\n" +
			"mpd.test tree (forge.mpd.test, warp.mpd.test). mpd-virt does not\n" +
			"manage these machines; it remembers their addresses, issues their\n" +
			"TLS certificates (signed by the root CA on this Mac), and publishes\n" +
			"their names into every VM so containers reach them by name over\n" +
			"verified TLS. Run with no subcommand to list.",
		// Default subcommand: `mpd-virt server` lists, matching the Swift.
		RunE: func(cmd *cobra.Command, args []string) error {
			return serverList(false)
		},
	}
	cmd.AddCommand(serverAddCmd(), serverListCmd(), serverDeleteCmd(), serverCertCmd(), serverSyncCmd())
	return cmd
}

func serverAddCmd() *cobra.Command {
	var ip string
	cmd := &cobra.Command{
		Use:   "add <name> --ip <addr>",
		Short: "Register a LAN server (name is a single label under mpd.test)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := server.Normalise(args[0])
			if err != nil {
				return err
			}
			if err := server.ValidateIP(ip); err != nil {
				return err
			}
			if server.Exists(name) {
				return fmt.Errorf("server %q already exists — remove it first, or edit %s", name, server.EnvFile(name))
			}
			e := server.Entry{Name: name, IP: ip}
			if err := server.Save(e); err != nil {
				return err
			}
			if _, err := server.WriteHostsFile(); err != nil {
				return err
			}
			fmt.Printf("server %s\n", e.Host())
			pass("registered " + e.Host() + " → " + e.IP)
			fmt.Printf("  registry: %s\n\n", server.EnvFile(name))
			fmt.Println("Next:")
			fmt.Printf("  mpd-virt server cert %s   # issue its TLS certificate, if it serves any\n", name)
			fmt.Println("  mpd-virt server sync      # publish the name inside every VM")
			fmt.Println()
			return etcHostsReminder()
		},
	}
	cmd.Flags().StringVar(&ip, "ip", "", "IPv4 or IPv6 address on the LAN (required)")
	_ = cmd.MarkFlagRequired("ip")
	return cmd
}

func serverListCmd() *cobra.Command {
	var etcHosts bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Print every LAN server as a hosts(5) line, then what /etc/hosts here still lacks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return serverList(etcHosts)
		},
	}
	cmd.Flags().BoolVar(&etcHosts, "etc-hosts", false,
		"emit only the hosts(5) lines, no trailing report, so the output pipes")
	return cmd
}

// serverList prints the registry in hosts(5) form — always pasteable, never
// a decorated table, because this output is copied into /etc/hosts.
func serverList(etcHosts bool) error {
	entries, err := server.LoadAll()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		if !etcHosts {
			fmt.Println("no LAN servers registered — add one with `mpd-virt server add <name> --ip <addr>`")
		}
		return nil
	}
	for _, e := range entries {
		fmt.Printf("%s\t%s\n", e.IP, e.Host())
	}
	// --etc-hosts suppresses everything else so the output pipes:
	//   mpd-virt server list --etc-hosts | sudo tee -a /etc/hosts
	if etcHosts {
		return nil
	}
	return etcHostsReminder()
}

func serverDeleteCmd() *cobra.Command {
	var assumeYes bool
	cmd := &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"rm"},
		Short:   "Forget a LAN server and delete its certificate + key",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := server.Normalise(args[0])
			if err != nil {
				return err
			}
			e, err := server.Load(name)
			if err != nil {
				return err
			}
			fmt.Printf("remove %s\n", e.Host())
			fmt.Printf("  this deletes %s/, including its private key\n", server.Dir(name))
			if !assumeYes && !confirmYesNo("Remove "+e.Host()+"?") {
				fmt.Println("aborted — nothing changed")
				return nil
			}
			if err := server.Remove(name); err != nil {
				return err
			}
			if _, err := server.WriteHostsFile(); err != nil {
				return err
			}
			pass("removed " + e.Host())
			fmt.Printf("  ⚠ the certificate stays installed on %s until you remove it there\n", e.IP)
			fmt.Println("  VMs keep answering for this name until `mpd-virt server sync`")
			return nil
		},
	}
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func serverCertCmd() *cobra.Command {
	var extraSans []string
	var force bool
	cmd := &cobra.Command{
		Use:   "cert <name>",
		Short: "Issue (or renew) this server's TLS certificate, signed by the mpd root CA",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := server.Normalise(args[0])
			if err != nil {
				return err
			}
			e, err := server.Load(name)
			if err != nil {
				return err
			}
			// Every extra SAN goes through the same rules as the primary name.
			sans := []string{e.Host()}
			for _, raw := range extraSans {
				label, err := server.Normalise(raw)
				if err != nil {
					return err
				}
				h := server.ServiceHost(label)
				if !contains(sans, h) {
					sans = append(sans, h)
				}
			}

			certPath := server.CertPath(name)
			if _, statErr := os.Stat(certPath); statErr == nil && !force {
				if days, err := ca.DaysUntilExpiry(certPath); err == nil && days > expiryWarningDays {
					fmt.Printf("certificate for %s\n", e.Host())
					fmt.Printf("  current certificate is valid for another %d day(s) — nothing to do\n", days)
					fmt.Println("  re-issue anyway with --force")
					return nil
				}
			}

			fmt.Printf("certificate for %s\n", e.Host())
			if err := ca.IssueLeaf(sans, certPath, server.KeyPath(name)); err != nil {
				return err
			}
			// Record the SAN list so a re-issue can reproduce it (DNS:… form,
			// matching the Swift artifact).
			var sig []string
			for _, s := range sans {
				sig = append(sig, "DNS:"+s)
			}
			if err := os.WriteFile(server.SansPath(name), []byte(strings.Join(sig, "\n")+"\n"), 0o644); err != nil {
				return err
			}
			days, _ := ca.DaysUntilExpiry(certPath)
			pass(fmt.Sprintf("issued for %s  (%d days)", strings.Join(sig, ", "), days))
			fmt.Printf("  cert: %s\n", certPath)
			fmt.Printf("  key:  %s\n", server.KeyPath(name))
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&extraSans, "san", nil, "extra DNS name under mpd.test to cover (repeatable)")
	cmd.Flags().BoolVar(&force, "force", false, "re-issue even when the current certificate has plenty of life left")
	return cmd
}

func serverSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync [NNN]",
		Short: "Publish every LAN name into VMs' resolvers (all VMs, or one by id)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var only *vmid.ID
			if len(args) == 1 {
				id, err := vmid.Parse(args[0])
				if err != nil {
					return err
				}
				only = &id
			}
			return serverSync(cmd.Context(), only)
		},
	}
	return cmd
}

// serverSync pushes the rendered hosts file into VMs and has each republish
// it via `mpd --vm-setup`. A VM that is down is reported, not fatal: the
// records are static LAN facts and the next setup/update on that VM picks
// them up.
func serverSync(ctx context.Context, only *vmid.ID) error {
	path, err := server.WriteHostsFile()
	if err != nil {
		return err
	}
	entries, err := server.LoadAll()
	if err != nil {
		return err
	}
	fmt.Printf("publishing %d LAN record(s) into VMs\n", len(entries))
	for _, e := range entries {
		fmt.Printf("  %s → %s\n", e.Host(), e.IP)
	}

	boxes, err := registry.List()
	if err != nil {
		return err
	}
	if only != nil {
		e, err := registry.Load(*only)
		if err != nil {
			return err
		}
		boxes = []registry.Entry{e}
	}
	if len(boxes) == 0 {
		fmt.Println("  no VMs registered — nothing to publish to")
		return nil
	}

	for _, b := range boxes {
		fmt.Printf("\n▶ %s\n", b.ID.Name())
		t := host.Target{User: b.User, Host: b.IP}
		if !t.Reachable(ctx) {
			fmt.Printf("  ⚠ not reachable at %s — skipped (next `mpd-virt update` will push it)\n", b.IP)
			continue
		}
		if err := t.Install(ctx, path, server.RemoteLanHostsPath, "0644"); err != nil {
			fmt.Printf("  ⚠ push failed: %v\n", err)
			continue
		}
		if code, err := t.Stream(ctx, "/opt/mpd/bin/mpd --vm-setup >/dev/null"); err != nil {
			fmt.Printf("  ⚠ vm-setup failed: %v\n", err)
			continue
		} else if code != 0 {
			fmt.Printf("  ⚠ vm-setup exited %d\n", code)
			continue
		}
		pass("published (" + server.RemoteLanHostsPath + ")")
	}
	return nil
}

// etcHostsReminder reports which LAN records this Mac's /etc/hosts is
// missing. mpd-virt does not write /etc/hosts: it needs sudo, other tools
// edit it too, and an ownership marker would need its own uninstall path.
// /etc/resolver cannot help either — those files are per-VM-zone and a LAN
// name matches none of them. /etc/hosts is consulted first, which is why
// hand-editing is the right answer.
func etcHostsReminder() error {
	entries, err := server.LoadAll()
	if err != nil {
		return err
	}
	live, _ := os.ReadFile("/etc/hosts")
	var missing []server.Entry
	for _, e := range entries {
		if !hostsHas(string(live), e) {
			missing = append(missing, e)
		}
	}
	if len(missing) == 0 {
		pass("/etc/hosts on this Mac resolves all of these")
		return nil
	}
	fmt.Printf("  ⚠ /etc/hosts on this Mac is missing %d of these:\n", len(missing))
	for _, e := range missing {
		fmt.Printf("      %s\t%s\n", e.IP, e.Host())
	}
	fmt.Println("    then: sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder")
	return nil
}

// hostsHas reports whether an uncommented /etc/hosts line maps e.IP to
// e.Host().
func hostsHas(live string, e server.Entry) bool {
	for _, line := range strings.Split(live, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 || fields[0] != e.IP {
			continue
		}
		if contains(fields[1:], e.Host()) {
			return true
		}
	}
	return false
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// confirmYesNo asks a y/N question. Anything but y/yes — including EOF on a
// non-interactive stdin — is no.
func confirmYesNo(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(sc.Text())) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
