package registry

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mutms/mpd-virt/go/internal/paths"
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

// A saved entry round-trips through vm.json, and the file is the pretty,
// reviewable shape the refactor is for: real JSON with the derived name and the
// three-digit id as a string.
func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("MPD_VIRT_ROOT", t.TempDir())
	id := mustID(t, "150")
	in := Entry{ID: id, IP: "10.1.10.150", User: "skodak", Backend: "proxmox", Notes: "customer acme"}
	if err := Save(in); err != nil {
		t.Fatal(err)
	}

	got, err := Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Errorf("round-trip = %+v, want %+v", got, in)
	}

	body, err := os.ReadFile(paths.VMRecord(id))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct{ ID, Name, Backend, IP, User, Notes string }
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatalf("vm.json is not valid JSON: %v", err)
	}
	if rec.ID != "150" || rec.Name != "mpd-150" {
		t.Errorf("id/name in file = %q/%q, want 150/mpd-150", rec.ID, rec.Name)
	}
	if !strings.Contains(string(body), "\n  ") {
		t.Errorf("vm.json is not pretty-printed:\n%s", body)
	}
}

// Notes is sticky: a lifecycle Save that carries no notes must preserve the
// cache `list` maintains, while a non-empty value overwrites it.
func TestSaveNotesSticky(t *testing.T) {
	t.Setenv("MPD_VIRT_ROOT", t.TempDir())
	id := mustID(t, "151")

	if err := Save(Entry{ID: id, IP: "10.1.10.151", User: "skodak", Backend: "proxmox", Notes: "first"}); err != nil {
		t.Fatal(err)
	}
	// A start-shaped Save (no notes) leaves the cached notes intact.
	if err := Save(Entry{ID: id, IP: "10.1.10.151", User: "skodak", Backend: "proxmox"}); err != nil {
		t.Fatal(err)
	}
	if e, _ := Load(id); e.Notes != "first" {
		t.Errorf("notes after note-less Save = %q, want first (sticky)", e.Notes)
	}
	// A non-empty value overwrites.
	if err := Save(Entry{ID: id, IP: "10.1.10.151", User: "skodak", Backend: "proxmox", Notes: "second"}); err != nil {
		t.Fatal(err)
	}
	if e, _ := Load(id); e.Notes != "second" {
		t.Errorf("notes after refresh = %q, want second", e.Notes)
	}
}

// A missing record, a mangled address, and an id that disagrees with its
// directory are all refused — the record flows into ssh command lines, so a bad
// one is caught here, not obeyed downstream.
func TestLoadRejects(t *testing.T) {
	t.Setenv("MPD_VIRT_ROOT", t.TempDir())
	id := mustID(t, "152")

	if _, err := Load(id); err == nil {
		t.Error("missing record should error")
	}

	dir := paths.VMDir(id)
	_ = os.MkdirAll(dir, 0o700)
	write := func(s string) {
		if err := os.WriteFile(paths.VMRecord(id), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"id":"152","ip":"not-an-ip","user":"skodak"}`)
	if _, err := Load(id); err == nil {
		t.Error("a non-IPv4 ip should be refused")
	}
	write(`{"id":"999","ip":"10.1.10.152","user":"skodak"}`)
	if _, err := Load(id); err == nil {
		t.Error("an id that disagrees with the directory should be refused")
	}
	write(`{"id":"152","ip":"10.1.10.152","user":"skodak"}`)
	if _, err := Load(id); err != nil {
		t.Errorf("a valid record should load: %v", err)
	}
}
