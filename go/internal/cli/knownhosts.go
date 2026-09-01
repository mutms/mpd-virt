package cli

import (
	"os"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// Every host key mpd-virt knows about lives in the per-VM
// ~/.mpd-virt/<NNN>/known_hosts, and the developer's own ~/.ssh/known_hosts
// is never written: the managed ssh-config stanzas point UserKnownHostsFile
// at that file. First contact pins the VM's key there (see vmTarget).
// See docs/security.md.

// replaceEntries rewrites one alias's lines in the VM's known_hosts,
// leaving every other alias alone. Replace, not append: a re-imaged VM
// generates a new key, and a stale line beside it is a refused connection.
func replaceEntries(id vmid.ID, alias string, lines []string) error {
	path := paths.EnsureKnownHosts(id)

	var kept []string
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if f := strings.Fields(line); len(f) > 0 && f[0] == alias {
				continue
			}
			if strings.TrimSpace(line) != "" {
				kept = append(kept, line)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	body := strings.Join(append(kept, lines...), "\n") + "\n"
	return os.WriteFile(path, []byte(body), 0o600)
}
