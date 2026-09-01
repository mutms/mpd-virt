package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// Replacing one alias leaves every other alias's key alone.
func TestReplaceEntriesSwapsOneAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := testVMID(t, 222)

	if err := replaceEntries(id, "mpd-222", []string{"mpd-222 ssh-ed25519 AAAAvm"}); err != nil {
		t.Fatal(err)
	}
	if err := replaceEntries(id, "mpd-223", []string{"mpd-223 ssh-ed25519 AAAAold"}); err != nil {
		t.Fatal(err)
	}
	if err := replaceEntries(id, "mpd-223", []string{"mpd-223 ssh-ed25519 AAAAnew"}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(home, ".mpd-virt", "222", "known_hosts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "AAAAold") {
		t.Errorf("stale key kept:\n%s", got)
	}
	for _, want := range []string{"mpd-222 ssh-ed25519 AAAAvm", "mpd-223 ssh-ed25519 AAAAnew"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	// ssh refuses a known_hosts it considers world-exposed.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("known_hosts mode = %v, want 0600", fi.Mode().Perm())
	}
}

// The developer's own ~/.ssh/known_hosts is never touched.
func TestReplaceEntriesLeavesUserKnownHostsAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := testVMID(t, 222)

	if err := replaceEntries(id, "mpd-222", []string{"mpd-222 ssh-ed25519 AAAAvm"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "known_hosts")); !os.IsNotExist(err) {
		t.Errorf("~/.ssh/known_hosts = %v, want untouched", err)
	}
}

func testVMID(t *testing.T, n int) vmid.ID {
	t.Helper()
	id, err := vmid.Parse(strconv.Itoa(n))
	if err != nil {
		t.Fatalf("vmid.Parse(%d): %v", n, err)
	}
	return id
}
