package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/spf13/cobra"
)

// listCmd prints one row per adopted box from the registry, with a live SSH
// reachability probe. The probe is best-effort — a down box shows "down", it
// does not fail the listing.
func listCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List adopted boxes (with a live SSH reachability probe)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := registry.List()
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Fprintln(os.Stderr, "no boxes adopted — add one with `mpd-virt takeover` or `mpd-virt create`.")
				return nil
			}
			if jsonOut {
				return printListJSON(cmd.Context(), entries)
			}
			printListTable(cmd.Context(), entries)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output JSON instead of a table")
	return cmd
}

const listRow = "%-4s %-10s %-10s %-16s %-9s %s\n"

func printListTable(ctx context.Context, entries []registry.Entry) {
	fmt.Printf(listRow, "NNN", "NAME", "BACKEND", "IP", "USER", "SSH")
	for _, e := range entries {
		fmt.Printf(listRow, e.ID.Pad(), e.ID.Name(), e.Backend, e.IP, e.User, sshState(ctx, e.IP))
	}
}

func printListJSON(ctx context.Context, entries []registry.Entry) error {
	type row struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Backend string `json:"backend"`
		IP      string `json:"ip"`
		User    string `json:"user"`
		SSH     string `json:"ssh"`
	}
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, row{e.ID.Pad(), e.ID.Name(), e.Backend, e.IP, e.User, sshState(ctx, e.IP)})
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// sshState reports whether the box answers on ssh, as a quick liveness signal.
// A short timeout keeps a down box from stalling the whole listing.
func sshState(ctx context.Context, ip string) string {
	if ip == "" {
		return "?"
	}
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, "22"))
	if err != nil {
		return "down"
	}
	_ = c.Close()
	return "up"
}
