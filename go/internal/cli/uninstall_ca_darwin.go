//go:build darwin

package cli

import (
	"context"
	"fmt"

	"github.com/mutms/mpd-virt/go/internal/ca"
	"github.com/mutms/mpd-virt/go/internal/exec"
)

// reportCATrustStore reports whether the mpd root CA sits in the macOS Keychain
// and, if so, how to remove it. Presence only — uninstall never touches
// keychain trust itself; that stays an explicit sudo the dev chooses to run
// (removing it is what makes *.mpd.test stop being trusted system-wide).
//
// `find-certificate` with no keychain argument searches the default list (login
// + System), so this finds it wherever it was added.
func reportCATrustStore(ctx context.Context) {
	res, err := exec.Capture(ctx, exec.Cmd{
		Name: "security",
		Args: []string{"find-certificate", "-c", ca.RootCommonName},
	})
	if err != nil || res.Code != 0 {
		fmt.Printf("  • Keychain (macOS): no %q present — nothing to remove.\n", ca.RootCommonName)
		return
	}
	fmt.Printf("  • Keychain (macOS): %q is present. Remove it once you're truly done:\n"+
		"      sudo security delete-certificate -c %q /Library/Keychains/System.keychain\n"+
		"      (login keychain instead:  security delete-certificate -c %q)\n",
		ca.RootCommonName, ca.RootCommonName, ca.RootCommonName)
}
