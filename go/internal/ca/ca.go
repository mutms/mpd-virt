// Package ca is mpd-virt's local certificate authority for *.mpd.test.
//
// One root CA per Mac, generated on first adopt, persisted under
// ~/.mpd-virt/conf/caroot/. It is name-constrained to the mpd.test DNS
// tree, so even trusted in the System Keychain it can only ever sign
// certs for *.mpd.test — that constraint is what makes trusting it safe.
//
//	mpd Root CA                         (key: this Mac, and only this Mac)
//	└── mpd VM <NNN> CA                 (key: pushed into the box)
//	      permitted DNS:<NNN>.mpd.test
//	      └── <NNN>.mpd.test, …         signed inside the box
//
// The root's private key NEVER leaves the Mac. Each box instead gets its
// own intermediate under ~/.mpd-virt/<NNN>/ca/, signed by the root and
// constrained to that box's zone alone — so a compromised box can forge
// *.<NNN>.mpd.test and nothing else. Ported from CA.swift, but using
// crypto/x509 rather than shelling to openssl: name constraints are
// first-class here, so there is no temp conf file and nothing to parse
// back out of openssl's text output.
package ca

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

const (
	rootCommonName = "mpd Root CA"
	rootValidDays  = 365 // macOS caps user-root lifetimes; annual rotation.
	vmMaxDays      = 397 // leaf ceiling; nothing may outlive its issuer.
	keyBits        = 4096
)

// RootCertPath is the root CA's public cert, pushed to every box and
// trusted in the System Keychain.
func RootCertPath() string { return filepath.Join(paths.CARoot(), "rootCA.pem") }
func rootKeyPath() string  { return filepath.Join(paths.CARoot(), "rootCA-key.pem") }

func vmCADir(id vmid.ID) string    { return filepath.Join(paths.VMDir(id), "ca") }
func VMCertPath(id vmid.ID) string { return filepath.Join(vmCADir(id), "vmCA.pem") }
func VMKeyPath(id vmid.ID) string  { return filepath.Join(vmCADir(id), "vmCA-key.pem") }

// LoadOrGenerateRoot ensures the root CA exists, generating it on first
// use. Idempotent.
func LoadOrGenerateRoot() error {
	if fileExists(RootCertPath()) && fileExists(rootKeyPath()) {
		return nil
	}
	key, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return err
	}
	tmpl, err := baseTemplate(rootCommonName, rootValidDays)
	if err != nil {
		return err
	}
	tmpl.Subject.CommonName = rootCommonName
	// Unconstrained pathlen: the root must be able to sign the per-VM
	// intermediates. name-constrained to the whole mpd.test tree.
	tmpl.PermittedDNSDomainsCritical = true
	tmpl.PermittedDNSDomains = []string{"mpd.test"}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	if err := writePEM(RootCertPath(), "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	return writeKeyPEM(rootKeyPath(), key)
}

// LoadOrGenerateVM ensures the per-box intermediate exists, signed by the
// root and name-constrained to <NNN>.mpd.test. Idempotent.
func LoadOrGenerateVM(id vmid.ID) error {
	if fileExists(VMCertPath(id)) && fileExists(VMKeyPath(id)) {
		return nil
	}
	if err := LoadOrGenerateRoot(); err != nil {
		return err
	}
	rootCert, rootKey, err := loadRoot()
	if err != nil {
		return err
	}

	// Nothing outlives its issuer: cap the intermediate to whatever the
	// root has left, and refuse if the root has already expired.
	rootDaysLeft := int(time.Until(rootCert.NotAfter).Hours() / 24)
	if rootDaysLeft <= 0 {
		return fmt.Errorf("mpd root CA at %s has expired — regenerate and re-trust it", RootCertPath())
	}
	days := min(vmMaxDays, rootDaysLeft)

	if err := os.MkdirAll(vmCADir(id), 0o700); err != nil {
		return err
	}
	key, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return err
	}
	tmpl, err := baseTemplate("mpd VM "+id.Pad()+" CA", days)
	if err != nil {
		return err
	}
	// pathlen:0 — this CA signs leaves and never another CA.
	tmpl.MaxPathLen = 0
	tmpl.MaxPathLenZero = true
	tmpl.PermittedDNSDomainsCritical = true
	tmpl.PermittedDNSDomains = []string{id.Zone()}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, rootCert, &key.PublicKey, rootKey)
	if err != nil {
		return err
	}
	if err := writePEM(VMCertPath(id), "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	return writeKeyPEM(VMKeyPath(id), key)
}

// baseTemplate is the common shape of a CA certificate: a random serial,
// the mpd-virt DN, CA:TRUE, keyCertSign+cRLSign, valid for days.
func baseTemplate(cn string, days int) (*x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         cn,
			OrganizationalUnit: []string{"mpd-virt"},
			Organization:       []string{"mpd local development"},
		},
		NotBefore:             now.Add(-time.Hour), // tolerate clock skew
		NotAfter:              now.AddDate(0, 0, days),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}, nil
}

func loadRoot() (*x509.Certificate, *rsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(RootCertPath())
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, nil, fmt.Errorf("root CA cert at %s is not valid PEM", RootCertPath())
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(rootKeyPath())
	if err != nil {
		return nil, nil, err
	}
	kblock, _ := pem.Decode(keyPEM)
	if kblock == nil {
		return nil, nil, fmt.Errorf("root CA key at %s is not valid PEM", rootKeyPath())
	}
	k, err := x509.ParsePKCS8PrivateKey(kblock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	rsaKey, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("root CA key is not RSA")
	}
	return cert, rsaKey, nil
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	return os.WriteFile(path, body, mode)
}

// writeKeyPEM writes an RSA private key as PKCS#8 ("PRIVATE KEY"), mode
// 0600. This key is meant to travel into the box — that is the point.
func writeKeyPEM(path string, key *rsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return writePEM(path, "PRIVATE KEY", der, 0o600)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
