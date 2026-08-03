package cli

import (
	"fmt"
	"os"

	"github.com/mutms/mpd-virt/go/internal/ca"
	"github.com/spf13/cobra"
)

// caCmd inspects and exports the mpd root CA. Its one job today is `export`
// — writing the root's public certificate somewhere it can be installed in
// a LAN host's trust store, so that host trusts the leaves `server cert`
// issues.
func caCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ca",
		Short: "Inspect and export the mpd root CA",
		// Default subcommand: `mpd-virt ca` exports.
		RunE: func(cmd *cobra.Command, args []string) error { return caExport("") },
	}
	cmd.AddCommand(caExportCmd())
	return cmd
}

func caExportCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Print the root CA's public certificate (for a LAN host's trust store)",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return caExport(path) },
	}
	cmd.Flags().StringVar(&path, "path", "", "write here instead of stdout")
	return cmd
}

// caExport writes the root CA's public cert to path, or stdout so it pipes.
// It generates the root on first use, so `ca export` on a fresh Mac still
// yields a certificate.
func caExport(path string) error {
	if err := ca.LoadOrGenerateRoot(); err != nil {
		return err
	}
	pem, err := os.ReadFile(ca.RootCertPath())
	if err != nil {
		return err
	}
	if path == "" {
		fmt.Print(string(pem))
		return nil
	}
	if err := os.WriteFile(path, pem, 0o644); err != nil {
		return err
	}
	pass("wrote " + path)
	return nil
}
