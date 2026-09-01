// Package config is mpd-virt's own host-side settings, in
// ~/.mpd-virt/config.json — the knobs that are the developer's to set and are
// not a secret, a per-VM record, or a backend's own credentials. JSON so it
// opens and reviews cleanly like vm.json; the one cost is no comments, so every
// key is documented here and in AGENTS.md.
package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mutms/mpd-virt/go/internal/paths"
)

// Config mirrors config.json. Every field is optional; a missing file or key
// leaves the zero value, which each consumer reads as "unset".
type Config struct {
	// DefaultBackend is the backend adopt/create assume when --backend is
	// omitted on a first adoption (a re-adoption still reads the backend
	// recorded in the registry). Empty means none — --backend is then required.
	// The name is validated by the caller against the known backends.
	DefaultBackend string `json:"default_backend"`
}

// BackendMirror reads the optional oci_mirror_location from
// ~/.mpd-virt/backends/<backend>.json: an OCI pull-through cache is a property
// of the network the backend's VMs live on (the home LAN's cache is
// unreachable from a laptop's local VMs on the road), so each backend carries
// its own. A missing file or key means no mirror; a present-but-malformed file
// is an error, so a JSON typo does not silently disable the cache.
func BackendMirror(backend string) (string, error) {
	path := paths.BackendConfig(backend)
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var f struct {
		OCIMirrorLocation string `json:"oci_mirror_location"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return "", fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return f.OCIMirrorLocation, nil
}

// Load reads config.json. A missing file is the zero Config, not an error — the
// common case, and what "no settings yet" looks like. A present-but-malformed
// file IS an error, so a JSON typo does not silently disable a setting the
// developer believed they had set.
func Load() (Config, error) {
	body, err := os.ReadFile(paths.Config())
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(body, &c); err != nil {
		return Config{}, fmt.Errorf("%s is not valid JSON: %w", paths.Config(), err)
	}
	return c, nil
}

// Architectures a VM can be. mpd builds nothing per-arch itself; this
// exists so the right third-party installers reach a VM (see
// paths.Installers).
const (
	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
)

// fixedArch is the architecture a backend can only ever be: Parallels, UTM
// and Apple container run on Apple silicon. The rest can be either, so they
// read `arch` from their backend config and fall back to amd64.
var fixedArch = map[string]string{
	"parallels": ArchARM64,
	"utm":       ArchARM64,
	"container": ArchARM64,
}

// BackendArch reports the architecture of VMs on a backend, from the
// optional `arch` key in ~/.mpd-virt/backends/<backend>.json.
//
// Backends that can only be one architecture ignore the key. For the rest —
// proxmox, libvirt, generic — it is a property of the machine the VMs run
// on, not of mpd, so it lives beside that backend's other settings. Unset
// means amd64: it is what a server and a Linux workstation still usually
// are, and an arm64 host is the deliberate case worth writing down.
func BackendArch(backend string) (string, error) {
	if a, ok := fixedArch[backend]; ok {
		return a, nil
	}
	body, err := os.ReadFile(paths.BackendConfig(backend))
	if err != nil {
		if os.IsNotExist(err) {
			return ArchAMD64, nil
		}
		return "", err
	}
	var f struct {
		Arch string `json:"arch"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return "", fmt.Errorf("%s is not valid JSON: %w", paths.BackendConfig(backend), err)
	}
	switch f.Arch {
	case "":
		return ArchAMD64, nil
	case ArchAMD64, ArchARM64:
		return f.Arch, nil
	default:
		return "", fmt.Errorf("%s: arch %q is not one of %s, %s",
			paths.BackendConfig(backend), f.Arch, ArchAMD64, ArchARM64)
	}
}
