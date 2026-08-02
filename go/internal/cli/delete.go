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

// deleteCmd removes an adopted box: destroy its backend resource, then wipe the
// host-side bookkeeping. Guarded by typing the box name (or --yes) since it is
// destructive and easy to fire at the wrong id.
func deleteCmd() *cobra.Command {
	var assumeYes, keepVM bool
	cmd := &cobra.Command{
		Use:   "delete <NNN>",
		Short: "Delete an adopted box: destroy its backend resource + wipe host bookkeeping",
		Long: "Destroys the box's container/VM through its backend (unless\n" +
			"--keep-vm) and wipes the host-side bookkeeping: detaches it from the\n" +
			"mpd-proxy overlay, strips its ~/.ssh/config block, and removes its\n" +
			"registry entry. Leaves the CA under ~/.mpd-virt/conf/ (uninstall's\n" +
			"job), so re-adopting the same id reuses the trust material.\n\n" +
			"Requires typing the box name (mpd-<NNN>) to confirm, or --yes to\n" +
			"skip the prompt.",
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
			be := backend.Backend(e.Backend)

			fmt.Printf("delete %s  (backend=%s, ip=%s)\n", id.Name(), e.Backend, e.IP)
			if !assumeYes && !confirmName(id) {
				fmt.Println("aborted — nothing changed")
				return nil
			}

			// 1. Detach from the overlay (best-effort; mpd-proxy may be down).
			if err := proxy.New(proxy.DefaultSocket).Remove(id.Pad()); err == nil {
				pass("detached from overlay (mpd-proxy peer removed)")
			}
			// 2. Destroy the backend resource unless kept. Not fatal — the
			//    bookkeeping wipe below still runs, so a half-gone box clears.
			if keepVM {
				fmt.Println("  → --keep-vm: backend resource left in place")
			} else if err := backend.Delete(cmd.Context(), cmd.OutOrStdout(), id, be); err != nil {
				fmt.Printf("  ⚠ %v\n    continuing — host bookkeeping is wiped anyway\n", err)
			}
			// 3. Strip the ssh-config block.
			if err := sshconfig.Strip(id); err != nil {
				return fmt.Errorf("ssh config: %w", err)
			}
			pass("~/.ssh/config block removed")
			// 4. Remove the registry entry (leaves ~/.mpd-virt/conf/ for re-adoption).
			if err := registry.Remove(id); err != nil {
				return fmt.Errorf("registry: %w", err)
			}
			pass("registry entry removed (CA kept for re-adoption)")

			fmt.Printf("\n✓ %s deleted.\n", id.Name())
			return nil
		},
	}
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "skip the typed confirmation")
	cmd.Flags().BoolVar(&keepVM, "keep-vm", false, "wipe the bookkeeping but leave the backend resource")
	return cmd
}

// confirmName makes the operator type the box name to confirm a destructive
// delete. Anything else — including EOF on a non-interactive stdin — aborts.
func confirmName(id vmid.ID) bool {
	fmt.Printf("Type %s to confirm (anything else aborts): ", id.Name())
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	return strings.TrimSpace(sc.Text()) == id.Name()
}
