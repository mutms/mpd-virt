// Command mpd-virt provisions and adopts mpd environments on macOS.
//
// It is the host-side counterpart to `mpd`, which runs inside the box.
// mpd-virt creates native Apple containers and adopts existing VMs, then
// drives them over SSH — it owns nothing inside the box beyond what SSH
// and the mpd binary already provide.
//
// The grammar is verb-first: `mpd-virt takeover 135 10.211.55.135`,
// `mpd-virt create 160`. Every verb names a box by its id NNN, from which
// the hostname (mpd-<NNN>), class and zone derive; the host IP does not —
// see internal/vmid and docs/proposals/apple-container-backend.md.
package main

import (
	"fmt"
	"os"

	"github.com/mutms/mpd-virt-macos/go/internal/cli"
)

// version is stamped at build time via -ldflags.
var version = "dev"

func main() {
	if err := cli.Root(version).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "mpd-virt:", err)
		os.Exit(1)
	}
}
