package cli

import (
	"fmt"
	"os"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/sshconfig"
	"github.com/spf13/cobra"
)

// uninstallCmd removes mpd-virt from this Mac. It stops every adopted box
// (without deleting it — boxes stay re-takeover-able) and wipes mpd-virt's host
// state (~/.mpd-virt and the ssh-config blocks). It does not touch VM data, so
// it is fully recoverable. mpd-proxy and the CA keychain trust are separate; it
// prints those follow-ups rather than doing them.
func uninstallCmd() *cobra.Command {
	var assumeYes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove mpd-virt from this Mac: stop boxes (keep them) + wipe host state",
		Long: "Stops every adopted box through its backend (container/parallels;\n" +
			"generic/proxmox are left running) WITHOUT deleting any — they stay\n" +
			"re-takeover-able. Then wipes mpd-virt's host state: ~/.mpd-virt/\n" +
			"(registry + the root CA) and every ~/.ssh/config managed block. No VM\n" +
			"data is touched, so this is fully recoverable.\n\n" +
			"mpd-proxy and the CA's keychain trust are separate — it prints those\n" +
			"follow-ups. Requires typing 'uninstall' to confirm, or --yes.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := registry.List()
			if err != nil {
				return err
			}
			fmt.Printf("uninstall mpd-virt — stops %d box(es) (kept, not deleted), then wipes ~/.mpd-virt + ssh-config blocks.\n", len(entries))
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

			// 2. Wipe ~/.mpd-virt (registry + root CA).
			if err := os.RemoveAll(paths.Root()); err != nil {
				return fmt.Errorf("remove %s: %w", paths.Root(), err)
			}
			pass("removed " + paths.Root() + " (registry + root CA)")

			// 3. The two things this tool can't do for you.
			fmt.Print("\nmpd-virt host state removed. Finish up:\n" +
				"  • mpd-proxy:  sudo mpd-proxy uninstall\n" +
				"  • keychain (macOS), if you trusted the root CA there:\n" +
				"      sudo security delete-certificate -c \"mpd Root CA\" /Library/Keychains/System.keychain\n" +
				"      (login keychain instead:  security delete-certificate -c \"mpd Root CA\")\n" +
				"  • remove the binary:  rm ~/.local/bin/mpd-virt   (or `make uninstall`)\n\n" +
				"The boxes are stopped, not deleted — re-adopt any later with `mpd-virt takeover <NNN> --backend <b>`.\n")
			return nil
		},
	}
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "skip the typed confirmation")
	return cmd
}
