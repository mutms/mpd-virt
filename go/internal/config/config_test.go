package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("MPD_VIRT_TEST_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A missing config file is the zero Config, not an error — the common case.
func TestLoadMissing(t *testing.T) {
	t.Setenv("MPD_VIRT_TEST_ROOT", t.TempDir()) // empty root, no config.json
	c, err := Load()
	if err != nil {
		t.Fatalf("missing config should not error: %v", err)
	}
	if c.DefaultBackend != "" {
		t.Errorf("DefaultBackend = %q, want empty", c.DefaultBackend)
	}
}

// A well-formed config yields its values.
func TestLoadValid(t *testing.T) {
	write(t, `{"default_backend": "proxmox"}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultBackend != "proxmox" {
		t.Errorf("DefaultBackend = %q, want proxmox", c.DefaultBackend)
	}
}

// A malformed file is an error — a JSON typo must not silently disable a
// setting the developer thought they had set.
func TestLoadMalformed(t *testing.T) {
	write(t, `{"default_backend": "proxmox"`) // missing closing brace
	if _, err := Load(); err == nil {
		t.Error("malformed config.json should be an error")
	}
}
