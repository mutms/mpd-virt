package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/mutms/mpd-virt/go/internal/backend"
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
				fmt.Fprintln(os.Stderr, "no boxes adopted — add one with `mpd-virt adopt` or `mpd-virt create`.")
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

// sshStates resolves every entry's SSH column concurrently, aligned to entries
// by index. Serial probing stalled the whole listing by the dial timeout for
// each unreachable box; done in parallel the listing is only as slow as the
// single slowest probe.
//
// For a box whose backend can report power state (proxmox, and the laptop
// hypervisors), that state is asked first: a box the hypervisor calls off shows
// its power word and is never dialed, so a stopped proxmox VM costs one cheap
// API answer instead of the full SSH dial timeout its dead IP would otherwise
// blackhole for. Only a box reported running — or one whose backend cannot say,
// which includes every `generic` box — falls through to the SSH dial, the
// connect-first behaviour as before.
func sshStates(ctx context.Context, entries []registry.Entry) []string {
	states := make([]string, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(i int, e registry.Entry) {
			defer wg.Done()
			states[i] = entryState(ctx, e)
		}(i, e)
	}
	wg.Wait()
	return states
}

// entryState is the SSH column for one box: the hypervisor's power word when it
// reports the box off, otherwise a live SSH dial. A box the backend calls
// running is still dialed — running is not the same as reachable (it may be
// booting or firewalled), and the dial is the true liveness signal.
func entryState(ctx context.Context, e registry.Entry) string {
	switch state := powerState(ctx, e.ID, backend.Backend(e.Backend)); state {
	case "stopped", "suspended", "paused":
		return state
	}
	return sshState(ctx, e.IP)
}

// powerState is the backend power probe as a var, so tests can substitute it
// without a live hypervisor.
var powerState = backend.PowerState

func printListTable(ctx context.Context, entries []registry.Entry) {
	// Header first, then the probes: everything but the SSH column is known
	// up front, so the table's shape appears immediately while the parallel
	// probe runs, rather than after the dial timeout.
	fmt.Printf(listRow, "NNN", "NAME", "BACKEND", "IP", "USER", "SSH")
	states := sshStates(ctx, entries)
	for i, e := range entries {
		fmt.Printf(listRow, e.ID.String(), e.ID.Name(), e.Backend, e.IP, e.User, states[i])
	}
}

func printListJSON(ctx context.Context, entries []registry.Entry) error {
	states := sshStates(ctx, entries)
	type row struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Backend string `json:"backend"`
		IP      string `json:"ip"`
		User    string `json:"user"`
		SSH     string `json:"ssh"`
	}
	rows := make([]row, 0, len(entries))
	for i, e := range entries {
		rows = append(rows, row{e.ID.String(), e.ID.Name(), e.Backend, e.IP, e.User, states[i]})
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
