package backend

// proxmox.go drives Proxmox VMs through the Proxmox REST API: power state,
// start, graceful shutdown, and `create` — a full clone of the mpd-template
// VM with the clone's hostname and static IP set through cloud-init
// (docs/PROXMOX.md). The VM number is the Proxmox VMID, and the VM's LAN
// address is NETWORK with the last octet replaced by that number.

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
	"strconv"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// proxmoxConfig is ~/.mpd-virt/proxmox.env: where the API lives, which LAN
// the VMs sit on, and the API token exactly as the Proxmox UI shows it.
type proxmoxConfig struct {
	apiURL      string // https://<host>:8006/api2/json/
	network     string // e.g. 10.1.10.0/16 — VM NNN lives at .NNN; the prefix (default /24) is the clone's netmask
	gateway     string // GATEWAY: the clone's default route (create only)
	tokenID     string // <user>@<realm>!<token name>
	tokenSecret string // the secret uuid
	template    string // TEMPLATE_VMID: the VM `create` clones (default 999)
	pool        string // POOL: the pool new VMs join (optional; the token's grant usually lives there)
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
		template:    vals["TEMPLATE_VMID"],
		pool:        vals["POOL"],
		gateway:     vals["GATEWAY"],
	}
	if cfg.template == "" {
		cfg.template = "999"
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
	return c.callForm(ctx, method, path, nil, out)
}

// callForm is call with a form body (POST/PUT parameters).
func (c *proxmoxClient) callForm(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.apiURL+path, body)
	if err != nil {
		return err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.cfg.tokenID+"="+c.cfg.tokenSecret)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("proxmox API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("proxmox API %s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
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

// findVM locates the VM in the cluster listing by its number (the VM number
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

// proxmoxState reports the VM's power state, stUnknown on any failure
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
// swallowed, like the other backends: whether the VM actually moved is
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
// for every VM). A VM's guest agent reports addresses on it — the overlay
// gateway .1, container bridges — that are not the VM's LAN address and are
// not routable from the Mac without the tunnel, so they are filtered out of
// address discovery.
var overlayRange = netip.MustParsePrefix("10.163.0.0/16")

// proxmoxAgentIPs asks the VM's qemu-guest-agent, through the Proxmox API, for
// its current LAN addresses. Adopted VMs run the agent (the prep script and
// bootstrap install it), so this is the authoritative address of a *running*
// VM — and unlike proxmoxDerivedIP it finds a VM that sits off the cloud-init
// convention on a non-standard static lease. Empty on any failure (agent not up
// yet, API unreachable, VM off), so locate falls back to the derived IP.
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

// proxmoxDerivedIP is the VM's LAN address by convention: NETWORK with its
// last octet replaced by the VM number (10.1.10.0 + 150 → 10.1.10.150). Used as
// the fallback when the guest agent cannot be reached (see proxmoxAgentIPs).
// locate's ssh probe has the final word, as for every candidate address.
func proxmoxDerivedIP(id vmid.ID) string {
	cfg, err := loadProxmoxConfig()
	if err != nil {
		return ""
	}
	return derivedIP(cfg.network, id)
}

// --- create -----------------------------------------------------------------

// proxmoxCreate makes a VM by cloning the template VM: full clone under the
// new VMID named mpd-<NNN>, cloud-init pointed at the derived static IP with
// the dev user and the Mac's key, then start and wait for the first boot's
// cloud-init to finish. The template never ran `mpd --vm-setup`, so its
// cloud-init modules are all still enabled: the clone's first boot sets the
// hostname, creates the user and generates fresh host keys. Returns the IP
// the VM is reachable at, ready for adoption.
func proxmoxCreate(ctx context.Context, out io.Writer, id vmid.ID, opts CreateOpts) (string, error) {
	cfg, err := loadProxmoxConfig()
	if err != nil {
		return "", err
	}
	c := newProxmoxClient(cfg)
	ip, err := c.cloneFromTemplate(ctx, out, id, opts)
	if err != nil {
		return "", err
	}

	t := host.Target{
		User: opts.User, Host: ip,
		KnownHostsFile: paths.EnsureKnownHosts(id), HostKeyAlias: id.Name(),
	}
	fmt.Fprintf(out, "  ▶ waiting for %s at %s (cloud-init first boot) …\n", id.Name(), ip)
	if !waitReachable(ctx, t, 300*time.Second) {
		return "", fmt.Errorf("%s did not come up at %s within 5 min — open its console in the Proxmox UI", id.Name(), ip)
	}
	if err := waitCloudInitDone(ctx, out, t, 300*time.Second); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "  ▶ Proxmox VM ready: %s\n", ip)
	return ip, nil
}

// cloneFromTemplate is the API half of proxmoxCreate: clone, configure
// cloud-init, start. Split off so it can be exercised against a fake API.
func (c *proxmoxClient) cloneFromTemplate(ctx context.Context, out io.Writer, id vmid.ID, opts CreateOpts) (string, error) {
	if vm, err := c.findVM(ctx, id); err == nil {
		return "", fmt.Errorf("proxmox already has VM %d (%s on %s) — pick another id, or `mpd-virt remove` and delete it first", int(id), vm.Status, vm.Node)
	}
	if _, err := strconv.Atoi(c.cfg.template); err != nil {
		return "", fmt.Errorf("TEMPLATE_VMID=%q in proxmox.env is not a VMID", c.cfg.template)
	}
	var template vmResource
	{
		var vms []vmResource
		if err := c.call(ctx, http.MethodGet, "cluster/resources?type=vm", &vms); err != nil {
			return "", err
		}
		for _, vm := range vms {
			if fmt.Sprint(vm.VMID) == c.cfg.template {
				template = vm
			}
		}
		if template.Node == "" {
			return "", fmt.Errorf("template VM %s is not visible to the token — create it (docs/PROXMOX.md) and grant the token on it", c.cfg.template)
		}
	}
	node := url.PathEscape(template.Node)

	ipconfig, ip, err := proxmoxIPConfig(c.cfg.network, c.cfg.gateway, id)
	if err != nil {
		return "", err
	}
	// The template's cloud-init keys are kept; the Mac's key is added if
	// it is not among them.
	var tcfg struct {
		SSHKeys string `json:"sshkeys"`
	}
	if err := c.call(ctx, http.MethodGet, fmt.Sprintf("nodes/%s/qemu/%s/config", node, c.cfg.template), &tcfg); err != nil {
		return "", err
	}
	keys := proxmoxAddKey(tcfg.SSHKeys, opts.PubKey)

	fmt.Fprintf(out, "  ▶ proxmox clone %s → %d %s (node %s)\n", c.cfg.template, int(id), id.Name(), template.Node)
	form := url.Values{"newid": {fmt.Sprint(int(id))}, "name": {id.Name()}, "full": {"1"}}
	if c.cfg.pool != "" {
		form.Set("pool", c.cfg.pool)
	}
	var upid string
	if err := c.callForm(ctx, http.MethodPost, fmt.Sprintf("nodes/%s/qemu/%s/clone", node, c.cfg.template), form, &upid); err != nil {
		return "", err
	}
	if err := c.waitTask(ctx, template.Node, upid, 10*time.Minute); err != nil {
		return "", fmt.Errorf("clone: %w", err)
	}

	fmt.Fprintf(out, "  ▶ cloud-init: %s, user %s, your key\n", ipconfig, opts.User)
	// sshkeys is stored URL-encoded by Proxmox (its verifier accepts only
	// [-A-Za-z0-9_.!~*'()%], so spaces must be %20, not +); the form
	// encoding on top is the transport's.
	form = url.Values{
		"ipconfig0": {ipconfig},
		"ciuser":    {opts.User},
		"sshkeys":   {proxmoxURLEncode(keys)},
		"delete":    {"cipassword"},
	}
	if err := c.callForm(ctx, http.MethodPut, fmt.Sprintf("nodes/%s/qemu/%d/config", node, int(id)), form, nil); err != nil {
		return "", err
	}

	fmt.Fprintf(out, "  ▶ proxmox start %s\n", id.Name())
	if err := c.callForm(ctx, http.MethodPost, fmt.Sprintf("nodes/%s/qemu/%d/status/start", node, int(id)), url.Values{}, &upid); err != nil {
		return "", err
	}
	if err := c.waitTask(ctx, template.Node, upid, 2*time.Minute); err != nil {
		return "", fmt.Errorf("start: %w", err)
	}
	return ip, nil
}

// proxmoxIPConfig is the clone's cloud-init ipconfig0 — "ip=<NETWORK with
// .NNN>/<prefix>,gw=<GATEWAY>" — and the address in it, which is what the
// VM is waited for at. NETWORK may carry the prefix (10.1.10.0/16);
// without one it is /24.
func proxmoxIPConfig(network, gateway string, id vmid.ID) (string, string, error) {
	ip := derivedIP(network, id)
	if ip == "" {
		return "", "", fmt.Errorf("NETWORK=%q in proxmox.env is not an IPv4 network (e.g. 10.1.10.0/16)", network)
	}
	prefix := "24"
	if _, p, ok := strings.Cut(network, "/"); ok {
		prefix = p
	}
	if gw, err := netip.ParseAddr(gateway); err != nil || !gw.Is4() {
		return "", "", fmt.Errorf("GATEWAY=%q in proxmox.env is not an IPv4 address — create needs the clone's default route", gateway)
	}
	return "ip=" + ip + "/" + prefix + ",gw=" + gateway, ip, nil
}

// derivedIP is NETWORK (with or without a prefix) with the last octet
// replaced by the VM number; "" when NETWORK does not parse.
func derivedIP(network string, id vmid.ID) string {
	base, _, _ := strings.Cut(network, "/")
	addr, err := netip.ParseAddr(base)
	if err != nil || !addr.Is4() {
		return ""
	}
	b := addr.As4()
	b[3] = byte(int(id))
	return netip.AddrFrom4(b).String()
}

// proxmoxAddKey appends key to a VM's sshkeys value (URL-encoded,
// newline-separated) unless it is already there, returning the decoded
// list.
func proxmoxAddKey(encoded, key string) string {
	existing, _ := url.PathUnescape(encoded)
	var keys []string
	for _, k := range strings.Split(existing, "\n") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	key = strings.TrimSpace(key)
	for _, k := range keys {
		if k == key {
			return strings.Join(keys, "\n")
		}
	}
	return strings.Join(append(keys, key), "\n")
}

// proxmoxURLEncode is JavaScript's encodeURIComponent — what the Proxmox UI
// uses for sshkeys and what its verifier accepts.
func proxmoxURLEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// waitTask polls a node task until it finishes, failing on any exit status
// other than OK.
func (c *proxmoxClient) waitTask(ctx context.Context, node, upid string, timeout time.Duration) error {
	path := fmt.Sprintf("nodes/%s/tasks/%s/status", url.PathEscape(node), url.PathEscape(upid))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var st struct {
			Status     string `json:"status"`
			ExitStatus string `json:"exitstatus"`
		}
		if err := c.call(ctx, http.MethodGet, path, &st); err != nil {
			return err
		}
		if st.Status == "stopped" {
			if st.ExitStatus != "OK" {
				return fmt.Errorf("proxmox task %s: %s", upid, st.ExitStatus)
			}
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("proxmox task %s did not finish within %s", upid, timeout)
}
