package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/proxy"
	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/vmid"
	"github.com/spf13/cobra"
)

// listCmd prints one row per adopted VM from the registry, with a live SSH
// reachability probe. The probe is best-effort — a down VM shows "down", it
// does not fail the listing.
func listCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List adopted VMs (with a live SSH reachability probe)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := registry.List()
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Fprintln(os.Stderr, "no VMs adopted — add one with `mpd-virt adopt` or `mpd-virt create`.")
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

// NOTES sits second, right after NNN: the customer name lives there, and the
// NNN beside it is what you type into `start`/`stop` — so the two columns you
// read together are adjacent, not at opposite ends of the row.
const listRow = "%-4s %s %-10s %-10s %-16s %-9s %s\n"

// notesWidth caps the NOTES column — enough of a VM's Notes to tell it apart at
// a glance (the whole point of the column) without letting one long note push
// the table wide.
const notesWidth = 20

// padNotes right-pads a NOTES cell to notesWidth *display columns*, counting
// runes rather than bytes: shortNotes truncates to notesWidth runes, but a
// multibyte rune (the "…" ellipsis, an accented customer name) is several
// bytes, so Go's byte-counting "%-20s" would under-pad exactly the rows that
// hold one and jag the column. Since NOTES is no longer last, that padding has
// to be right.
func padNotes(s string) string {
	if gap := notesWidth - len([]rune(s)); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// probe is the per-VM data a live listing gathers: the SSH column and, for a
// backend that carries a per-VM note (proxmox), the tidied first line of it.
// Both are best-effort — a failure leaves the field its zero value.
type probe struct {
	ssh   string
	notes string
}

// probeRows resolves every entry's live columns concurrently, aligned to
// entries by index. Serial probing stalled the whole listing by the dial
// timeout for each unreachable VM; done in parallel the listing is only as slow
// as the single slowest VM.
//
// For a VM whose backend can report power state (proxmox, and the laptop
// hypervisors), that state is asked first: a VM the hypervisor calls off shows
// its power word and is never dialed, so a stopped proxmox VM costs one cheap
// API answer instead of the full SSH dial timeout its dead IP would otherwise
// blackhole for. Only a VM reported running — or one whose backend cannot say,
// which includes every `generic` VM — falls through to the SSH dial, the
// connect-first behaviour as before. The Notes lookup is asked in the same
// goroutine (only proxmox VMs actually reach the API; every other backend
// answers "" without a round trip), so it adds no goroutines and, for a VM
// listed at all, at most one extra API call to the listing's wall-clock.
func probeRows(ctx context.Context, entries []registry.Entry) []probe {
	rows := make([]probe, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(i int, e registry.Entry) {
			defer wg.Done()
			rows[i].ssh = entryState(ctx, e)
			rows[i].notes = shortNotes(cachedNotes(e.ID, vmNotes(ctx, e.ID, backend.Backend(e.Backend))))
		}(i, e)
	}
	wg.Wait()
	return rows
}

// vmNotes is the backend notes lookup as a var, so tests can substitute it
// without a live hypervisor — the same trick as powerState.
var vmNotes = backend.Notes

// cachedNotes is a write-through cache over the backend's live notes: a
// non-empty live value is returned and persisted to ~/.mpd-virt/<NNN>/notes,
// and an empty one (the Proxmox host unreachable, the API down, the backend
// carrying no notes at all) falls back to the last value cached there. That is
// what keeps a listing legible when the box is off or off-network — the case
// where knowing which VM is which matters most. Best-effort throughout: a cache
// that cannot be written or read simply leaves the live value to stand.
//
// The trade-off is that notes deliberately *cleared* on a reachable VM keep
// showing their last value until something else is set — an acceptable price
// for surviving an unreachable one, and the raw text is cached (not the trimmed
// cell) so a later width change re-trims it correctly.
func cachedNotes(id vmid.ID, live string) string {
	if live != "" {
		if err := os.WriteFile(paths.VMNotes(id), []byte(live), 0o600); err != nil {
			// A missing VM dir is the only expected failure; create it and retry
			// once, else give up and just use the live value.
			if os.MkdirAll(paths.VMDir(id), 0o700) == nil {
				_ = os.WriteFile(paths.VMNotes(id), []byte(live), 0o600)
			}
		}
		return live
	}
	b, err := os.ReadFile(paths.VMNotes(id))
	if err != nil {
		return ""
	}
	return string(b)
}

// shortNotes renders raw VM notes as one tidy table cell: the first non-blank
// line, with a leading markdown heading/quote marker dropped, every control
// character (ANSI escapes included — the notes are attacker-adjacent free text
// printed straight to a terminal) and inner whitespace run folded to a single
// space, and the result truncated to notesWidth with an ellipsis. Empty in,
// empty out — the common case of a VM with no notes, and of every backend that
// carries none.
func shortNotes(raw string) string {
	line := ""
	for _, l := range strings.Split(raw, "\n") {
		if strings.TrimSpace(l) != "" {
			line = l
			break
		}
	}
	line = strings.TrimLeft(line, "#> \t")

	var b strings.Builder
	pendingSpace := false
	for _, r := range line {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			pendingSpace = b.Len() > 0 // collapse runs; never lead with a space
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
	}

	if runes := []rune(b.String()); len(runes) > notesWidth {
		return strings.TrimRight(string(runes[:notesWidth-1]), " ") + "…"
	}
	return b.String()
}

// entryState is the SSH column for one VM: the hypervisor's power word when it
// reports the VM off, otherwise a live SSH dial. A VM the backend calls
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

// ANSI green for a row whose VM is live on the mpd-proxy overlay, and the reset
// that ends it. Foreground only, so the padding spaces stay invisible.
const (
	ansiGreen = "\033[32m"
	ansiReset = "\033[0m"
)

// overlayVMs is the set of VM ids mpd-proxy currently tunnels — the VMs whose
// 10.163.<NNN>.x services this host actually reaches. A var so tests can
// substitute it; the default asks a running mpd-proxy over its control socket
// and treats an absent one (the overlay simply not up — the common case) as
// "none connected", never an error. One socket round trip for the whole
// listing, not one per VM.
var overlayVMs = func() map[string]bool {
	vms, err := proxy.New(proxy.DefaultSocket()).List()
	if err != nil {
		return nil
	}
	set := make(map[string]bool, len(vms))
	for _, vm := range vms {
		set[vm.ID] = true
	}
	return set
}

// colorEnabled reports whether to emit ANSI colour: stdout is a real terminal
// and NO_COLOR is unset (https://no-color.org). A var so a test can force it;
// piping `ls` to a file or another program gets clean, uncoloured text.
var colorEnabled = func() bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// headerLine is the table header, printed before the probes so the table's
// shape appears immediately rather than after the slowest dial.
func headerLine() string {
	return fmt.Sprintf(listRow, "NNN", padNotes("NOTES"), "NAME", "BACKEND", "IP", "USER", "SSH")
}

// renderRow formats one VM's row and paints it green only when the VM is both
// reachable right now (SSH `up`) and live on the mpd-proxy overlay — the
// at-a-glance answer to "which VMs can I actually reach?". Reachability is half
// of it: a stopped or down VM still sitting in mpd-proxy's peer list is not
// reachable, so it stays plain. The remaining confusing case — an `up` row that
// is *not* green — is a VM powered but off the overlay, whose 10.163.<NNN>.x
// services are therefore unreachable.
func renderRow(e registry.Entry, p probe, onOverlay, color bool) string {
	row := fmt.Sprintf(listRow, e.ID.String(), padNotes(p.notes), e.ID.Name(), e.Backend, e.IP, e.User, p.ssh)
	if color && onOverlay && p.ssh == sshUp {
		return ansiGreen + strings.TrimSuffix(row, "\n") + ansiReset + "\n"
	}
	return row
}

func printListTable(ctx context.Context, entries []registry.Entry) {
	fmt.Print(headerLine())
	probes := probeRows(ctx, entries)
	onOverlay := overlayVMs()
	color := colorEnabled()
	for i, e := range entries {
		fmt.Print(renderRow(e, probes[i], onOverlay[e.ID.String()], color))
	}
}

func printListJSON(ctx context.Context, entries []registry.Entry) error {
	probes := probeRows(ctx, entries)
	onOverlay := overlayVMs()
	type row struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Notes   string `json:"notes"`
		Backend string `json:"backend"`
		IP      string `json:"ip"`
		User    string `json:"user"`
		SSH     string `json:"ssh"`
		Overlay bool   `json:"overlay"`
	}
	rows := make([]row, 0, len(entries))
	for i, e := range entries {
		rows = append(rows, row{e.ID.String(), e.ID.Name(), probes[i].notes, e.Backend, e.IP, e.User, probes[i].ssh, onOverlay[e.ID.String()]})
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// sshState reports whether the VM answers on ssh, as a quick liveness signal.
// A short timeout keeps a down VM from stalling the whole listing.
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
	return sshUp
}

// sshUp is the SSH column's one "reachable now" value — the dial answered.
// Every other value (down, ?, or a hypervisor power word) means not reachable,
// and only a reachable VM earns the overlay-green highlight.
const sshUp = "up"
