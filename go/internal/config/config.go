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

	// OCIMirrorLocation points every adopted VM's podman at an OCI
	// pull-through cache: when set, adopt/update write a
	// /etc/containers/registries.conf.d drop-in mirroring the registries mpd
	// pulls from (docker.io, ghcr.io) to this host[:port], so images are
	// fetched once and served from the LAN. Empty means no mirror — pulls go
	// straight upstream. The cache host is the developer's own; mpd-virt only
	// carries the setting. Example: "devoci.mpd.test:5000".
	OCIMirrorLocation string `json:"oci_mirror_location"`
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
