package cli

import (
	"context"
	"crypto/md5"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// testSiteProject is the project mpd --vm-setup creates on every VM. It
// serves moodle-exttests, so its bodies are known in advance.
const testSiteProject = "exttests"

// testHTMLMD5 is the md5 of test.html in moodle-exttests. Moodle's own
// filelib tests pin the same value.
const testHTMLMD5 = "47250a973d1b88d9445f94db4ef2c97a"

// testIndexTitle is in the start page mpd seeds into the exttests project.
const testIndexTitle = "<title>Moodle external test files</title>"

// siteProbe is one request with a known answer.
type siteProbe struct {
	url   string
	check func(body []byte) bool
	want  string
}

func testSiteProbes(id vmid.ID) []siteProbe {
	host := testSiteProject + "." + id.Zone()
	return []siteProbe{
		{
			url: "https://" + host + "/test.html",
			check: func(b []byte) bool {
				sum := md5.Sum(b)
				return hex.EncodeToString(sum[:]) == testHTMLMD5
			},
			want: "md5 " + testHTMLMD5,
		},
		{
			url:   "http://" + host + "/",
			check: func(b []byte) bool { return strings.Contains(string(b), testIndexTitle) },
			want:  "the exttests start page",
		},
	}
}

// verifyTestSite fetches known pages of the VM's exttests project from this
// host, through the system resolver and trust store. Informative only: it
// warns and never fails the verb.
func verifyTestSite(ctx context.Context, id vmid.ID) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		// A redirect would mean another server answered.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	failed := false
	for _, p := range testSiteProbes(id) {
		if err := runSiteProbe(ctx, client, p); err != nil {
			fmt.Printf("  ⚠ test site: %s — %v\n", p.url, err)
			failed = true
			continue
		}
		pass("test site: " + p.url)
	}
	if failed {
		fmt.Printf("    A VM set up before mpd shipped the %s project lacks it:\n"+
			"    ssh %s and run `mpd --vm-setup`.\n", testSiteProject, id.Name())
	}
}

// runSiteProbe performs one probe, retrying briefly while a fresh tunnel
// settles. The error names the layer that failed.
func runSiteProbe(ctx context.Context, client *http.Client, p siteProbe) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Second)
		}
		if err = fetchAndCheck(ctx, client, p); err == nil {
			return nil
		}
	}
	return err
}

func fetchAndCheck(ctx context.Context, client *http.Client, p siteProbe) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return classifyProbeError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("reading the answer failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from the VM — the exttests project is missing or not serving", resp.StatusCode)
	}
	if !p.check(body) {
		return fmt.Errorf("unexpected content, want %s — another server answered, or the files changed", p.want)
	}
	return nil
}

// classifyProbeError maps a transport error to the layer that broke.
func classifyProbeError(err error) error {
	var dnsErr *net.DNSError
	var unknownCA x509.UnknownAuthorityError
	var invalidCert x509.CertificateInvalidError
	var hostErr x509.HostnameError
	switch {
	case errors.As(err, &dnsErr):
		return fmt.Errorf("DNS: the name does not resolve (mpd-proxy split DNS, or the project has no record): %v", dnsErr)
	case errors.As(err, &unknownCA):
		return fmt.Errorf("TLS: the mpd root CA is not trusted on this host")
	case errors.As(err, &invalidCert), errors.As(err, &hostErr):
		return fmt.Errorf("TLS: the certificate is not valid for this name: %v", err)
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Errorf("connection refused: routed to the VM, but nothing listens (mpd-caddy)")
	case isTimeout(err):
		return fmt.Errorf("timed out: no route to the VM (WireGuard overlay down?)")
	}
	return fmt.Errorf("request failed: %v", err)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
