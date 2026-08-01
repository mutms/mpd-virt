// Package paths holds the host-side filesystem locations mpd-virt owns
// under ~/.mpd-virt/. Ported from MpdVirt.swift's path section.
//
// MPD_VIRT_ROOT overrides the root, which keeps tests (and dry-runs) out
// of the developer's real ~/.mpd-virt.
package paths

import (
	"os"
	"path/filepath"

	"github.com/mutms/mpd-virt-macos/go/internal/vmid"
)

// Root is ~/.mpd-virt (or $MPD_VIRT_ROOT) — everything mpd-virt owns on
// the host. Holds conf/ (identity, survives delete) and per-box dirs.
func Root() string {
	if r := os.Getenv("MPD_VIRT_ROOT"); r != "" {
		return r
	}
	return filepath.Join(home(), ".mpd-virt")
}

// Conf is ~/.mpd-virt/conf — identity that survives `delete` (CA, certs).
func Conf() string { return filepath.Join(Root(), "conf") }

// CARoot is ~/.mpd-virt/conf/caroot — the root CA keypair.
func CARoot() string { return filepath.Join(Conf(), "caroot") }

// VMDir is ~/.mpd-virt/<NNN> — per-box bookkeeping.
func VMDir(id vmid.ID) string { return filepath.Join(Root(), id.Pad()) }

// VMEnv is ~/.mpd-virt/<NNN>/env — the registry entry for a box.
func VMEnv(id vmid.ID) string { return filepath.Join(VMDir(id), "env") }

func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}
