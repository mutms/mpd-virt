package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

func TestTestSiteProbesCheckKnownBodies(t *testing.T) {
	probes := testSiteProbes(vmid.ID(190))
	if len(probes) != 2 {
		t.Fatalf("got %d probes, want 2", len(probes))
	}
	if probes[0].url != "https://exttests.190.mpd.test/test.html" || probes[1].url != "http://exttests.190.mpd.test/" {
		t.Fatalf("unexpected urls: %s, %s", probes[0].url, probes[1].url)
	}
	if probes[0].check([]byte("not the file")) {
		t.Error("test.html check accepted the wrong body")
	}
	if !probes[1].check([]byte("<html><head>" + testIndexTitle + "</head>")) {
		t.Error("start page check rejected the real title")
	}
	if probes[1].check([]byte("<title>Sorry, you have been blocked</title>")) {
		t.Error("start page check accepted another page")
	}
}

func TestFetchAndCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("hello " + testIndexTitle))
		case "/other":
			_, _ = w.Write([]byte("something else"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	check := func(b []byte) bool { return strings.Contains(string(b), testIndexTitle) }
	client := srv.Client()
	ctx := context.Background()

	if err := fetchAndCheck(ctx, client, siteProbe{url: srv.URL + "/ok", check: check}); err != nil {
		t.Errorf("good page: %v", err)
	}
	if err := fetchAndCheck(ctx, client, siteProbe{url: srv.URL + "/other", check: check}); err == nil || !strings.Contains(err.Error(), "unexpected content") {
		t.Errorf("wrong body: got %v", err)
	}
	if err := fetchAndCheck(ctx, client, siteProbe{url: srv.URL + "/missing", check: check}); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("missing page: got %v", err)
	}
}

func TestClassifyProbeError(t *testing.T) {
	dns := classifyProbeError(&net.DNSError{Err: "no such host", Name: "x.mpd.test", IsNotFound: true})
	if !strings.HasPrefix(dns.Error(), "DNS:") {
		t.Errorf("dns error classified as %q", dns)
	}

	// A closed port on loopback gives a real ECONNREFUSED.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	_, err = client.Get("http://" + addr + "/")
	if got := classifyProbeError(err); !strings.HasPrefix(got.Error(), "connection refused") {
		t.Errorf("refused classified as %q", got)
	}

	tls := httptest.NewTLSServer(http.NotFoundHandler())
	defer tls.Close()
	_, err = http.Get(tls.URL)
	if got := classifyProbeError(err); !strings.Contains(got.Error(), "not trusted") {
		t.Errorf("untrusted CA classified as %q", got)
	}

	if got := classifyProbeError(errors.New("boom")); !strings.HasPrefix(got.Error(), "request failed") {
		t.Errorf("unknown error classified as %q", got)
	}
}
