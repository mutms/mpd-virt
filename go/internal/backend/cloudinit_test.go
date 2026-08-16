package backend

import (
	"crypto/sha512"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The archive becomes every created VM's operating system; a digest
// mismatch is fatal and names both sums.
func TestVerifySHA512(t *testing.T) {
	p := filepath.Join(t.TempDir(), "archive")
	body := []byte("not really a debian image")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha512.Sum512(body)
	want := hex.EncodeToString(sum[:])

	if err := verifySHA512(p, want); err != nil {
		t.Errorf("matching digest refused: %v", err)
	}
	err := verifySHA512(p, strings.Repeat("0", 128))
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("mismatched digest should fail with a mismatch error, got %v", err)
	}
}

// The pinned constant must stay a plausible SHA-512 — a truncated paste
// would otherwise refuse every download with a confusing mismatch.
func TestPinnedArchiveDigestShape(t *testing.T) {
	if len(cloudArchiveSHA512) != 128 {
		t.Fatalf("cloudArchiveSHA512 is %d hex chars, want 128", len(cloudArchiveSHA512))
	}
	if _, err := hex.DecodeString(cloudArchiveSHA512); err != nil {
		t.Fatalf("cloudArchiveSHA512 is not hex: %v", err)
	}
}
