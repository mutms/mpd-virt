package sshconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

func testID(t *testing.T, n int) vmid.ID {
	t.Helper()
	id, err := vmid.Parse(itoa(n))
	if err != nil {
		t.Fatalf("vmid.Parse(%d): %v", n, err)
	}
	return id
}

func itoa(n int) string {
	return string(rune('0'+n/100)) + string(rune('0'+(n/10)%10)) + string(rune('0'+n%10))
}

func useTempConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	t.Setenv("MPD_VIRT_SSH_CONFIG", path)
	return path
}

// The managed block carries exactly three stanzas: the bare name for the
// runtime, `-vm` for the box, and `-socks`. The naive slice edit that
// would render runtime.runtime.<zone> is what this test pins against, and
// the bare name must never resolve to the box's IP again — that was the
// old meaning and silently landing on the VM is the regression to catch.
func TestWriteRendersSingleRuntimeStanza(t *testing.T) {
	path := useTempConfig(t)
	id := testID(t, 158)

	if err := Write(id, "10.1.10.158", "dev"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)

	for _, want := range []string{
		"Host mpd-158\n    HostName runtime.158.mpd.test\n",
		"    ProxyJump mpd-158-vm\n",
		"Host mpd-158-vm\n    HostName 10.1.10.158\n",
		"Host mpd-158-socks\n",
		"    DynamicForward 1080\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("managed block should contain %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{
		"-php", "-node", "-util", "runtime.runtime.",
		"Host mpd-158\n    HostName 10.1.10.158\n", // the pre-swap meaning
		"Host mpd-158-runtime\n",
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("managed block must not contain %q:\n%s", forbidden, got)
		}
	}
}

// Rewriting replaces the whole fenced block, so an upgraded mpd-virt
// cleanly retires the legacy per-language aliases.
func TestWriteReplacesLegacyBlock(t *testing.T) {
	path := useTempConfig(t)
	id := testID(t, 158)

	legacy := beginMarker(id) + "\n" +
		"Host mpd-158-php\n    HostName php.runtime.158.mpd.test\n" +
		endMarker(id) + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Write(id, "10.1.10.158", "dev"); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "mpd-158-php") {
		t.Errorf("legacy alias survived the rewrite:\n%s", body)
	}
	if !strings.Contains(string(body), "mpd-158-vm") {
		t.Errorf("new alias missing after rewrite:\n%s", body)
	}
}

// Strip removes the block and leaves foreign content alone.
func TestStripKeepsForeignContent(t *testing.T) {
	path := useTempConfig(t)
	id := testID(t, 158)

	foreign := "Host github.com\n    User git\n"
	if err := os.WriteFile(path, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(id, "10.1.10.158", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := Strip(id); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "Host github.com") {
		t.Errorf("foreign content lost:\n%s", body)
	}
	if strings.Contains(string(body), "mpd-158") {
		t.Errorf("managed block survived Strip:\n%s", body)
	}
}
