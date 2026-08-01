// Package sshconfig maintains one managed block per box in ~/.ssh/config,
// so `ssh mpd-<NNN>` reaches the box at its current address. Ported from
// SSHConfig.swift.
//
// The block sits between name-stamped markers so several boxes coexist in
// one config and each can be found and stripped cleanly:
//
//	# >>> mpd-<NNN> (managed by mpd-virt) >>>
//	Host mpd-<NNN>
//	    HostName <ip>
//	    ...
//	# <<< mpd-<NNN> <<<
//
// MPD_VIRT_SSH_CONFIG overrides the file, keeping tests off the real one.
//
// NOTE: the runtime aliases (mpd-<NNN>-php/node/util with ProxyJump) from
// the Swift version are not ported yet — they need internal/net. Only the
// box's own Host block is written here.
package sshconfig

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/mutms/mpd-virt-macos/go/internal/vmid"
)

// Path is the ssh config file mpd-virt manages (or $MPD_VIRT_SSH_CONFIG).
func Path() string {
	if p := os.Getenv("MPD_VIRT_SSH_CONFIG"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".ssh", "config")
}

func beginMarker(id vmid.ID) string {
	return "# >>> " + id.Name() + " (managed by mpd-virt) >>>"
}

func endMarker(id vmid.ID) string { return "# <<< " + id.Name() + " <<<" }

// render is the self-contained managed block for one box.
func render(id vmid.ID, ip, user string) string {
	return strings.Join([]string{
		beginMarker(id),
		"Host " + id.Name(),
		"    HostName " + ip,
		"    User " + user,
		"    StrictHostKeyChecking no",
		"    UserKnownHostsFile /dev/null",
		endMarker(id),
	}, "\n")
}

// Write inserts (or replaces) the managed block for a box, creating ~/.ssh
// and the config file if missing. Idempotent.
func Write(id vmid.ID, ip, user string) error {
	if err := ensureFile(); err != nil {
		return err
	}
	cur, err := os.ReadFile(Path())
	if err != nil {
		return err
	}
	rebuilt := stripBlock(string(cur), id)
	if rebuilt != "" {
		rebuilt += "\n\n"
	}
	rebuilt += render(id, ip, user) + "\n"
	return os.WriteFile(Path(), []byte(rebuilt), 0o600)
}

// Strip removes a box's managed block. No-op if absent.
func Strip(id vmid.ID) error {
	cur, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	stripped := stripBlock(string(cur), id)
	if stripped == string(cur) {
		return nil
	}
	return os.WriteFile(Path(), []byte(stripped), 0o600)
}

// Contains reports whether a managed block for the box exists.
func Contains(id vmid.ID) bool {
	cur, err := os.ReadFile(Path())
	if err != nil {
		return false
	}
	return strings.Contains(string(cur), beginMarker(id))
}

// stripBlock removes the marked block and collapses the blank lines left
// behind, so repeated write/strip cycles keep the file tidy.
func stripBlock(contents string, id vmid.ID) string {
	begin, end := beginMarker(id), endMarker(id)
	var out []string
	inside := false
	for _, line := range strings.Split(contents, "\n") {
		switch {
		case !inside && strings.Contains(line, begin):
			inside = true
		case inside && strings.Contains(line, end):
			inside = false
		case inside:
			// drop
		default:
			out = append(out, line)
		}
	}
	// Collapse consecutive blanks to one, then drop trailing blanks.
	var collapsed []string
	for _, line := range out {
		if line == "" && len(collapsed) > 0 && collapsed[len(collapsed)-1] == "" {
			continue
		}
		collapsed = append(collapsed, line)
	}
	for len(collapsed) > 0 && collapsed[len(collapsed)-1] == "" {
		collapsed = collapsed[:len(collapsed)-1]
	}
	return strings.Join(collapsed, "\n")
}

func ensureFile() error {
	dir := filepath.Dir(Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(Path()); os.IsNotExist(err) {
		return os.WriteFile(Path(), nil, 0o600)
	}
	return nil
}
