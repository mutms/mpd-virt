package cli

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/registry"
)

// Extra ssh public keys the developer authorizes on a VM by hand — a bastion
// (warpgate), a second laptop, a CI runner. They live in the VM's vm.json
// record (authorized_keys) and are pushed to the dev user's
// ~/.ssh/authorized_keys on every start/update, so the edit-then-`start` loop
// is the whole workflow.
//
// mpd-virt owns only a delimited block in that file, between the markers below.
// The primary adoption key (placed by cloud-init or the adoption itself) and
// any key added by hand both live OUTSIDE the block and are never touched;
// only the block is regenerated from vm.json. That is what makes it safe to
// both add and remove keys this way without any risk to the key you reach the
// VM with — remove one from vm.json and it is gone from the block on the next
// start, while everything else in the file stays put.
const (
	authKeysBegin  = "# >>> mpd-virt managed keys >>>"
	authKeysEnd    = "# <<< mpd-virt managed keys <<<"
	authKeysStaged = "/tmp/mpd-virt-authorized-keys"
)

// authKeyRe matches a bare ssh public key line: a known key type, its base64
// blob, and an optional comment. authorized_keys option prefixes
// (command="…", restrict, …) are deliberately not accepted — add those by hand
// outside the managed block; this field is for plain "grant this key" entries.
var authKeyRe = regexp.MustCompile(`^(?:ssh-ed25519|ssh-rsa|ssh-dss|ecdsa-sha2-nistp(?:256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com)\s+[A-Za-z0-9+/]+={0,3}(?:\s+\S.*)?$`)

// validAuthorizedKey reports whether s is exactly one plain ssh public key
// line — no embedded newline (which would smuggle a second line into the file)
// and the shape authKeyRe requires.
func validAuthorizedKey(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "\r\n") {
		return false
	}
	return authKeyRe.MatchString(s)
}

// buildManagedKeysBlock renders the marked block for the given keys (no
// trailing newline — applyManagedKeys adds the file's line endings).
func buildManagedKeysBlock(keys []string) string {
	var b strings.Builder
	b.WriteString(authKeysBegin + "\n")
	b.WriteString("# Managed by mpd-virt — set \"authorized_keys\" in the VM's vm.json on the host, then `mpd-virt start <NNN>`. Do not edit here.\n")
	for _, k := range keys {
		b.WriteString(k + "\n")
	}
	b.WriteString(authKeysEnd)
	return b.String()
}

// stripManagedBlock returns s with the mpd-virt managed block removed (begin
// through end, inclusive). A begin with no end removes to EOF, so a truncated
// block self-heals. Everything outside the block is preserved verbatim.
func stripManagedBlock(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	inBlock := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if !inBlock && t == authKeysBegin {
			inBlock = true
			continue
		}
		if inBlock {
			if t == authKeysEnd {
				inBlock = false
			}
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// applyManagedKeys computes the new authorized_keys content from the existing
// file and the desired managed keys. It is idempotent — feeding its own output
// back with the same keys returns it unchanged — and it leaves the file byte
// for byte alone when there is nothing to do (no keys and no existing block),
// so a VM that never used this is never rewritten.
func applyManagedKeys(existing string, keys []string) string {
	if len(keys) == 0 && !strings.Contains(existing, authKeysBegin) {
		return existing
	}
	base := strings.TrimRight(stripManagedBlock(existing), "\n")
	if len(keys) == 0 {
		if base == "" {
			return ""
		}
		return base + "\n"
	}
	block := buildManagedKeysBlock(keys)
	if base == "" {
		return block + "\n"
	}
	// A blank line separates the developer's own keys from the managed block;
	// stripManagedBlock + TrimRight above discards it before each rebuild, so
	// it never accumulates.
	return base + "\n\n" + block + "\n"
}

// pushAuthorizedKeys converges the VM's ~/.ssh/authorized_keys managed block on
// keys, reporting whether it changed anything. The new content is computed here
// (not with remote sed) so the transformation is exact and testable; only a
// genuine change is written back.
func pushAuthorizedKeys(ctx context.Context, t host.Target, keys []string) (bool, error) {
	// `|| true` so a missing file reads as empty rather than erroring — absent
	// and "no managed block yet" are the same case.
	r, err := t.Run(ctx, "cat ~/.ssh/authorized_keys 2>/dev/null || true")
	if err != nil {
		return false, err
	}
	// Capture already trimmed the trailing newline(s) off the file's content,
	// so compare newline-insensitively — otherwise the generated trailing
	// newline would read as a change and rewrite the file on every start.
	existing := r.Stdout
	updated := applyManagedKeys(existing, keys)
	if strings.TrimRight(updated, "\n") == strings.TrimRight(existing, "\n") {
		return false, nil
	}
	if err := t.WriteRemote(ctx, updated, authKeysStaged, "0600"); err != nil {
		return false, err
	}
	// Installed as the dev user into their own ~/.ssh (0700 dir, 0600 file — the
	// modes sshd insists on); ~/.ssh is created if a bare VM lacks it.
	cmd := "install -d -m 700 ~/.ssh && install -m 600 " + authKeysStaged + " ~/.ssh/authorized_keys && rm -f " + authKeysStaged
	if res, err := t.Run(ctx, cmd); err != nil {
		return false, err
	} else if res.Failed() {
		return false, fmt.Errorf("installing authorized_keys: %s", strings.TrimSpace(res.Stderr))
	}
	return true, nil
}

// syncAuthorizedKeys is the best-effort wrapper the lifecycle verbs use. Like
// assets and the env files, these are the developer's own material: a failed
// push warns and never fails a start or update. Malformed entries are skipped
// with a warning rather than corrupting the file — sshd would ignore them
// anyway, and skipping keeps the managed block clean.
func syncAuthorizedKeys(ctx context.Context, t host.Target, e registry.Entry, idPad string) {
	var valid []string
	for _, k := range e.AuthorizedKeys {
		if strings.TrimSpace(k) == "" {
			continue
		}
		if !validAuthorizedKey(k) {
			fmt.Printf("  ⚠ skipping malformed authorized key: %q\n", truncKey(k))
			continue
		}
		valid = append(valid, strings.TrimSpace(k))
	}
	changed, err := pushAuthorizedKeys(ctx, t, valid)
	if err != nil {
		fmt.Printf("  ⚠ authorized_keys push failed: %v\n    retry with: mpd-virt start %s\n", err, idPad)
		return
	}
	if changed {
		pass(fmt.Sprintf("authorized_keys synced (%d managed) → ~/.ssh/authorized_keys", len(valid)))
	}
}

// truncKey shortens a key for a warning line — the type and a snippet, never
// the whole blob.
func truncKey(k string) string {
	k = strings.TrimSpace(strings.ReplaceAll(k, "\n", "⏎"))
	if len(k) > 40 {
		return k[:40] + "…"
	}
	return k
}
