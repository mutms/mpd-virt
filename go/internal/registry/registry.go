// Package registry is the source of truth for which VMs mpd-virt knows
// about: one JSON record per VM at ~/.mpd-virt/<NNN>/vm.json.
//
// vm.json is a pretty-printed mirror of Entry — the non-derivable facts (IP,
// User, Backend) plus the derived Name for readability and the cached backend
// Notes. JSON so the file opens and reviews cleanly in a Finder/editor, where
// the old shell-style env file (and the plain-text notes cache beside it) did
// not. The backend is recorded because it is no longer derivable from the id —
// it is supplied explicitly at adoption. The id itself is the directory name,
// the authoritative source; the "id" in the file is for the human reading it.
package registry

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// Entry is one VM's registry record. Name derives from ID; the non-derivable
// facts are IP, User, and Backend. Notes is the cached first-line-ish backend
// note (proxmox Notes/description) `list` shows — a display cache, not
// identity, which is why Save treats it as sticky (see Save).
type Entry struct {
	ID      vmid.ID
	IP      string
	User    string
	Backend string
	Notes   string
}

// record is the on-disk shape of vm.json — Entry rendered with the id and the
// derived name as strings, field order fixed for a stable, readable file.
type record struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Backend string `json:"backend"`
	IP      string `json:"ip"`
	User    string `json:"user"`
	Notes   string `json:"notes"`
}

// Save writes (or overwrites) vm.json for a VM, creating the <NNN>/ directory
// as needed. Owner-only modes, like everything under ~/.mpd-virt — the record
// holds no secret, but the directory beside it holds the VM's CA key and
// pinned host key.
//
// Notes is sticky: the lifecycle verbs (adopt, start) Save an Entry that
// carries no notes, and it would be wrong for them to wipe the cache `list`
// maintains. So an empty e.Notes preserves whatever the file already had;
// only a non-empty value (from `list`'s refresh) overwrites it.
func Save(e Entry) error {
	dir := paths.VMDir(e.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	notes := e.Notes
	if notes == "" {
		if prev, err := Load(e.ID); err == nil {
			notes = prev.Notes
		}
	}
	rec := record{
		ID:      e.ID.String(),
		Name:    e.ID.Name(),
		Backend: e.Backend,
		IP:      e.IP,
		User:    e.User,
		Notes:   notes,
	}
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return os.WriteFile(paths.VMRecord(e.ID), body, 0o600)
}

// Exists reports whether a VM's record is present (no parsing).
func Exists(id vmid.ID) bool {
	_, err := os.Stat(paths.VMRecord(id))
	return err == nil
}

// Load reads and parses a VM's vm.json. It errors if the file is missing or
// invalid. The id is taken from the caller (the directory name is
// authoritative); a mismatched "id" inside the file is refused rather than
// obeyed, the same spirit as the value checks below.
func Load(id vmid.ID) (Entry, error) {
	body, err := os.ReadFile(paths.VMRecord(id))
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, fmt.Errorf("no registry entry for %s", id.Name())
		}
		return Entry{}, err
	}
	var rec record
	if err := json.Unmarshal(body, &rec); err != nil {
		return Entry{}, fmt.Errorf("registry entry for %s: %s is not valid JSON: %w", id.Name(), paths.VMRecord(id), err)
	}
	if rec.ID != "" && rec.ID != id.String() {
		return Entry{}, fmt.Errorf("registry entry for %s: file says id %q — the directory name is authoritative, fix the file", id.Name(), rec.ID)
	}

	ip, user := strings.TrimSpace(rec.IP), strings.TrimSpace(rec.User)
	if ip == "" || user == "" {
		return Entry{}, fmt.Errorf("registry entry for %s is missing ip or user", id.Name())
	}
	// Both values flow into ssh command lines and ~/.ssh/config, so a mangled
	// entry is refused here rather than obeyed downstream.
	if a, err := netip.ParseAddr(ip); err != nil || !a.Is4() {
		return Entry{}, fmt.Errorf("registry entry for %s: ip %q is not an IPv4 address", id.Name(), ip)
	}
	if strings.ContainsAny(user, " \t\"") {
		return Entry{}, fmt.Errorf("registry entry for %s: user %q is not a valid username", id.Name(), user)
	}
	// Backend is metadata for lifecycle commands, not needed to reach the VM,
	// so it is optional.
	return Entry{ID: id, IP: ip, User: user, Backend: strings.TrimSpace(rec.Backend), Notes: rec.Notes}, nil
}

// Remove deletes a VM's <NNN>/ dir entirely. It does not touch
// ~/.mpd-virt/conf/, so re-adopting at the same id reuses the trust
// material. No-op if the dir is absent.
func Remove(id vmid.ID) error {
	err := os.RemoveAll(paths.VMDir(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// List returns every adopted VM's entry, sorted by id. It scans ~/.mpd-virt
// for <NNN>/vm.json records; anything that is not a valid id directory (conf/, …)
// or lacks a loadable record is skipped. An absent root is an empty list, not
// an error.
func List() ([]Entry, error) {
	dirents, err := os.ReadDir(paths.Root())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, d := range dirents {
		if !d.IsDir() {
			continue
		}
		id, err := vmid.Parse(d.Name())
		if err != nil {
			continue // not an <NNN> VM dir (e.g. conf/)
		}
		e, err := Load(id)
		if err != nil {
			continue // no or incomplete record
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
