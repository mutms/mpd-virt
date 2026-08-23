package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

func TestParseSizeMiB(t *testing.T) {
	cases := map[string]int{
		"8g":    8192,
		"8G":    8192,
		"8192m": 8192,
		"8192":  8192,
		" 4g ":  4096,
		"":      0,
		"junk":  0,
	}
	for in, want := range cases {
		if got := parseSizeMiB(in); got != want {
			t.Errorf("parseSizeMiB(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseSizeGiB(t *testing.T) {
	cases := map[string]int{
		"80g":    80,
		"80G":    80,
		"81920m": 80,
		"81920":  80, // bare number read as MiB → 80 GiB
		"":       0,
		"nope":   0,
	}
	for in, want := range cases {
		if got := parseSizeGiB(in); got != want {
			t.Errorf("parseSizeGiB(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestUTMCanonicalIP(t *testing.T) {
	id, _ := vmid.Parse("158")
	if got := utmCanonicalIP(id); got != "192.168.64.158" {
		t.Errorf("utmCanonicalIP(158) = %q, want 192.168.64.158", got)
	}
}

func TestAsQuote(t *testing.T) {
	cases := map[string]string{
		"mpd-158":      `"mpd-158"`,
		`a"b`:          `"a\"b"`,
		`c\d`:          `"c\\d"`,
		`/tmp/x y.raw`: `"/tmp/x y.raw"`,
	}
	for in, want := range cases {
		if got := asQuote(in); got != want {
			t.Errorf("asQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUTMNetworkConfig(t *testing.T) {
	got := utmNetworkConfig("192.168.64.158")
	for _, want := range []string{
		"version: 2",
		"enp0s1:",
		"addresses: [192.168.64.158/24]",
		"gateway4: 192.168.64.1",
		"addresses: [192.168.64.1]", // nameserver = gateway
	} {
		if !strings.Contains(got, want) {
			t.Errorf("utmNetworkConfig missing %q in:\n%s", want, got)
		}
	}
}

func TestCidataUserData(t *testing.T) {
	got := cidataUserData("skodak", "ssh-ed25519 AAAAKEY dev@mac", "mpd-158")
	for _, want := range []string{
		"#cloud-config",
		"hostname: mpd-158",
		"name: skodak",
		"sudo: ALL=(ALL) NOPASSWD:ALL",
		"lock_passwd: true",
		"- ssh-ed25519 AAAAKEY dev@mac",
		"ssh_pwauth: false",
		"resize_rootfs: true",
		"systemctl enable --now ssh",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("cidataUserData missing %q in:\n%s", want, got)
		}
	}
}

func TestCidataMetaData(t *testing.T) {
	got := cidataMetaData("mpd-158")
	if !strings.Contains(got, "instance-id: mpd-158") || !strings.Contains(got, "local-hostname: mpd-158") {
		t.Errorf("cidataMetaData = %q", got)
	}
}

// TestUTMLivePlumbing exercises the real osascript + hdiutil paths against
// an installed UTM: build a cidata seed, create a VM referencing it and a
// tiny fake disk, confirm exists/status, then delete it. Gated on
// MPD_VIRT_UTM_LIVE=1 (and UTM present) so ordinary `go test` never touches
// the desktop app. It deliberately skips the 200 MB image download + boot —
// this proves provisioning, not that the guest runs.
func TestUTMLivePlumbing(t *testing.T) {
	if os.Getenv("MPD_VIRT_UTM_LIVE") != "1" {
		t.Skip("set MPD_VIRT_UTM_LIVE=1 to run the live UTM plumbing test")
	}
	if err := requireUTM(); err != nil {
		t.Skipf("UTM not installed: %v", err)
	}
	ctx := context.Background()
	name := "mpd-199" // a test id unlikely to collide with a real VM

	// Clean any leftover from a previous run.
	_, _ = runOsascript(ctx, utmKillScript(name))
	_, _ = runOsascript(ctx, utmDeleteScript(name))

	dir := t.TempDir()
	disk := filepath.Join(dir, name+".raw")
	if err := os.Truncate(disk, 0); err != nil { // create empty file
		if f, e := os.Create(disk); e == nil {
			f.Close()
		} else {
			t.Fatal(e)
		}
	}
	if err := os.Truncate(disk, 1<<30); err != nil { // 1 GiB sparse, no real bytes
		t.Fatal(err)
	}
	seed := filepath.Join(dir, "seed.iso")
	if err := makeCidataISO(ctx, seed, "skodak", "ssh-ed25519 AAAATEST test@mac", name, utmNetworkConfig(utmCanonicalIP(mustID(t, "199")))); err != nil {
		t.Fatalf("makeCidataISO: %v", err)
	}
	if fi, err := os.Stat(seed); err != nil || fi.Size() == 0 {
		t.Fatalf("seed ISO not produced: %v (size %d)", err, fi.Size())
	}
	t.Logf("cidata seed built: %s", seed)

	if _, err := runOsascript(ctx, utmCreateScript(name, 2048, 2, disk, seed)); err != nil {
		t.Fatalf("create VM: %v", err)
	}
	t.Cleanup(func() {
		_, _ = runOsascript(ctx, utmKillScript(name))
		_, _ = runOsascript(ctx, utmDeleteScript(name))
	})

	if !utmVMExists(ctx, name) {
		t.Fatal("VM does not exist after create")
	}
	t.Logf("VM status after create: %q", utmVMStatus(ctx, name))

	// Deleting the VM itself is UTM's business now (mpd-virt's `remove`
	// only un-adopts), so the teardown here uses the raw scripts — the same
	// ones create's own failure rollback runs.
	_, _ = runOsascript(ctx, utmKillScript(name))
	if _, err := runOsascript(ctx, utmDeleteScript(name)); err != nil {
		t.Fatalf("utmDeleteScript: %v", err)
	}
	if utmVMExists(ctx, name) {
		t.Fatal("VM still exists after delete")
	}
}

// TestUTMCreateScriptShape checks the AppleScript the create step sends is
// well-formed enough to carry the right VM name, arch, and both drives.
func TestUTMCreateScriptShape(t *testing.T) {
	s := utmCreateScript("mpd-158", 8192, 4, "/s/mpd-158.raw", "/s/seed.iso")
	for _, want := range []string{
		`tell application "UTM"`,
		"make new virtual machine",
		"backend:qemu",
		`name:"mpd-158"`,
		`architecture:"aarch64"`,
		"memory:8192",
		"cpu cores:4",
		`POSIX file "/s/mpd-158.raw"`,
		`POSIX file "/s/seed.iso"`,
		"network interfaces:{{mode:shared}}",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("utmCreateScript missing %q in:\n%s", want, s)
		}
	}
}
