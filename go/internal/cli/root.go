// Package cli assembles the mpd-virt cobra command tree.
package cli

import "github.com/spf13/cobra"

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
	}
	root.AddCommand(takeoverCmd())
	root.AddCommand(createCmd())
	root.AddCommand(startCmd())
	root.AddCommand(stopCmd())
	root.AddCommand(deleteCmd())
	root.AddCommand(listCmd())
	return root
}
