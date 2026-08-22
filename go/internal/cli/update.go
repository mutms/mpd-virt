package cli

import (
	"fmt"

	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/vmid"
	"github.com/spf13/cobra"
)

// updateCmd refreshes an adopted box over SSH: mpd's bootstrap step 20
// (apt dist-upgrade + the package set — the same script adoption and a
// template pre-run use, so a stale box converges), then mpd's own
// `--vm-upgrade` (git pull → rebuild → mudev + catalogues → re-run
// `mpd --vm-setup`), then verifies reachability. The update logic lives
// in mpd, so this stays a thin orchestration verb.
func updateCmd() *cobra.Command {
	var username string
	cmd := &cobra.Command{
		Use:   "update <NNN>",
		Short: "Refresh an adopted box: OS packages, then mpd (pull + rebuild + vm-setup)",
		Long: "SSHes into the box and runs mpd's bootstrap/20-install-software.sh\n" +
			"(apt dist-upgrade + every package mpd needs) followed by\n" +
			"`mpd --vm-upgrade` (pull, rebuild, re-run `mpd --vm-setup`) — then\n" +
			"verifies reachability.",
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

			// Refresh the LAN names before the update runs: --vm-upgrade
			// re-runs `mpd --vm-setup`, which republishes whatever file is
			// on the box by then — so the push costs nothing extra here.
			if _, err := pushLanHosts(cmd.Context(), t); err != nil {
				fmt.Printf("  ⚠ LAN hosts push failed: %v\n    run `mpd-virt server sync %s` afterwards\n", err, id.String())
			}
			syncAssets(cmd.Context(), t, id.String())
			syncMpdEnv(cmd.Context(), t, id.String())

			fmt.Printf("update %s at %s\n", id.Name(), e.IP)
			if err := step(cmd.Context(), t, "20-install-software (OS upgrade + package set)",
				bootstrapStep("20-install-software.sh")); err != nil {
				return fmt.Errorf("%w — ssh in and re-run `%s` to see the full output", err, bootstrapStep("20-install-software.sh"))
			}
			if err := step(cmd.Context(), t, "mpd --vm-upgrade", "/opt/mpd/bin/mpd --vm-upgrade"); err != nil {
				return fmt.Errorf("%w — ssh in and re-run `mpd --vm-upgrade` to see the full output", err)
			}
			pass("update complete")

			// --vm-upgrade re-runs mpd --vm-setup, which restarts wg0 and drops
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
