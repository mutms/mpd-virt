package cli

import (
	"context"
	"testing"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

func mustID(t *testing.T, s string) vmid.ID {
	t.Helper()
	id, err := vmid.Parse(s)
	if err != nil {
		t.Fatalf("vmid.Parse(%q): %v", s, err)
	}
	return id
}

// shortNotes distils raw VM notes into one table cell: first non-blank line,
// leading markdown marker gone, control chars and whitespace runs folded, and
// truncated to notesWidth with an ellipsis.
func TestShortNotes(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"", ""},                                           // no notes — the common case
		{"test vm", "test vm"},                             // short, untouched
		{"# prod db\nsecond line", "prod db"},              // markdown heading, first line only
		{"\n\n  trixie - server", "trixie - server"},       // leading blank lines skipped, trimmed
		{"a\t\tb   c", "a b c"},                            // whitespace runs collapse to one space
		{"x\x00y\x1by", "x y y"},                           // control chars (NUL, ESC) never reach the terminal
		{"trixie - gnome desktop", "trixie - gnome desk…"}, // 22 chars → 19 + ellipsis
		{"12345678901234567890", "12345678901234567890"},   // exactly notesWidth — no ellipsis
	}
	for _, c := range cases {
		if got := shortNotes(c.raw); got != c.want {
			t.Errorf("shortNotes(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// padNotes pads to a fixed number of display columns by rune count, so a
// multibyte cell does not jag the columns that follow it.
func TestPadNotes(t *testing.T) {
	if got := padNotes("abc"); len([]rune(got)) != notesWidth {
		t.Errorf("padNotes(ascii) rune width = %d, want %d", len([]rune(got)), notesWidth)
	}
	// An accented name and the ellipsis are multi-byte; byte padding would
	// under-pad exactly these rows, so the pad must count runes.
	if got := padNotes("škoďák…"); len([]rune(got)) != notesWidth {
		t.Errorf("padNotes(multibyte) rune width = %d, want %d", len([]rune(got)), notesWidth)
	}
	if full := "12345678901234567890"; padNotes(full) != full {
		t.Errorf("padNotes at full width should add nothing")
	}
}

// cachedNotes returns a live value and writes it through, and falls back to the
// last written value when the backend goes quiet — the whole point being a
// legible listing while the Proxmox host is unreachable or the VM is off.
func TestCachedNotes(t *testing.T) {
	t.Setenv("MPD_VIRT_ROOT", t.TempDir())
	id := mustID(t, "150")

	// A live value passes through and is cached, though the VM dir is absent.
	if got := cachedNotes(id, "customer acme"); got != "customer acme" {
		t.Fatalf("live pass-through = %q, want customer acme", got)
	}
	// No live value (backend unreachable): the cached value stands in.
	if got := cachedNotes(id, ""); got != "customer acme" {
		t.Errorf("cache fallback = %q, want customer acme", got)
	}
	// A fresh live value overwrites the cache.
	cachedNotes(id, "customer beta")
	if got := cachedNotes(id, ""); got != "customer beta" {
		t.Errorf("cache after refresh = %q, want customer beta", got)
	}
	// Never cached, and nothing live, is empty — never an error.
	if got := cachedNotes(mustID(t, "151"), ""); got != "" {
		t.Errorf("no cache and no live = %q, want empty", got)
	}
}

// stubPowerState substitutes the backend power probe for one test.
func stubPowerState(t *testing.T, state string) {
	t.Helper()
	orig := powerState
	powerState = func(context.Context, vmid.ID, backend.Backend) string { return state }
	t.Cleanup(func() { powerState = orig })
}

// A VM the backend reports off shows that power word verbatim and is never
// dialed — the whole point is to skip the SSH timeout a dead IP would blackhole.
// The blackhole IP proves it: were it dialed, the case would hang, not return
// instantly with the power word.
func TestEntryStateOffSkipsDial(t *testing.T) {
	for _, state := range []string{"stopped", "suspended", "paused"} {
		stubPowerState(t, state)
		e := registry.Entry{ID: mustID(t, "150"), IP: "10.255.255.1", Backend: "proxmox"}
		if got := entryState(context.Background(), e); got != state {
			t.Errorf("power state %q: entryState = %q, want %q (no dial)", state, got, state)
		}
	}
}

// A VM reported running is still dialed — running is not reachable. With no IP
// the dial resolves instantly to "?", which proves the running branch fell
// through to sshState rather than returning the power word.
func TestEntryStateRunningFallsThroughToDial(t *testing.T) {
	stubPowerState(t, "running")
	e := registry.Entry{ID: mustID(t, "151"), IP: "", Backend: "proxmox"}
	if got := entryState(context.Background(), e); got != "?" {
		t.Errorf("running VM should be dialed; with no IP entryState = %q, want %q", got, "?")
	}
}

// The backend that cannot report state — every generic VM, or a failed probe —
// reads as "" and falls through to the dial: the connect-first behaviour.
func TestEntryStateUnknownFallsThroughToDial(t *testing.T) {
	stubPowerState(t, "")
	e := registry.Entry{ID: mustID(t, "152"), IP: "", Backend: "generic"}
	if got := entryState(context.Background(), e); got != "?" {
		t.Errorf("unknown state should be dialed; with no IP entryState = %q, want %q", got, "?")
	}
}
