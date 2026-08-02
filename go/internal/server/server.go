// Package server is mpd-virt's registry of LAN machines that are not mpd
// VMs — real hosts on the local network that get a name under mpd.test
// (forge.mpd.test, runner.mpd.test, warp.mpd.test). mpd-virt does not
// manage those machines and knows nothing about what runs on them; it
// remembers their addresses, issues their TLS certificates (signed by the
// root CA on this Mac), and renders the hosts file that `server sync`
// pushes into every VM so containers can reach them by name over verified
// TLS. What to install where belongs in each machine's own runbook, not
// here. Ported from Server.swift.
//
// Layout mirrors the per-VM registry — one directory per host, the same
// KEY=VALUE env file, the same "dir exists + env inside" definition of
// known:
//
//	~/.mpd-virt/servers/forge/
//	├── env        — MPD_SERVER_{NAME,IP}
//	├── cert.pem   — leaf, signed directly by the root on this Mac
//	├── key.pem    — 0600
//	└── sans       — the DNS:… SAN list, so a re-issue reproduces it
//
// The naming rule this depends on: a first label that is a 3-digit number
// is a VM zone (126.mpd.test); anything else under mpd.test is a LAN
// service name. That is what lets both live in one tree with no registry
// of reservations — see isVMZoneLabel.
package server

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/paths"
)

// RootDomain is the DNS root every mpd name hangs under, and what the root
// CA is name-constrained to.
const RootDomain = "mpd.test"

// RemoteLanHostsPath is where the rendered hosts file lands inside a VM;
// mpd's --vm-setup reads it and republishes it through dnsmasq. Must match
// the sibling mpd's vm.LanHostsPath.
const RemoteLanHostsPath = "/var/lib/mpd/conf/lan-hosts"

// Entry is one registered LAN server. The DNS name is derived, never
// stored twice — see Host.
type Entry struct {
	Name string // bare label: "forge"
	IP   string
}

// Host is the entry's DNS name: "forge.mpd.test".
func (e Entry) Host() string { return ServiceHost(e.Name) }

// ServiceHost turns a bare label into its LAN service name: forge →
// forge.mpd.test.
func ServiceHost(name string) string { return name + "." + RootDomain }

// isVMZoneLabel reports whether a first label names a VM zone (a 3-digit
// number like "126") rather than a LAN service. The rule is positional and
// is the inverse of vmid's zero-padded formatting, so the two never
// collide without a reservation table.
func isVMZoneLabel(label string) bool {
	if len(label) != 3 {
		return false
	}
	for _, r := range label {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// --- Paths -----------------------------------------------------------------

func dir(name string) string     { return filepath.Join(paths.Servers(), name) }
func envFile(name string) string { return filepath.Join(dir(name), "env") }

// CertPath, KeyPath, SansPath are the per-server artifact locations.
func CertPath(name string) string { return filepath.Join(dir(name), "cert.pem") }
func KeyPath(name string) string  { return filepath.Join(dir(name), "key.pem") }
func SansPath(name string) string { return filepath.Join(dir(name), "sans") }

// Dir is the server's registry directory (exposed for the delete message).
func Dir(name string) string     { return dir(name) }
func EnvFile(name string) string { return envFile(name) }

// --- Read ------------------------------------------------------------------

// known returns the names of every registered server, sorted. A directory
// without an env file inside is not registered — same rule as the VM
// registry.
func known() ([]string, error) {
	dirents, err := os.ReadDir(paths.Servers())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, d := range dirents {
		if !d.IsDir() {
			continue
		}
		if _, err := os.Stat(envFile(d.Name())); err == nil {
			names = append(names, d.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Exists reports whether a server's env file is present (no parsing).
func Exists(name string) bool {
	_, err := os.Stat(envFile(name))
	return err == nil
}

// Load reads and parses one server's env file. Unknown keys (e.g. a
// hand-added annotation) are ignored — only NAME and IP are required.
func Load(name string) (Entry, error) {
	f, err := os.Open(envFile(name))
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, fmt.Errorf("no server named %q — add it with `mpd-virt server add %s --ip <addr>`", name, name)
		}
		return Entry{}, err
	}
	defer f.Close()

	kv := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if err := sc.Err(); err != nil {
		return Entry{}, err
	}
	n, ip := kv["MPD_SERVER_NAME"], kv["MPD_SERVER_IP"]
	if n == "" || ip == "" {
		return Entry{}, fmt.Errorf("server %q is missing MPD_SERVER_NAME or MPD_SERVER_IP in %s", name, envFile(name))
	}
	return Entry{Name: n, IP: ip}, nil
}

// LoadAll returns every entry, skipping (and reporting to stderr) any that
// fails to parse rather than aborting the whole listing.
func LoadAll() ([]Entry, error) {
	names, err := known()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, name := range names {
		e, err := Load(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping server %q — %v\n", name, err)
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// --- Write -----------------------------------------------------------------

// Save writes (or overwrites) a server's env file, creating its directory.
func Save(e Entry) error {
	if err := os.MkdirAll(dir(e.Name), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf(`# mpd-virt registry entry for %s.
# Managed by `+"`mpd-virt server add`"+`. Edit at your own risk.
MPD_SERVER_NAME=%s
MPD_SERVER_IP=%s
`, e.Host(), e.Name, e.IP)
	return os.WriteFile(envFile(e.Name), []byte(body), 0o644)
}

// Remove deletes a server and everything issued for it. The key goes too:
// keeping key material for a machine we no longer track is how an
// unaccounted-for private key ends up on disk. No-op if absent.
func Remove(name string) error {
	err := os.RemoveAll(dir(name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// --- Validation ------------------------------------------------------------

// Normalise reduces "forge" or "forge.mpd.test" to the bare label "forge",
// rejecting anything that cannot be a LAN service name. Each rejection is a
// distinct message because each has a different fix.
func Normalise(input string) (string, error) {
	label := strings.ToLower(strings.TrimSpace(input))
	suffix := "." + RootDomain

	switch {
	case strings.HasSuffix(label, suffix):
		label = strings.TrimSuffix(label, suffix)
	case strings.Contains(label, "."):
		return "", fmt.Errorf("%q is not under %s — the mpd root CA is name-constrained to %s, "+
			"so a certificate for this name could neither be issued nor verified", input, RootDomain, RootDomain)
	}

	if label == "" {
		return "", fmt.Errorf("empty server name — expected something like `forge` or `forge.%s`", RootDomain)
	}
	if strings.Contains(label, ".") {
		return "", fmt.Errorf("%q has more than one label under %s — LAN services take a single label "+
			"(forge.%s); extra names belong on the certificate as `--san`", input, RootDomain, RootDomain)
	}
	if isVMZoneLabel(label) {
		return "", fmt.Errorf("%q is a 3-digit number, which names VM zone %s.%s — that zone belongs to "+
			"that VM and is signed by its own CA; pick a non-numeric name", label, label, RootDomain)
	}
	// Hostname charset: these become DNS names and SANs.
	for _, r := range label {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return "", fmt.Errorf("%q is not a valid hostname label (use a-z, 0-9 and '-')", label)
		}
	}
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return "", fmt.Errorf("%q is not a valid hostname label (no leading or trailing '-')", label)
	}
	return label, nil
}

// ValidateIP accepts an IPv4 or IPv6 literal. A hostname, CIDR, or typo is
// refused here rather than written into a hosts file where it would
// silently answer nothing.
func ValidateIP(ip string) error {
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("%q is not an IPv4 or IPv6 address", ip)
	}
	return nil
}

// --- Hosts rendering -------------------------------------------------------

// hostsBody is the hosts(5) body for every registered server. hosts format
// rather than something bespoke: it pastes into /etc/hosts verbatim and is
// exactly what dnsmasq's hostsdir= reads at the other end — one format, no
// conversion at either boundary.
func hostsBody() (string, error) {
	entries, err := LoadAll()
	if err != nil {
		return "", err
	}
	lines := []string{
		"# mpd-virt LAN service records.",
		"# Generated by `mpd-virt server`; edits are overwritten.",
	}
	for _, e := range entries {
		lines = append(lines, e.IP+"\t"+e.Host())
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// WriteHostsFile renders conf/lan-hosts, the artifact `server sync` (and a
// future setup) push into VMs, and returns its path.
func WriteHostsFile() (string, error) {
	if err := os.MkdirAll(paths.Conf(), 0o755); err != nil {
		return "", err
	}
	body, err := hostsBody()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(paths.LanHosts(), []byte(body), 0o644); err != nil {
		return "", err
	}
	return paths.LanHosts(), nil
}
