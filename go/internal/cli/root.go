// Package cli assembles the mpd-virt cobra command tree.
package cli

import (
	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/spf13/cobra"
)

// Root builds the top-level command. version is stamped by main via
// -ldflags.
func Root(version string) *cobra.Command {
	root := &cobra.Command{
		Use:     "mpd-virt",
		Short:   "Provision and adopt mpd environments on macOS",
		Version: version,
		// mpd-virt prints its own errors from main; cobra should not
		// re-print them or dump usage on a runtime failure.
		SilenceUsage:  true,
		SilenceErrors: true,
		// Every invocation re-asserts owner-only permissions on the whole
		// state tree — CA keys, the proxmox token, pinned host keys: none
		// of ~/.mpd-virt is anyone else's to read, whatever mode a file
		// arrived with.
		PersistentPreRun: func(*cobra.Command, []string) { paths.EnsurePrivate() },
	}
	root.AddCommand(takeoverCmd())
	root.AddCommand(createCmd())
	root.AddCommand(startCmd())
	root.AddCommand(stopCmd())
	root.AddCommand(deleteCmd())
	root.AddCommand(listCmd())
	root.AddCommand(updateCmd())
	root.AddCommand(serverCmd())
	root.AddCommand(caCmd())
	root.AddCommand(uninstallCmd())
	return root
}
