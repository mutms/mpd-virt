package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// State without a registry entry is deleted on confirmation.
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
	// Answering anything but "delete" aborts and touches nothing.
	withStdin(t, "no\n")
	if err := clearLeftoverState(id); err == nil {
		t.Error("declining the prompt returned nil, want an abort")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("abort must not delete anything")
	}

	withStdin(t, "delete\n")
	if err := clearLeftoverState(id); err != nil {
		t.Fatalf("clearLeftoverState: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s still there, want it deleted", dir)
	}
}

// withStdin points os.Stdin at a scripted answer for confirmWord.
func withStdin(t *testing.T, answer string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(answer); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; r.Close() })
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
