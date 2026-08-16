package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/ca"
	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/sshconfig"
	"github.com/spf13/cobra"
)

// uninstallCmd removes mpd-virt from this Mac. It stops every adopted box
// (without deleting it — boxes stay re-adoption-able) and wipes mpd-virt's host
// state (the registry under ~/.mpd-virt and the ssh-config blocks). It does not
// touch VM data, so it is fully recoverable.
//
// Two things are deliberately KEPT. The root CA, so a later adoption reuses
// the same trust anchor instead of minting a fresh-fingerprint CA the dev
// would have to re-trust everywhere. And mpd-virt.env, because it is the
// dev's own writing rather than state mpd-virt generated — losing it to a
// verb that promises to be "fully recoverable" would be a lie, and it is not
// reproducible from anything left on the machine.
//
// mpd-proxy and the CA's OS-trust-store entry are separate concerns;
// uninstall reports their status and how to remove them rather than doing it.
func uninstallCmd() *cobra.Command {
	var assumeYes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove mpd-virt from this Mac: stop boxes (keep them) + wipe host state (keep the root CA + your mpd-virt.env)",
		Long: "Stops every adopted box through its backend (container/parallels/utm;\n" +
			"generic/proxmox are left running) WITHOUT deleting any — they stay\n" +
			"re-adoption-able. Then wipes mpd-virt's host state under ~/.mpd-virt/\n" +
			"and every ~/.ssh/config managed block. No VM data is touched, so this\n" +
			"is fully recoverable.\n\n" +
			"The root CA is KEPT (~/.mpd-virt/conf/caroot) so a later adoption\n" +
			"reuses it with no re-trust, and so is your own mpd-virt.env — this\n" +
			"tool never wrote it and cannot reproduce it. mpd-proxy and the CA's\n" +
			"keychain trust are separate — it reports those follow-ups. Requires\n" +
			"typing 'uninstall' to confirm, or --yes.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := registry.List()
			if err != nil {
				return err
			}
			fmt.Printf("uninstall mpd-virt — stops %d box(es) (kept, not deleted), then wipes ~/.mpd-virt (keeping the root CA and mpd-virt.env) + ssh-config blocks.\n", len(entries))
			if !assumeYes && !confirmWord("uninstall") {
				fmt.Println("aborted — nothing changed")
				return nil
			}

			// 1. Stop every box (keep them), and strip its ssh-config block.
			//    Best-effort per box — one failure must not strand the rest.
			for _, e := range entries {
				if err := backend.Stop(cmd.Context(), cmd.OutOrStdout(), e.ID, backend.Backend(e.Backend)); err != nil {
					fmt.Printf("  ⚠ stop %s: %v\n", e.ID.Name(), err)
				}
				_ = sshconfig.Strip(e.ID)
			}
			pass(fmt.Sprintf("stopped %d box(es); ssh-config blocks stripped", len(entries)))

			// 2. Wipe ~/.mpd-virt EXCEPT the root CA (conf/caroot) and
			//    the dev's own mpd-virt.env.
			if err := wipeHostStateKeepingOwn(); err != nil {
				return err
			}
			pass("removed " + paths.Root() + " (kept the root CA and mpd-virt.env)")

			// 3. Report what was kept and the follow-ups this tool won't do for you.
			fmt.Print("\nmpd-virt host state removed. Finish up:\n")
			if _, err := os.Stat(ca.RootCertPath()); err == nil {
				fmt.Printf("  • Root CA kept at %s — a later `adopt` reuses this trust\n"+
					"    anchor (no re-trust). Delete it by hand only if you want a fresh CA.\n",
					paths.CARoot())
			} else {
				fmt.Printf("  • No root CA on disk (none was ever generated).\n")
			}
			reportCATrustStore(cmd.Context())
			fmt.Print("  • mpd-proxy:  sudo mpd-proxy uninstall\n" +
				"  • the binary:  rm ~/.local/bin/mpd-virt   (or `make uninstall`)\n\n" +
				"The boxes are stopped, not deleted — re-adopt any later with `mpd-virt adopt <NNN> --backend <b>`.\n")
			return nil
		},
	}
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "skip the typed confirmation")
	return cmd
}

// wipeHostStateKeepingOwn removes everything under ~/.mpd-virt except the
// root CA directory (conf/caroot) and the dev's own mpd-virt.env: all per-box
// registry dirs and the rest of conf (servers, lan-hosts, service,
// backend.env). Keeping caroot is what lets a re-adopt reuse the same trust
// anchor; keeping mpd-virt.env is because mpd-virt never wrote it and cannot
// reproduce it. A missing ~/.mpd-virt is a no-op.
//
// assets/ is NOT kept, and that is the older decision this one sits beside:
// a mirror of it exists on every box that was ever started, so it survives
// elsewhere. mpd-virt.env has no such copy worth trusting — the in-VM ones
// are overwritten by whatever this file said last.
func wipeHostStateKeepingOwn() error {
	root := paths.Root()
	top, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", root, err)
	}
	confName := filepath.Base(paths.Conf())  // "conf"
	caName := filepath.Base(paths.CARoot())  // "caroot"
	envName := filepath.Base(paths.MpdEnv()) // "mpd-virt.env"
	for _, e := range top {
		if e.Name() == envName {
			continue
		}
		if e.Name() == confName {
			// Inside conf, drop everything but caroot.
			confEntries, err := os.ReadDir(paths.Conf())
			if err != nil {
				return fmt.Errorf("read %s: %w", paths.Conf(), err)
			}
			for _, ce := range confEntries {
				if ce.Name() == caName {
					continue
				}
				p := filepath.Join(paths.Conf(), ce.Name())
				if err := os.RemoveAll(p); err != nil {
					return fmt.Errorf("remove %s: %w", p, err)
				}
			}
			continue
		}
		p := filepath.Join(root, e.Name())
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return nil
}
