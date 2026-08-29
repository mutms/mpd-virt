package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// A re-create of the same number must not stack duplicate lines.
func TestAppendKnownHostsIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	lines := []string{"mpd-222 ssh-ed25519 AAAAkey1", "mpd-222-runtime ssh-ed25519 AAAAkey2"}
	if n, err := appendKnownHosts(lines); err != nil || n != 2 {
		t.Fatalf("first append = %d, %v; want 2, nil", n, err)
	}
	if n, err := appendKnownHosts(lines); err != nil || n != 0 {
		t.Fatalf("second append = %d, %v; want 0, nil", n, err)
	}

	path := filepath.Join(home, ".ssh", "known_hosts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "mpd-222 ssh-ed25519"); got != 1 {
		t.Errorf("entry written %d times, want 1", got)
	}
	// ssh refuses a known_hosts it considers world-exposed.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("known_hosts mode = %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Join(home, ".ssh"))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("~/.ssh mode = %v, want 0700", di.Mode().Perm())
	}
}

// Only this VM's well-formed lines transfer out of the per-VM file.
func TestVMHostKeyLinesSelectsThisVMOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := testVMID(t, 222)

	dir := filepath.Join(home, ".mpd-virt", "222")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "mpd-222 ssh-ed25519 AAAAmine\nmpd-223 ssh-ed25519 AAAAtheirs\nmpd-222 short\n\n"
	if err := os.WriteFile(filepath.Join(dir, "known_hosts"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := vmHostKeyLines(id)
	if len(got) != 1 || got[0] != "mpd-222 ssh-ed25519 AAAAmine" {
		t.Errorf("vmHostKeyLines = %v, want just this VM's well-formed line", got)
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
