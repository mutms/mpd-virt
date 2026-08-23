// Package ca is mpd-virt's local certificate authority for *.mpd.test.
//
// One root CA per Mac, generated on first adopt, persisted under
// ~/.mpd-virt/conf/caroot/. It is name-constrained to the mpd.test DNS
// tree, so even trusted in the System Keychain it can only ever sign
// certs for *.mpd.test — that constraint is what makes trusting it safe.
//
//	mpd Root CA                         (key: this Mac, and only this Mac)
//	└── mpd VM <NNN> CA                 (key: pushed into the VM)
//	      permitted DNS:<NNN>.mpd.test
//	      └── <NNN>.mpd.test, …         signed inside the VM
//
// The root's private key NEVER leaves the Mac. Each VM instead gets its
// own intermediate under ~/.mpd-virt/<NNN>/ca/, signed by the root and
// constrained to that VM's zone alone — so a compromised VM can forge
// *.<NNN>.mpd.test and nothing else. This implementation uses
// crypto/x509 rather than shelling to openssl: name constraints are
// first-class here, so there is no temp conf file and nothing to parse
// back out of openssl's text output.
package ca

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

const (
	RootCommonName = "mpd Root CA"
	rootValidDays  = 365 // macOS caps user-root lifetimes; annual rotation.
	vmMaxDays      = 397 // leaf ceiling; nothing may outlive its issuer.
	leafMaxDays    = 397 // LAN service leaf ceiling; matches the macOS 398-day cap.
	keyBits        = 4096
	leafKeyBits    = 2048 // leaves live on other machines; 2048 is plenty and cheaper.
)

// RootCertPath is the root CA's public cert, pushed to every VM and
// trusted in the System Keychain.
func RootCertPath() string { return filepath.Join(paths.CARoot(), "rootCA.pem") }

// RootFingerprintSHA1 is the SHA-1 of the root CA's DER as uppercase hex with
// no separators — the exact form `security find-certificate -Z` prints, so the
// two can be compared to tell the live root apart from a stale "mpd Root CA"
// left in the Keychain by an earlier, regenerated root.
func RootFingerprintSHA1() (string, error) {
	pemBytes, err := os.ReadFile(RootCertPath())
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("root CA cert at %s is not valid PEM", RootCertPath())
	}
	sum := sha1.Sum(block.Bytes)
	return strings.ToUpper(hex.EncodeToString(sum[:])), nil
}
func rootKeyPath() string { return filepath.Join(paths.CARoot(), "rootCA-key.pem") }

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
	tmpl, err := baseTemplate(RootCommonName, rootValidDays)
	if err != nil {
		return err
	}
	tmpl.Subject.CommonName = RootCommonName
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

// LoadOrGenerateVM ensures the per-VM intermediate exists, signed by the
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
	tmpl, err := baseTemplate("mpd VM "+id.String()+" CA", days)
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

// IssueLeaf issues a server-auth leaf for sans (DNS names only), signed
// directly by the root CA on this Mac and written to certPath/keyPath. The
// first SAN becomes the CN. Used for LAN service hosts (forge.mpd.test, …):
// the signing key already lives here, so a per-host intermediate would only
// add a chain hop and another file to install without protecting anything.
//
// DNS SANs only, deliberately: the root name-constrains dNSName to mpd.test,
// but under RFC 5280 an iPAddress SAN sits outside that constraint — so these
// hosts are reached by name, never by a bare-IP cert. Nothing outlives its
// issuer: the leaf is capped to whatever the root has left.
func IssueLeaf(sans []string, certPath, keyPath string) error {
	if len(sans) == 0 {
		return fmt.Errorf("IssueLeaf: no SANs given")
	}
	if err := LoadOrGenerateRoot(); err != nil {
		return err
	}
	rootCert, rootKey, err := loadRoot()
	if err != nil {
		return err
	}
	rootDaysLeft := int(time.Until(rootCert.NotAfter).Hours() / 24)
	if rootDaysLeft <= 0 {
		return fmt.Errorf("mpd root CA at %s has expired — regenerate and re-trust it before issuing certificates", RootCertPath())
	}
	days := min(leafMaxDays, rootDaysLeft)

	key, err := rsa.GenerateKey(rand.Reader, leafKeyBits)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: sans[0]},
		DNSNames:     sans,
		NotBefore:    now.Add(-time.Hour), // tolerate clock skew
		NotAfter:     now.AddDate(0, 0, days),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// A leaf, not a CA — BasicConstraints present and CA:FALSE.
		IsCA:                  false,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, rootCert, &key.PublicKey, rootKey)
	if err != nil {
		return err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	return writeKeyPEM(keyPath, key)
}

// DaysUntilExpiry parses the certificate at path and returns whole days
// until it expires — negative once past. Used to decide whether a leaf is
// worth re-issuing.
func DaysUntilExpiry(path string) (int, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return 0, fmt.Errorf("certificate at %s is not valid PEM", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return 0, err
	}
	return int(time.Until(cert.NotAfter).Hours() / 24), nil
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
// 0600. This key is meant to travel into the VM — that is the point.
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
