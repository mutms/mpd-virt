package backend

// proxmox.go drives Proxmox VMs through the Proxmox REST API — power state,
// start, and graceful shutdown, nothing else. Provisioning stays manual by
// design (docs/PROXMOX.md). The VM number is the Proxmox VMID, and the VM's
// LAN address is NETWORK with the last octet replaced by that number — the
// cloud image runs no guest agent, so the address is assigned statically in
// cloud-init to match this rule rather than queried.

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// proxmoxConfig is conf/backends/proxmox.env: where the API lives, which LAN
// the VMs sit on, and the API token exactly as the Proxmox UI shows it.
type proxmoxConfig struct {
	apiURL      string // https://<host>:8006/api2/json/
	network     string // e.g. 10.1.10.0 — VM NNN lives at .NNN
	tokenID     string // <user>@<realm>!<token name>
	tokenSecret string // the secret uuid
}

// loadProxmoxConfig reads and validates the env file. Every key is required;
// a missing file means the backend was never configured, and the error points
// at the doc that walks through creating the token.
func loadProxmoxConfig() (proxmoxConfig, error) {
	path := paths.ProxmoxEnv()
	f, err := os.Open(path)
	if err != nil {
		return proxmoxConfig{}, fmt.Errorf("proxmox backend is not configured (%s) — see docs/PROXMOX.md", path)
	}
	defer f.Close()

	vals := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			vals[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	cfg := proxmoxConfig{
		apiURL:      vals["API_URL"],
		network:     vals["NETWORK"],
		tokenID:     vals["TOKEN_ID"],
		tokenSecret: vals["TOKEN_SECRET"],
	}
	for k, v := range map[string]string{
		"API_URL": cfg.apiURL, "NETWORK": cfg.network,
		"TOKEN_ID": cfg.tokenID, "TOKEN_SECRET": cfg.tokenSecret,
	} {
		if v == "" {
			return proxmoxConfig{}, fmt.Errorf("%s: %s is missing — see docs/PROXMOX.md", path, k)
		}
	}
	if !strings.HasSuffix(cfg.apiURL, "/") {
		cfg.apiURL += "/"
	}
	return cfg, nil
}

// proxmoxClient is one authenticated hold of the API. TLS trusts the system
// roots plus the mpd root CA, so a Proxmox host serving an mpd-CA-signed
// certificate verifies without any extra configuration.
type proxmoxClient struct {
	cfg  proxmoxConfig
	http *http.Client
}

func newProxmoxClient(cfg proxmoxConfig) *proxmoxClient {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if pem, err := os.ReadFile(filepath.Join(paths.CARoot(), "rootCA.pem")); err == nil {
		pool.AppendCertsFromPEM(pem)
	}
	return &proxmoxClient{cfg: cfg, http: &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}}
}

// call performs one API request and decodes the response's "data" envelope
// into out (nil to discard). Every Proxmox reply is {"data": ...}.
func (c *proxmoxClient) call(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.apiURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.cfg.tokenID+"="+c.cfg.tokenSecret)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("proxmox API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("proxmox API %s %s: %s", method, path, resp.Status)
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("proxmox API %s: decoding reply: %w", path, err)
	}
	return json.Unmarshal(envelope.Data, out)
}

// vmResource is one row of cluster/resources — enough to know where a VM
// lives and what state it is in, which is all the backend ever asks.
type vmResource struct {
	VMID   int    `json:"vmid"`
	Node   string `json:"node"`
	Status string `json:"status"`
}

// findVM locates the box in the cluster listing by its number (the VM number
// IS the Proxmox VMID — docs/PROXMOX.md). The token only sees VMs it was
// granted, so an absent one means "not created, or not granted to the token".
func (c *proxmoxClient) findVM(ctx context.Context, id vmid.ID) (vmResource, error) {
	var vms []vmResource
	if err := c.call(ctx, http.MethodGet, "cluster/resources?type=vm", &vms); err != nil {
		return vmResource{}, err
	}
	for _, vm := range vms {
		if vm.VMID == int(id) {
			return vm, nil
		}
	}
	return vmResource{}, fmt.Errorf("proxmox does not list VM %d — not created, or the API token has no permission on it", int(id))
}

// proxmoxState reports the box's power state, stUnknown on any failure
// (backend unconfigured, API unreachable) so the power path falls back to
// issuing its verb blind — the same contract as the other backends' probes.
func proxmoxState(ctx context.Context, id vmid.ID) vmState {
	cfg, err := loadProxmoxConfig()
	if err != nil {
		return stUnknown
	}
	vm, err := newProxmoxClient(cfg).findVM(ctx, id)
	if err != nil {
		return stUnknown
	}
	return normalizeState(vm.Status)
}

// proxmoxPower issues one power verb. The package-wide "stop" is sent as the
// API's graceful "shutdown" (ACPI). Failures are reported to out and
// swallowed, like the other backends: whether the box actually moved is
// decided by Start's reachability wait, not here.
func proxmoxPower(ctx context.Context, out io.Writer, id vmid.ID, verb string) bool {
	if verb == "stop" {
		verb = "shutdown"
	}
	cfg, err := loadProxmoxConfig()
	if err != nil {
		fmt.Fprintf(out, "    … %v\n", err)
		return false
	}
	c := newProxmoxClient(cfg)
	vm, err := c.findVM(ctx, id)
	if err != nil {
		fmt.Fprintf(out, "    … %v\n", err)
		return false
	}
	fmt.Fprintf(out, "  ▶ proxmox %s %s (node %s)\n", verb, id.Name(), vm.Node)
	path := fmt.Sprintf("nodes/%s/qemu/%d/status/%s", url.PathEscape(vm.Node), int(id), verb)
	if err := c.call(ctx, http.MethodPost, path, nil); err != nil {
		fmt.Fprintf(out, "    … %v (continuing)\n", err)
		return false
	}
	return true
}

// overlayRange is the in-VM container/overlay subnet family (10.163.<NNN>.0/24
// for every box). A box's guest agent reports addresses on it — the overlay
// gateway .1, container bridges — that are not the box's LAN address and are
// not routable from the Mac without the tunnel, so they are filtered out of
// address discovery.
var overlayRange = netip.MustParsePrefix("10.163.0.0/16")

// proxmoxAgentIPs asks the box's qemu-guest-agent, through the Proxmox API, for
// its current LAN addresses. Adopted boxes run the agent (the prep script and
// bootstrap install it), so this is the authoritative address of a *running*
// box — and unlike proxmoxDerivedIP it finds a box that sits off the cloud-init
// convention on a non-standard static lease. Empty on any failure (agent not up
// yet, API unreachable, box off), so locate falls back to the derived IP.
//
// Loopback, link-local and the overlay range are filtered out; only real LAN
// candidates are returned, and locate's ssh probe has the final say among them.
func proxmoxAgentIPs(ctx context.Context, id vmid.ID) []string {
	cfg, err := loadProxmoxConfig()
	if err != nil {
		return nil
	}
	c := newProxmoxClient(cfg)
	vm, err := c.findVM(ctx, id)
	if err != nil {
		return nil
	}
	var data struct {
		Result []struct {
			Name        string `json:"name"`
			IPAddresses []struct {
				Type    string `json:"ip-address-type"`
				Address string `json:"ip-address"`
			} `json:"ip-addresses"`
		} `json:"result"`
	}
	path := fmt.Sprintf("nodes/%s/qemu/%d/agent/network-get-interfaces", url.PathEscape(vm.Node), int(id))
	if err := c.call(ctx, http.MethodGet, path, &data); err != nil {
		return nil
	}
	var ips []string
	for _, iface := range data.Result {
		if iface.Name == "lo" {
			continue
		}
		for _, a := range iface.IPAddresses {
			if a.Type != "ipv4" {
				continue
			}
			addr, err := netip.ParseAddr(a.Address)
			if err != nil || !addr.Is4() {
				continue
			}
			if addr.IsLoopback() || addr.IsLinkLocalUnicast() || overlayRange.Contains(addr) {
				continue
			}
			ips = append(ips, addr.String())
		}
	}
	return ips
}

// proxmoxDerivedIP is the box's LAN address by convention: NETWORK with its
// last octet replaced by the VM number (10.1.10.0 + 150 → 10.1.10.150). Used as
// the fallback when the guest agent cannot be reached (see proxmoxAgentIPs).
// locate's ssh probe has the final word, as for every candidate address.
func proxmoxDerivedIP(id vmid.ID) string {
	cfg, err := loadProxmoxConfig()
	if err != nil {
		return ""
	}
	addr, err := netip.ParseAddr(cfg.network)
	if err != nil || !addr.Is4() {
		return ""
	}
	b := addr.As4()
	b[3] = byte(int(id))
	return netip.AddrFrom4(b).String()
}
