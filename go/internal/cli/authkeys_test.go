package cli

import (
	"strings"
	"testing"
)

const (
	kA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 warpgate"
	kB = "ssh-rsa AAAAB3NzaC1yc2E laptop"
)

// applyManagedKeys owns only its delimited block: the primary key and any
// hand-added key (both outside the block) survive add, change, and removal of
// the managed keys, and the whole thing is idempotent.
func TestApplyManagedKeys(t *testing.T) {
	const primary = "ssh-ed25519 AAAAPRIMARY me@mac\n"

	// Add a block to a file that only had the primary key.
	one := applyManagedKeys(primary, []string{kA})
	if !strings.Contains(one, primary) || !strings.Contains(one, kA) {
		t.Fatalf("first apply dropped a key:\n%s", one)
	}
	if !strings.Contains(one, authKeysBegin) || !strings.Contains(one, authKeysEnd) {
		t.Fatalf("first apply has no managed block:\n%s", one)
	}

	// Idempotent: same keys in, byte-identical out.
	if got := applyManagedKeys(one, []string{kA}); got != one {
		t.Errorf("not idempotent:\n---got---\n%s\n---want---\n%s", got, one)
	}

	// Changing the key set replaces only the block; the primary stays put and
	// the old key is gone.
	two := applyManagedKeys(one, []string{kA, kB})
	if !strings.Contains(two, primary) || !strings.Contains(two, kB) {
		t.Fatalf("changed apply lost the primary or the new key:\n%s", two)
	}
	if strings.Count(two, authKeysBegin) != 1 {
		t.Errorf("managed block duplicated:\n%s", two)
	}

	// Emptying the list removes the block but leaves the primary key.
	none := applyManagedKeys(two, nil)
	if strings.Contains(none, authKeysBegin) {
		t.Errorf("empty key set should remove the block:\n%s", none)
	}
	if !strings.Contains(none, "ssh-ed25519 AAAAPRIMARY me@mac") {
		t.Errorf("removing managed keys must not touch the primary key:\n%s", none)
	}

	// No keys and no existing block: the file is left exactly alone.
	if got := applyManagedKeys(primary, nil); got != primary {
		t.Errorf("no-op case rewrote the file: %q", got)
	}
}

func TestValidAuthorizedKey(t *testing.T) {
	good := []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 warpgate@bastion",
		"ssh-rsa AAAAB3NzaC1yc2E",
		"ecdsa-sha2-nistp256 AAAAE2VjZHNh key",
		"  ssh-ed25519 AAAAC3Nz key  ", // surrounding space is trimmed
	}
	for _, k := range good {
		if !validAuthorizedKey(k) {
			t.Errorf("valid key rejected: %q", k)
		}
	}
	bad := []string{
		"",
		"not-a-key",
		"ssh-ed25519",                           // no blob
		"ssh-ed25519 AAAA\nssh-rsa BBBB evil",   // smuggled second line
		`command="rm -rf /" ssh-ed25519 AAAA k`, // options prefix not accepted
	}
	for _, k := range bad {
		if validAuthorizedKey(k) {
			t.Errorf("invalid key accepted: %q", k)
		}
	}
}
