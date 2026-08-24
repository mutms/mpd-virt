package backend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProxmoxEnv points MPD_VIRT_ROOT at a scratch root holding a
// proxmox.env aimed at apiURL — the same isolation trick the
// whole test suite uses to stay out of the developer's real ~/.mpd-virt.
func writeProxmoxEnv(t *testing.T, apiURL string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("MPD_VIRT_ROOT", root)
	env := "API_URL=" + apiURL + "\nNETWORK=10.1.10.0\nTOKEN_ID=mpd-virt@pam!test\nTOKEN_SECRET=sekret\n"
	if err := os.WriteFile(filepath.Join(root, "proxmox.env"), []byte(env), 0o600); err != nil {
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
		case r.URL.Path == "/nodes/kitchenbox/qemu/150/config":
			// The Notes field is the config "description"; markdown, and here
			// several lines, so the caller's first-line/trim job is exercised.
			_, _ = w.Write([]byte(`{"data":{"description":"# prod db\nsecond line"}}`))
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

// Notes come back raw from the config "description" field (multi-line markdown
// and all); tidying them for display is the caller's job, so proxmoxNotes
// returns the value verbatim. A VM the token cannot see is honestly empty,
// never an error — the same best-effort contract as proxmoxState.
func TestProxmoxNotes(t *testing.T) {
	var hits []string
	ts := fakeProxmox(t, "running", &hits)
	defer ts.Close()
	writeProxmoxEnv(t, ts.URL+"/")

	if got := proxmoxNotes(context.Background(), mustID(t, "150")); got != "# prod db\nsecond line" {
		t.Errorf("notes(150) = %q, want the raw description", got)
	}
	if got := proxmoxNotes(context.Background(), mustID(t, "152")); got != "" {
		t.Errorf("notes(152) = %q, want empty for a VM the token cannot see", got)
	}
	if got := Notes(context.Background(), mustID(t, "150"), Generic); got != "" {
		t.Errorf("Notes for a non-proxmox backend = %q, want empty (no API call)", got)
	}
}

// An unconfigured backend yields empty notes, never an error — a blank cell,
// like every other proxmox probe with no env file.
func TestProxmoxNotesUnconfigured(t *testing.T) {
	t.Setenv("MPD_VIRT_ROOT", t.TempDir()) // empty root — no proxmox.env
	if got := proxmoxNotes(context.Background(), mustID(t, "150")); got != "" {
		t.Errorf("notes = %q, want empty without configuration", got)
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
// This is the authoritative address that finds a VM off the derived convention.
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
	env := "API_URL=https://x:8006/api2/json/\nNETWORK=10.1.10.0\nTOKEN_ID=a@b!c\n" // no TOKEN_SECRET
	if err := os.WriteFile(filepath.Join(root, "proxmox.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadProxmoxConfig()
	if err == nil || !strings.Contains(err.Error(), "TOKEN_SECRET") {
		t.Errorf("err = %v, want mention of TOKEN_SECRET", err)
	}
}

func TestProxmoxIPConfig(t *testing.T) {
	line, ip, err := proxmoxIPConfig("10.1.10.0/16", "10.1.1.1", 154)
	if err != nil || line != "ip=10.1.10.154/16,gw=10.1.1.1" || ip != "10.1.10.154" {
		t.Fatalf("got %q %q, %v", line, ip, err)
	}
	if line, _, _ := proxmoxIPConfig("10.1.10.0", "10.1.10.1", 154); line != "ip=10.1.10.154/24,gw=10.1.10.1" {
		t.Errorf("no prefix should mean /24, got %q", line)
	}
	if _, _, err := proxmoxIPConfig("10.1.10.0/16", "", 154); err == nil {
		t.Error("missing GATEWAY must be an error")
	}
	if _, _, err := proxmoxIPConfig("nope", "10.1.1.1", 154); err == nil {
		t.Error("bad NETWORK must be an error")
	}
}

// The API half of create: clone the template under the new id, set
// cloud-init (derived IP from the template's prefix/gateway, user, key, no
// password), start. Each call is checked for the parameters Proxmox needs.
func TestProxmoxCloneFromTemplate(t *testing.T) {
	var hits []string
	forms := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		hits = append(hits, r.Method+" "+r.URL.Path)
		forms[r.Method+" "+r.URL.Path] = r.PostForm.Encode()
		switch {
		case r.URL.Path == "/cluster/resources":
			_, _ = w.Write([]byte(`{"data":[{"vmid":999,"node":"kitchenbox","status":"stopped"},{"vmid":150,"node":"kitchenbox","status":"running"}]}`))
		case r.URL.Path == "/nodes/kitchenbox/qemu/999/config":
			_, _ = w.Write([]byte(`{"data":{"name":"mpd-template","sshkeys":"ssh-ed25519%20TTTT%20template"}}`))
		case r.URL.Path == "/nodes/kitchenbox/qemu/999/clone", r.URL.Path == "/nodes/kitchenbox/qemu/154/status/start":
			_, _ = w.Write([]byte(`{"data":"UPID:kitchenbox:1:2:3:task:154:t:"}`))
		case strings.HasPrefix(r.URL.Path, "/nodes/kitchenbox/tasks/"):
			_, _ = w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
		case r.URL.Path == "/nodes/kitchenbox/qemu/154/config" && r.Method == http.MethodPut:
			_, _ = w.Write([]byte(`{"data":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	writeProxmoxEnv(t, srv.URL+"/")
	root := os.Getenv("MPD_VIRT_ROOT")
	env := filepath.Join(root, "proxmox.env")
	b, _ := os.ReadFile(env)
	_ = os.WriteFile(env, append(b, []byte("POOL=mpd-test\nGATEWAY=10.1.1.1\n")...), 0o600)

	cfg, err := loadProxmoxConfig()
	if err != nil {
		t.Fatal(err)
	}
	c := newProxmoxClient(cfg)
	ip, err := c.cloneFromTemplate(context.Background(), io.Discard, 154, CreateOpts{User: "skodak", PubKey: "ssh-ed25519 AAAA+x/y me@host"})
	if err != nil {
		t.Fatal(err)
	}
	if ip != "10.1.10.154" {
		t.Errorf("ip = %q, want 10.1.10.154", ip)
	}
	if got := forms["POST /nodes/kitchenbox/qemu/999/clone"]; got != "full=1&name=mpd-154&newid=154&pool=mpd-test" {
		t.Errorf("clone form = %q", got)
	}
	// sshkeys = the template's key plus ours, double-encoded: Proxmox's own
	// %20 form, then the transport's form encoding of the percent signs.
	want := "ciuser=skodak&delete=cipassword&ipconfig0=ip%3D10.1.10.154%2F24%2Cgw%3D10.1.1.1&sshkeys=ssh-ed25519%2520TTTT%2520template%250Assh-ed25519%2520AAAA%252Bx%252Fy%2520me%2540host"
	if got := forms["PUT /nodes/kitchenbox/qemu/154/config"]; got != want {
		t.Errorf("config form = %q\nwant %q", got, want)
	}
	if hits[len(hits)-2] != "POST /nodes/kitchenbox/qemu/154/status/start" {
		t.Errorf("expected start before the final task poll, got %v", hits)
	}

	// An id that already exists is refused before anything is cloned.
	if _, err := c.cloneFromTemplate(context.Background(), io.Discard, 150, CreateOpts{}); err == nil || !strings.Contains(err.Error(), "already has VM 150") {
		t.Errorf("expected refusal for an existing VM, got %v", err)
	}
}

func TestProxmoxAddKey(t *testing.T) {
	if got := proxmoxAddKey("ssh-ed25519%20A%20one%0Assh-ed25519%20B%20two", "ssh-ed25519 B two"); got != "ssh-ed25519 A one\nssh-ed25519 B two" {
		t.Errorf("present key duplicated: %q", got)
	}
	if got := proxmoxAddKey("", "ssh-ed25519 C three"); got != "ssh-ed25519 C three" {
		t.Errorf("empty template keys: %q", got)
	}
}

// The API half of remove --full: hard-stop a running VM, then destroy it
// with its disks. A VM the API no longer lists is already gone and must
// not be an error.
func TestProxmoxDestroyVM(t *testing.T) {
	var hits []string
	status := "running"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.URL.Path == "/cluster/resources":
			_, _ = w.Write([]byte(`{"data":[{"vmid":154,"node":"kitchenbox","status":"` + status + `"}]}`))
		case r.URL.Path == "/nodes/kitchenbox/qemu/154/status/stop":
			status = "stopped"
			_, _ = w.Write([]byte(`{"data":"UPID:kitchenbox:1:2:3:qmstop:154:t:"}`))
		case r.URL.Path == "/nodes/kitchenbox/qemu/154" && r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"data":"UPID:kitchenbox:1:2:3:qmdestroy:154:t:"}`))
		case strings.HasPrefix(r.URL.Path, "/nodes/kitchenbox/tasks/"):
			_, _ = w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	writeProxmoxEnv(t, srv.URL+"/")
	cfg, err := loadProxmoxConfig()
	if err != nil {
		t.Fatal(err)
	}
	c := newProxmoxClient(cfg)

	// Running: stopped hard, then destroyed.
	var out strings.Builder
	if err := c.destroyVM(context.Background(), &out, 154); err != nil {
		t.Fatalf("destroyVM: %v\n%s", err, out.String())
	}
	want := []string{
		"POST /nodes/kitchenbox/qemu/154/status/stop",
		"DELETE /nodes/kitchenbox/qemu/154?purge=1&destroy-unreferenced-disks=1",
	}
	for _, w := range want {
		found := false
		for _, h := range hits {
			if h == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing call %q in %q", w, hits)
		}
	}

	// Already gone: no DELETE, no error.
	hits = nil
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv2.Close()
	writeProxmoxEnv(t, srv2.URL+"/")
	cfg, _ = loadProxmoxConfig()
	out.Reset()
	if err := newProxmoxClient(cfg).destroyVM(context.Background(), &out, 154); err != nil {
		t.Fatalf("destroyVM on an absent VM: %v", err)
	}
	for _, h := range hits {
		if strings.HasPrefix(h, "DELETE") {
			t.Errorf("DELETE issued for a VM the API does not list: %q", hits)
		}
	}
	if !strings.Contains(out.String(), "nothing to delete") {
		t.Errorf("absent VM not reported: %q", out.String())
	}
}
