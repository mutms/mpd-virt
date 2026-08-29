package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Debris from a failed create must not survive into the retry.
func TestUnfinishedCreateStateIsCleared(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := testVMID(t, 158)

	dir := filepath.Join(home, ".mpd-virt", "158")
	if err := os.MkdirAll(filepath.Join(dir, "ca"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "known_hosts"), []byte("mpd-158 ssh-ed25519 AAAAstale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearLeftoverState(id); err != nil {
		t.Fatalf("clearLeftoverState: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s still there, want it cleared", dir)
	}
}

// An adopted VM's identity is not create's to discard.
func TestAdoptedStateIsRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	id := testVMID(t, 158)

	dir := filepath.Join(home, ".mpd-virt", "158")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vm.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := clearLeftoverState(id)
	if err == nil {
		t.Fatal("clearLeftoverState returned nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "mpd-virt remove 158") {
		t.Errorf("error does not name the fix: %v", err)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Error("refusal must not remove anything")
	}
}

// The normal case.
func TestNoStateIsFine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := clearLeftoverState(testVMID(t, 158)); err != nil {
		t.Errorf("clearLeftoverState on a clean number: %v", err)
	}
}
