//go:build !darwin

package cli

import (
	"context"
	"fmt"

	"github.com/mutms/mpd-virt/go/internal/ca"
)

// reportCATrustStore is the non-macOS placeholder: the OS trust store and its
// removal command differ per platform (update-ca-certificates on Debian, etc.)
// and are not wired yet. Only the macOS Keychain step is automated today; this
// keeps a Linux/other build honest rather than printing keychain instructions
// that do not apply.
func reportCATrustStore(_ context.Context) {
	fmt.Printf("  • OS trust store: if you added the %q to it, remove it there "+
		"(mechanism is platform-specific and not automated here yet).\n", ca.RootCommonName)
}
