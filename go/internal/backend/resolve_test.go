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

// stub replaces the two effects ResolveIP has on the world for one test: what
// a name resolves to (dns), and which addresses answer on ssh (live). Both are
// restored on cleanup.
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
// with no boxes on file and cannot touch the developer's real ~/.mpd-virt.
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

// A name that resolves to a live box wins immediately — no registry needed.
func TestResolveIPName(t *testing.T) {
	isolateRegistry(t)
	stub(t, []string{"192.168.1.143"}, "192.168.1.143")
	got, err := ResolveIP(context.Background(), mustID(t, "139"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.168.1.143" {
		t.Errorf("ResolveIP = %q, want 192.168.1.143 (resolved by name)", got)
	}
}

// A name that resolves but does not answer ssh (a stale record) is skipped for
// the last IP on file — the Proxmox / re-takeover path.
func TestResolveIPFallsBackToLastIP(t *testing.T) {
	isolateRegistry(t)
	id := mustID(t, "139")
	seedLastIP(t, id, "192.168.1.143")
	stub(t, []string{"10.9.9.9"}, "192.168.1.143") // dns record dead, registry IP live
	got, err := ResolveIP(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.168.1.143" {
		t.Errorf("ResolveIP = %q, want the last IP 192.168.1.143", got)
	}
}

// Among several resolved addresses, the one answering ssh is chosen.
func TestResolveIPPicksLiveAddress(t *testing.T) {
	isolateRegistry(t)
	stub(t, []string{"10.0.0.5", "192.168.1.143"}, "192.168.1.143")
	got, err := ResolveIP(context.Background(), mustID(t, "139"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.168.1.143" {
		t.Errorf("ResolveIP = %q, want 192.168.1.143 (the live one)", got)
	}
}

// No name and nothing on file: fail telling the user to pass the IP.
func TestResolveIPNoCandidates(t *testing.T) {
	isolateRegistry(t)
	stub(t, nil) // nothing resolves, nothing live
	_, err := ResolveIP(context.Background(), mustID(t, "005"))
	if err == nil {
		t.Fatal("want an error when nothing resolves and no IP is on file")
	}
	if !strings.Contains(err.Error(), "takeover 005 <IP>") {
		t.Errorf("error should tell the user to pass an IP, got: %v", err)
	}
}

// Candidates exist but none answer ssh: the error names them and still points
// to the explicit-IP escape hatch.
func TestResolveIPAllDead(t *testing.T) {
	isolateRegistry(t)
	id := mustID(t, "139")
	seedLastIP(t, id, "192.168.1.99")
	stub(t, []string{"10.0.0.5"}) // both a dns record and a registry IP, neither live
	_, err := ResolveIP(context.Background(), id)
	if err == nil {
		t.Fatal("want an error when no candidate answers ssh")
	}
	for _, want := range []string{"10.0.0.5", "192.168.1.99", "takeover 139 <IP>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err.Error(), want)
		}
	}
}

// The same IP arriving from both DNS and the registry is probed once, not
// twice — the dedup keeps the candidate list honest.
func TestResolveIPDedupes(t *testing.T) {
	isolateRegistry(t)
	id := mustID(t, "139")
	seedLastIP(t, id, "192.168.1.143")
	probes := 0
	origResolve, origReach := resolveHost, sshReachable
	resolveHost = func(context.Context, string) []string { return []string{"192.168.1.143"} }
	sshReachable = func(context.Context, string) bool { probes++; return true }
	t.Cleanup(func() { resolveHost, sshReachable = origResolve, origReach })

	got, err := ResolveIP(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.168.1.143" {
		t.Errorf("ResolveIP = %q, want 192.168.1.143", got)
	}
	if probes != 1 {
		t.Errorf("a deduped IP should be probed once, got %d probes", probes)
	}
}
