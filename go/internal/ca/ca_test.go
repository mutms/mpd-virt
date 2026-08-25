package ca

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"testing"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

func TestGenerateAndConstrain(t *testing.T) {
	t.Setenv("MPD_VIRT_TEST_ROOT", t.TempDir())

	id, _ := vmid.Parse("135")
	if err := LoadOrGenerateVM(id); err != nil {
		t.Fatalf("LoadOrGenerateVM: %v", err)
	}

	root := parseCert(t, RootCertPath())
	vm := parseCert(t, VMCertPath(id))

	// Root: a CA, name-constrained to the whole mpd.test tree, no pathlen
	// cap (so it can sign the intermediate).
	if !root.IsCA {
		t.Error("root is not a CA")
	}
	if got := root.PermittedDNSDomains; len(got) != 1 || got[0] != "mpd.test" {
		t.Errorf("root PermittedDNSDomains = %v, want [mpd.test]", got)
	}

	// VM intermediate: a CA, pathlen:0, constrained to 135.mpd.test alone.
	if !vm.IsCA {
		t.Error("vm CA is not a CA")
	}
	if !vm.MaxPathLenZero {
		t.Error("vm CA is not pathlen:0")
	}
	if got := vm.PermittedDNSDomains; len(got) != 1 || got[0] != "135.mpd.test" {
		t.Errorf("vm PermittedDNSDomains = %v, want [135.mpd.test]", got)
	}

	// The intermediate must actually be signed by the root.
	if err := vm.CheckSignatureFrom(root); err != nil {
		t.Errorf("vm CA not signed by root: %v", err)
	}

	// Idempotent: a second call keeps the same cert.
	before, _ := os.ReadFile(VMCertPath(id))
	if err := LoadOrGenerateVM(id); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(VMCertPath(id))
	if string(before) != string(after) {
		t.Error("LoadOrGenerateVM not idempotent — cert changed on second call")
	}
}

func parseCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		t.Fatalf("%s is not PEM", path)
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
