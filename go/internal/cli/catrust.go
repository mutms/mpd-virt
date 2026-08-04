package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/ca"
	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

const systemKeychain = "/Library/Keychains/System.keychain"

// checkCATrust reports whether this Mac trusts the mpd root CA and, if not,
// prints the one-time `security add-trusted-cert` that fixes it. It never
// blocks: the verb continues either way, and re-running the verb re-checks and
// confirms once trust is in place — the same idempotent "here's what's left"
// pattern as the mpd-proxy/SOCKS hint.
//
// Trusting the root makes *.mpd.test HTTPS work transparently for every app on
// the Mac (Safari, curl, the WireGuard-overlay path). The root is
// name-constrained to mpd.test, so trusting it can vouch for no other name.
// Per-app trust stores (a dedicated Firefox, say) are the dev's own affair —
// this speaks only for the System Keychain.
func checkCATrust(ctx context.Context, id vmid.ID) {
	caPath := ca.RootCertPath()

	// verify-cert exits 0 only when the live root is a trusted SSL anchor.
	if res, err := exec.Capture(ctx, exec.Cmd{
		Name: "security", Args: []string{"verify-cert", "-c", caPath},
	}); err == nil && res.Code == 0 {
		pass("root CA trusted in the System Keychain")
		return
	}

	fmt.Printf("\n  … mpd root CA not trusted in the System Keychain.\n")
	if staleCAPresent(ctx) {
		fmt.Printf("    A different %q is already there (a previous, regenerated root) —\n"+
			"    remove the stale one first:\n"+
			"      sudo security delete-certificate -c %q %s\n",
			ca.RootCommonName, ca.RootCommonName, systemKeychain)
	}
	fmt.Printf("    Trust it system-wide (one-time, needs admin):\n"+
		"      sudo security add-trusted-cert -d -r trustRoot -k %s %s\n"+
		"    Then `mpd-virt start %s` again to confirm. (A browser with its own\n"+
		"    trust store — a dedicated Firefox — you import it there yourself.)\n",
		systemKeychain, caPath, id.Pad())
}

// staleCAPresent reports whether a cert named like the root but with a
// different fingerprint sits in the System Keychain — a leftover from a
// regenerated root that would shadow the live one. Only consulted when the live
// root is already known untrusted, so a match means "present but wrong".
func staleCAPresent(ctx context.Context) bool {
	want, err := ca.RootFingerprintSHA1()
	if err != nil {
		return false
	}
	res, err := exec.Capture(ctx, exec.Cmd{
		Name: "security",
		Args: []string{"find-certificate", "-a", "-c", ca.RootCommonName, "-Z", systemKeychain},
	})
	if err != nil || res.Code != 0 {
		return false // none present at all
	}
	// `-Z` prints a "SHA-1 hash: <40 hex>" line per match; a present hash that
	// differs from the live root is the stale cert.
	for _, line := range strings.Split(res.Stdout, "\n") {
		if h, ok := strings.CutPrefix(strings.TrimSpace(line), "SHA-1 hash:"); ok {
			if got := strings.ToUpper(strings.TrimSpace(h)); got != "" && got != want {
				return true
			}
		}
	}
	return false
}
