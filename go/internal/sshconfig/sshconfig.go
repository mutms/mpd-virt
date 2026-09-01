// Package sshconfig maintains one managed block per VM in ~/.ssh/config,
// so `ssh mpd-<NNN>` reaches the VM the developer works in.
//
// The block sits between name-stamped markers so several VMs coexist in
// one config and each can be found and stripped cleanly. One fence holds
// every stanza for a VM — the VM itself and the SOCKS tier:
//
//	# >>> mpd-<NNN> (managed by mpd-virt) >>>
//	Host mpd-<NNN> <ip>
//	    HostName <ip>
//	    ...
//	Host mpd-<NNN>-socks
//	    HostName <ip>
//	    DynamicForward 1080
//	    ...
//	# <<< mpd-<NNN> <<<
//
// The bare name reaches the VM directly: PHP, the tools and the agent all
// run there, so there is nothing to jump to.
//
// The `-socks` alias is the SOCKS tier: `ssh -N mpd-<NNN>-socks` opens a
// SOCKS5 proxy on 127.0.0.1:1080 tunnelled through the VM, so a browser
// pointed at it (with remote DNS) reaches *.mpd.test using the VM's
// resolver.
//
// Both paths ride plain SSH to the VM's direct IP, so they work even when
// the mpd-proxy WireGuard overlay is offline — that is the whole point of
// keeping them here. The "mpd Root CA" already in the Keychain makes the TLS
// trust work regardless of path.
//
// MPD_VIRT_TEST_SSH_CONFIG points the managed blocks at another file — a
// test-only escape hatch (the TEST in the name says so) that keeps the suite
// off the real ~/.ssh/config. Not a supported way to run mpd-virt.
package sshconfig

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// SocksPort is the local SOCKS5 port the `-socks` alias forwards through the
// VM. Fixed (not per-VM) so a browser's proxy setting is configured once and
// only the `ssh -N mpd-<NNN>-socks` target changes between VMs.
const SocksPort = 1080

// SocksAlias is the ssh Host name of a VM's SOCKS alias.
func SocksAlias(id vmid.ID) string { return id.Name() + "-socks" }

// Path is the ssh config file mpd-virt manages (or $MPD_VIRT_TEST_SSH_CONFIG).
func Path() string {
	if p := os.Getenv("MPD_VIRT_TEST_SSH_CONFIG"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".ssh", "config")
}

func beginMarker(id vmid.ID) string {
	return "# >>> " + id.Name() + " (managed by mpd-virt) >>>"
}

func endMarker(id vmid.ID) string { return "# <<< " + id.Name() + " <<<" }

// render is the self-contained managed block for one VM: the bare alias
// and its `-vm` synonym for the VM itself, plus the `-socks` alias (see
// the package doc). Both stanzas target the VM's direct IP over plain
// SSH, independent of the mpd-proxy overlay, which is exactly why they
// still work when it is down.
//
// Host keys go to the per-VM ~/.mpd-virt/<NNN>/known_hosts, never to your
// own ~/.ssh/known_hosts: UserKnownHostsFile points every stanza at the
// same file mpd-virt's own SSH pins into, so the key adopt recorded and
// printed the fingerprint for is the key your `ssh mpd-<NNN>` trusts.
// Verification is otherwise stock — an unpinned key still prompts, a
// changed one still refuses. HostKeyAlias stores every entry under the
// stable mpd-<NNN> rather than the DHCP address, so the approval survives
// an IP change.

// HostKeyAliases are the names host keys are stored under. Kept beside
// render() — an alias added there without a line here leaves an orphan
// entry.
func HostKeyAliases(id vmid.ID) []string {
	return []string{id.Name()}
}

func render(id vmid.ID, ip, user string) string {
	name := id.Name()
	socksPort := strconv.Itoa(SocksPort)
	// Quoted: ssh splits this value on whitespace, and a home directory
	// may contain a space.
	knownHosts := "    UserKnownHostsFile \"" + paths.KnownHosts(id) + "\""

	// Two patterns on one stanza: the bare name you type and the address,
	// so `ssh <ip>` lands on the same HostKeyAlias instead of a second
	// entry keyed by a DHCP address.
	lines := []string{
		beginMarker(id),
		"Host " + name + " " + ip,
		"    HostName " + ip,
		"    User " + user,
		"    HostKeyAlias " + name,
		knownHosts,
	}
	lines = append(lines,
		"",
		"Host "+name+"-socks",
		"    HostName "+ip,
		"    User "+user,
		"    HostKeyAlias "+name,
		knownHosts,
		"    DynamicForward "+socksPort,
		"    ServerAliveInterval 30",
		"    ServerAliveCountMax 3",
		endMarker(id),
	)
	return strings.Join(lines, "\n")
}

// Write inserts (or replaces) the managed block for a VM, creating ~/.ssh
// and the config file if missing. Idempotent.
//
// The ip and user are validated here at the sink, not only at their
// sources: this function writes into ~/.ssh/config, where a value carrying
// whitespace or config syntax would become directives ssh obeys for other
// hosts. Discovery upstream already validates addresses, but that guard
// must not be the only one in front of this file.
func Write(id vmid.ID, ip, user string) error {
	if a, err := netip.ParseAddr(ip); err != nil || !a.Is4() {
		return fmt.Errorf("refusing to write ssh config for %s: %q is not an IPv4 address", id.Name(), ip)
	}
	if user == "" || strings.ContainsFunc(user, func(r rune) bool {
		return r <= ' ' || r == '"' || r == 0x7f
	}) {
		return fmt.Errorf("refusing to write ssh config for %s: invalid user %q", id.Name(), user)
	}
	if err := ensureFile(); err != nil {
		return err
	}
	cur, err := os.ReadFile(Path())
	if err != nil {
		return err
	}
	rebuilt := stripBlock(string(cur), id)
	if rebuilt != "" {
		rebuilt += "\n\n"
	}
	rebuilt += render(id, ip, user) + "\n"
	return os.WriteFile(Path(), []byte(rebuilt), 0o600)
}

// Strip removes a VM's managed block. No-op if absent.
func Strip(id vmid.ID) error {
	cur, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	stripped := stripBlock(string(cur), id)
	if stripped == string(cur) {
		return nil
	}
	return os.WriteFile(Path(), []byte(stripped), 0o600)
}

// Contains reports whether a managed block for the VM exists.
func Contains(id vmid.ID) bool {
	cur, err := os.ReadFile(Path())
	if err != nil {
		return false
	}
	return strings.Contains(string(cur), beginMarker(id))
}

// stripBlock removes the marked block and collapses the blank lines left
// behind, so repeated write/strip cycles keep the file tidy.
func stripBlock(contents string, id vmid.ID) string {
	begin, end := beginMarker(id), endMarker(id)
	var out []string
	inside := false
	for _, line := range strings.Split(contents, "\n") {
		switch {
		case !inside && strings.Contains(line, begin):
			inside = true
		case inside && strings.Contains(line, end):
			inside = false
		case inside:
			// drop
		default:
			out = append(out, line)
		}
	}
	// Collapse consecutive blanks to one, then drop trailing blanks.
	var collapsed []string
	for _, line := range out {
		if line == "" && len(collapsed) > 0 && collapsed[len(collapsed)-1] == "" {
			continue
		}
		collapsed = append(collapsed, line)
	}
	for len(collapsed) > 0 && collapsed[len(collapsed)-1] == "" {
		collapsed = collapsed[:len(collapsed)-1]
	}
	return strings.Join(collapsed, "\n")
}

func ensureFile() error {
	dir := filepath.Dir(Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(Path()); os.IsNotExist(err) {
		return os.WriteFile(Path(), nil, 0o600)
	}
	return nil
}
