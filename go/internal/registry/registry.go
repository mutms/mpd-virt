// Package registry is the source of truth for which boxes mpd-virt knows
// about: one shell-style KEY=VALUE file per box at ~/.mpd-virt/<NNN>/env.
//
// Ported from Registry.swift, simplified for the container/general world:
// the Parallels-only fields (uuid, disk, ram) are gone. The backend (which
// platform the box runs on) is recorded here because it is no longer
// derivable from the id — it is supplied explicitly at takeover.
package registry

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// Entry is one box's registry record. Name derives from ID; the
// non-derivable facts are IP, User, and Backend.
type Entry struct {
	ID      vmid.ID
	IP      string
	User    string
	Backend string
}

// Save writes (or overwrites) the env file for a box, creating the
// <NNN>/ directory as needed.
func Save(e Entry) error {
	dir := paths.VMDir(e.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf(`# mpd-virt registry entry for %s.
# Source of truth for takeover. Edit at your own risk.
MPD_VM_OCTET=%s
MPD_VM_NAME=%s
MPD_VM_BACKEND=%s
MPD_VM_IP=%s
MPD_VM_USER=%s
`, e.ID.Name(), e.ID.Pad(), e.ID.Name(), e.Backend, e.IP, e.User)
	return os.WriteFile(paths.VMEnv(e.ID), []byte(body), 0o644)
}

// Exists reports whether a box's env file is present (no parsing).
func Exists(id vmid.ID) bool {
	_, err := os.Stat(paths.VMEnv(id))
	return err == nil
}

// Load reads and parses a box's env file. It errors if the file is
// missing or lacks a required key.
func Load(id vmid.ID) (Entry, error) {
	f, err := os.Open(paths.VMEnv(id))
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, fmt.Errorf("no registry entry for %s", id.Name())
		}
		return Entry{}, err
	}
	defer f.Close()

	kv := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if err := sc.Err(); err != nil {
		return Entry{}, err
	}

	ip, user := kv["MPD_VM_IP"], kv["MPD_VM_USER"]
	if ip == "" || user == "" {
		return Entry{}, fmt.Errorf("registry entry for %s is missing MPD_VM_IP or MPD_VM_USER", id.Name())
	}
	// Backend is metadata for lifecycle commands, not needed to reach the box,
	// so it is optional: an entry written before backends were recorded still
	// loads.
	return Entry{ID: id, IP: ip, User: user, Backend: kv["MPD_VM_BACKEND"]}, nil
}

// Remove deletes a box's <NNN>/ dir entirely. It does not touch
// ~/.mpd-virt/conf/, so re-adopting at the same id reuses the trust
// material. No-op if the dir is absent.
func Remove(id vmid.ID) error {
	err := os.RemoveAll(paths.VMDir(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
