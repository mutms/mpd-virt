package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/proxy"
	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/sshconfig"
	"github.com/mutms/mpd-virt/go/internal/vmid"
	"github.com/spf13/cobra"
)

// removeCmd un-adopts a box: it wipes the host-side bookkeeping — overlay
// peer, ssh-config block, registry entry, pinned host key, per-VM CA — and
// powers the box off, but never deletes it. Powering off is a convenience:
// a running Apple container refuses `container rm`, so leaving it up just
// forces a `stop` before the delete. Destroying the box stays the
// hypervisor's call (UTM, `container delete`, the Proxmox UI) — a stopped
// box is still re-adoptable, and that split is what makes rebuilds clean:
// re-image the box however you like, `remove`, then `adopt` again — which
// records the new host key as a deliberate first contact and reprovisions
// the certificates under a fresh per-VM CA.
//
// Still guarded by typing the box name (or --yes): removing the pin and the
// per-VM CA is a trust decision — the next adoption will believe whatever
// answers at the address — so it should never happen by a slip of the id.
func removeCmd() *cobra.Command {
	var assumeYes bool
	cmd := &cobra.Command{
		Use:     "remove <NNN>",
		Aliases: []string{"delete", "rm"},
		Short:   "Un-adopt a box: wipe host-side state (pin, CA, ssh-config, overlay) and power it off",
		Long: "Wipes everything mpd-virt holds for the box on this Mac: detaches it\n" +
			"from the mpd-proxy overlay, strips its ~/.ssh/config block, and\n" +
			"removes ~/.mpd-virt/<NNN>/ — the registry entry, the pinned ssh host\n" +
			"key, and the per-VM CA. It also powers the box off (a no-op for\n" +
			"generic) so its hypervisor object can be deleted right after — a\n" +
			"running Apple container refuses `container rm`. The box is only\n" +
			"stopped, never deleted: destroying it belongs to the hypervisor\n" +
			"(UTM, `container delete`, the Proxmox UI), and a stopped box stays\n" +
			"re-adoptable. The root CA under ~/.mpd-virt/conf/ survives too\n" +
			"(uninstall's job).\n\n" +
			"This is how a rebuilt box comes back: re-image it, `remove`, then\n" +
			"`adopt` — the new host key is recorded as a deliberate first\n" +
			"contact and the certificates are reprovisioned under a fresh per-VM\n" +
			"CA, chained to the same trusted root.\n\n" +
			"Requires typing the box name (mpd-<NNN>) to confirm, or --yes to\n" +
			"skip the prompt.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := vmid.Parse(args[0])
			if err != nil {
				return err
			}
			// The registry entry is informational here: print what is known,
			// but clean up regardless, so a half-removed box clears on re-run.
			e, loadErr := registry.Load(id)
			if loadErr == nil {
				fmt.Printf("remove %s  (backend=%s, ip=%s) — powers the box off, but never deletes it\n",
					id.Name(), e.Backend, e.IP)
			} else {
				fmt.Printf("remove %s — no registry entry; clearing whatever host-side state remains\n", id.Name())
			}
			if !assumeYes && !confirmWord(id.Name()) {
				fmt.Println("aborted — nothing changed")
				return nil
			}

			// 1. Detach from the overlay (best-effort; mpd-proxy may be down).
			if err := proxy.New(proxy.DefaultSocket()).Remove(id.String()); err == nil {
				pass("detached from overlay (mpd-proxy peer removed)")
			}
			// 2. Power the box off, so its hypervisor object can be deleted
			//    right after — a running Apple container refuses `container
			//    rm`. Best-effort and backend-aware (a no-op for generic,
			//    power off for the local hypervisors, graceful shutdown for
			//    proxmox); it stays *stopped*, never deleted, so a re-adopt
			//    can still bring it back. Needs the backend from the registry,
			//    so it is skipped when there is no entry left to read it from.
			if loadErr == nil {
				if err := backend.Stop(cmd.Context(), cmd.OutOrStdout(), id, backend.Backend(e.Backend)); err != nil {
					fmt.Printf("  ⚠ could not power %s off: %v (continuing)\n", id.Name(), err)
				}
			}
			// 3. Strip the ssh-config block.
			if err := sshconfig.Strip(id); err != nil {
				return fmt.Errorf("ssh config: %w", err)
			}
			pass("~/.ssh/config block removed")
			// 4. Remove ~/.mpd-virt/<NNN>/ — registry entry, pinned host key,
			//    per-VM CA. The root CA under conf/ survives for re-adoption.
			if err := registry.Remove(id); err != nil {
				return fmt.Errorf("registry: %w", err)
			}
			pass("~/.mpd-virt/" + id.String() + "/ removed (registry, pinned host key, per-VM CA; the root CA survives)")

			fmt.Printf("\n✓ %s removed.", id.Name())
			if loadErr == nil {
				fmt.Printf("  Re-adopt with: mpd-virt adopt %s --backend=%s\n", id.String(), e.Backend)
			} else {
				fmt.Println()
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "skip the typed confirmation")
	return cmd
}

// confirmWord makes the operator type an exact word (the box name for
// remove, "uninstall" for uninstall) to confirm a trust-relevant action.
// Anything else — including EOF on a non-interactive stdin — aborts.
func confirmWord(want string) bool {
	fmt.Printf("Type %s to confirm (anything else aborts): ", want)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	return strings.TrimSpace(sc.Text()) == want
}
