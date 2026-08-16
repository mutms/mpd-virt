package cli

import (
	"fmt"

	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/vmid"
	"github.com/spf13/cobra"
)

// updateCmd refreshes an adopted box's mpd to current main by running mpd's own
// bootstrap/99-update.sh over SSH (git pull → rebuild → re-run mpd --vm-setup →
// migrations), then verifies reachability. The update logic lives in mpd, so
// this stays a thin orchestration verb — mpd never needs a mpd-virt release for
// a changed update flow.
func updateCmd() *cobra.Command {
	var username string
	cmd := &cobra.Command{
		Use:   "update <NNN>",
		Short: "Refresh an adopted box's mpd to current main (pull + rebuild + vm-setup)",
		Long: "SSHes into the box and runs mpd's bootstrap/99-update.sh — pulls the\n" +
			"latest mpd source, rebuilds, re-runs `mpd --vm-setup`, applies any\n" +
			"migrations — then verifies reachability. A release that reshuffles the\n" +
			"update script itself may need two runs (see the script's\n" +
			"self-modification note).",
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
			t := boxTarget(id, user, e.IP)
			// One classified check up front: a refused host key stops the
			// update with the remedy, before anything is pushed or run.
			if err := t.CheckReachable(cmd.Context()); err != nil {
				return err
			}

			// Refresh the LAN names before the update runs: 99-update.sh
			// re-runs `mpd --vm-setup`, which republishes whatever file is
			// on the box by then — so the push costs nothing extra here.
			if _, err := pushLanHosts(cmd.Context(), t); err != nil {
				fmt.Printf("  ⚠ LAN hosts push failed: %v\n    run `mpd-virt server sync %s` afterwards\n", err, id.String())
			}
			syncAssets(cmd.Context(), t, id.String())
			syncMpdEnv(cmd.Context(), t, id.String())

			fmt.Printf("▶ update %s at %s — running /opt/mpd/bootstrap/99-update.sh\n", id.Name(), e.IP)
			code, err := t.Stream(cmd.Context(), "bash /opt/mpd/bootstrap/99-update.sh")
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("update failed (exit %d) — ssh in and re-run `bash /opt/mpd/bootstrap/99-update.sh` to see the full output", code)
			}
			pass("update complete")

			// 99-update.sh re-runs mpd --vm-setup, which restarts wg0 and drops
			// the mpd-proxy peer — re-wire reachability (as start does), then
			// verify. Best-effort: a proxy hiccup is a warning, not a failure.
			wired, err := setupReachability(cmd.Context(), t, id, e.IP)
			if err != nil {
				fmt.Printf("  ⚠ reachability re-wire failed: %v\n    run `mpd-virt start %s`\n", err, id.String())
			}
			if wired {
				verifyReachable(cmd.Context(), id)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "dev user on the box (defaults to the registry entry)")
	return cmd
}
