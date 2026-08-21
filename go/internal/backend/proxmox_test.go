package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProxmoxEnv points MPD_VIRT_ROOT at a scratch root holding a
// conf/backends/proxmox.env aimed at apiURL — the same isolation trick the
// whole test suite uses to stay out of the developer's real ~/.mpd-virt.
func writeProxmoxEnv(t *testing.T, apiURL string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("MPD_VIRT_ROOT", root)
	dir := filepath.Join(root, "conf", "backends")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := "API_URL=" + apiURL + "\nNETWORK=10.1.10.0\nTOKEN_ID=mpd-virt@pam!test\nTOKEN_SECRET=sekret\n"
	if err := os.WriteFile(filepath.Join(dir, "proxmox.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeProxmox serves the three API shapes the backend uses, recording every
// authenticated request path.
func fakeProxmox(t *testing.T, status string, hits *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken=mpd-virt@pam!test=sekret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		*hits = append(*hits, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/cluster/resources":
			_, _ = w.Write([]byte(`{"data":[{"vmid":150,"node":"kitchenbox","status":"` + status + `"},{"vmid":151,"node":"kitchenbox","status":"stopped"}]}`))
		case strings.HasPrefix(r.URL.Path, "/nodes/kitchenbox/qemu/150/status/"):
			_, _ = w.Write([]byte(`{"data":"UPID:kitchenbox:0:0:0:qmstart:150:t:"}`))
		case r.URL.Path == "/nodes/kitchenbox/qemu/150/agent/network-get-interfaces":
			// lo (loopback), ens18 (the real LAN address + link-local +
			// ipv6), and an overlay-range address on the container bridge —
			// only the ens18 LAN v4 should survive filtering.
			_, _ = w.Write([]byte(`{"data":{"result":[
				{"name":"lo","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"127.0.0.1"}]},
				{"name":"ens18","ip-addresses":[
					{"ip-address-type":"ipv4","ip-address":"10.1.1.54"},
					{"ip-address-type":"ipv4","ip-address":"169.254.3.4"},
					{"ip-address-type":"ipv6","ip-address":"fe80::1"}]},
				{"name":"mpd0","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"10.163.150.1"}]}
			]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// The state probe reads power state straight off the cluster listing; a VM
// the token cannot see (152) is honestly unknown, never an error.
func TestProxmoxState(t *testing.T) {
	var hits []string
	ts := fakeProxmox(t, "running", &hits)
	defer ts.Close()
	writeProxmoxEnv(t, ts.URL+"/")

	if st := proxmoxState(context.Background(), mustID(t, "150")); st != stRunning {
		t.Errorf("state(150) = %q, want running", st)
	}
	if st := proxmoxState(context.Background(), mustID(t, "151")); st != stStopped {
		t.Errorf("state(151) = %q, want stopped", st)
	}
	if st := proxmoxState(context.Background(), mustID(t, "152")); st != stUnknown {
		t.Errorf("state(152) = %q, want unknown", st)
	}
}

// Power verbs hit the node-qualified endpoint, and the package-wide "stop"
// travels as Proxmox's graceful "shutdown".
func TestProxmoxPower(t *testing.T) {
	var hits []string
	ts := fakeProxmox(t, "stopped", &hits)
	defer ts.Close()
	writeProxmoxEnv(t, ts.URL+"/")

	var out strings.Builder
	if !proxmoxPower(context.Background(), &out, mustID(t, "150"), "start") {
		t.Fatalf("start failed: %s", out.String())
	}
	if !proxmoxPower(context.Background(), &out, mustID(t, "150"), "stop") {
		t.Fatalf("stop failed: %s", out.String())
	}
	want := []string{"POST /nodes/kitchenbox/qemu/150/status/start", "POST /nodes/kitchenbox/qemu/150/status/shutdown"}
	var posts []string
	for _, h := range hits {
		if strings.HasPrefix(h, "POST") {
			posts = append(posts, h)
		}
	}
	if strings.Join(posts, ",") != strings.Join(want, ",") {
		t.Errorf("posted %v, want %v", posts, want)
	}
}

// The derived address is NETWORK's last octet replaced by the VM number.
func TestProxmoxDerivedIP(t *testing.T) {
	writeProxmoxEnv(t, "https://example.invalid/")
	if ip := proxmoxDerivedIP(mustID(t, "150")); ip != "10.1.10.150" {
		t.Errorf("derived IP for 150 = %q, want 10.1.10.150", ip)
	}
}

// The guest agent's real LAN address is returned, and only that: loopback,
// link-local, IPv6, and overlay-range (10.163.x) addresses are filtered out.
// This is the authoritative address that finds a box off the derived convention.
func TestProxmoxAgentIPs(t *testing.T) {
	var hits []string
	ts := fakeProxmox(t, "running", &hits)
	defer ts.Close()
	writeProxmoxEnv(t, ts.URL+"/")

	got := proxmoxAgentIPs(context.Background(), mustID(t, "150"))
	if len(got) != 1 || got[0] != "10.1.1.54" {
		t.Errorf("agent IPs = %v, want [10.1.1.54]", got)
	}
}

// A VM the token cannot see yields no agent address, never an error — locate
// then falls back to the derived convention, exactly as before this existed.
func TestProxmoxAgentIPsUnknownVM(t *testing.T) {
	var hits []string
	ts := fakeProxmox(t, "running", &hits)
	defer ts.Close()
	writeProxmoxEnv(t, ts.URL+"/")

	if got := proxmoxAgentIPs(context.Background(), mustID(t, "152")); got != nil {
		t.Errorf("agent IPs for an unlisted VM = %v, want nil", got)
	}
}

// An unconfigured backend (no env file) degrades exactly like an absent
// hypervisor CLI: unknown state, empty candidate IP, a reported power refusal.
func TestProxmoxUnconfigured(t *testing.T) {
	t.Setenv("MPD_VIRT_ROOT", t.TempDir()) // empty root — no proxmox.env

	if st := proxmoxState(context.Background(), mustID(t, "150")); st != stUnknown {
		t.Errorf("state = %q, want unknown", st)
	}
	if ip := proxmoxDerivedIP(mustID(t, "150")); ip != "" {
		t.Errorf("derived IP = %q, want empty", ip)
	}
	var out strings.Builder
	if proxmoxPower(context.Background(), &out, mustID(t, "150"), "start") {
		t.Error("power reported success without configuration")
	}
	if !strings.Contains(out.String(), "not configured") {
		t.Errorf("refusal does not explain itself: %q", out.String())
	}
}

// A partial env file names the missing key.
func TestProxmoxConfigMissingKey(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MPD_VIRT_ROOT", root)
	dir := filepath.Join(root, "conf", "backends")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	env := "API_URL=https://x:8006/api2/json/\nNETWORK=10.1.10.0\nTOKEN_ID=a@b!c\n" // no TOKEN_SECRET
	if err := os.WriteFile(filepath.Join(dir, "proxmox.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadProxmoxConfig()
	if err == nil || !strings.Contains(err.Error(), "TOKEN_SECRET") {
		t.Errorf("err = %v, want mention of TOKEN_SECRET", err)
	}
}
