// Package sshconfig maintains one managed block per VM in ~/.ssh/config,
// so `ssh mpd-<NNN>` reaches the runtime container the developer works in.
//
// The block sits between name-stamped markers so several VMs coexist in
// one config and each can be found and stripped cleanly. One fence holds
// every stanza for a VM — the runtime, the VM itself, and the SOCKS tier:
//
//	# >>> mpd-<NNN> (managed by mpd-virt) >>>
//	Host mpd-<NNN>
//	    HostName runtime
//	    ProxyJump <user>@<ip>
//	    ...
//	Host mpd-<NNN>-vm <ip>
//	    HostName <ip>
//	    ...
//	Host mpd-<NNN>-socks
//	    HostName <ip>
//	    DynamicForward 1080
//	    ...
//	# <<< mpd-<NNN> <<<
//
// The bare name is the runtime because that is where the developer (and
// their IDE, and their agent) actually works; the VM that manages the
// containers is the occasional destination, so it takes the `-vm` suffix.
// `ssh mpd-<NNN>` jumps through the VM, which resolves the bare
// `runtime` from the alias mpd keeps on the runtime's line in its
// /etc/hosts.
//
// Note this is the host side only. Inside the VM, mpd writes its own
// aliases for the runtime (`runtime`, `mpd-<NNN>-runtime`) — there the
// bare `mpd-<NNN>` is the VM's own hostname and cannot mean the runtime.
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

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// SocksPort is the local SOCKS5 port the `-socks` alias forwards through the
// VM. Fixed (not per-VM) so a browser's proxy setting is configured once and
// only the `ssh -N mpd-<NNN>-socks` target changes between VMs.
const SocksPort = 1080

// SocksAlias is the ssh Host name of a VM's SOCKS alias.
func SocksAlias(id vmid.ID) string { return id.Name() + "-socks" }

// VMAlias is the ssh Host name of the VM itself, as opposed to the bare
// name, which reaches the runtime container running on it.
func VMAlias(id vmid.ID) string { return id.Name() + "-vm" }

// runtimeHostName is what the runtime stanza connects to. Deliberately
// the bare `runtime`, not the FQDN: with ProxyJump the *VM* resolves the
// target, and mpd publishes `runtime` as a hosts alias on the runtime's
// line in the VM's /etc/hosts, so libc there answers it directly. Works
// over plain SSH with the mpd-proxy overlay down.
//
// The bare form is also the documentation: this block is what a developer
// reads when wiring up an SSH app that has a jump-host field but no
// config file (Terminus and friends). Everything such an app needs is
// then literal on the page — jump = <user>@<ip>, host = runtime — with
// no alias to chase into another stanza.
const runtimeHostName = "runtime"

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
// for the runtime container, the `-vm` alias for the VM itself, and the
// `-socks` alias (see the package doc). The `-vm` and `-socks` stanzas
// target the VM's direct IP, and the runtime alias jumps through it — all
// over plain SSH, independent of the mpd-proxy overlay, which is exactly
// why they still work when it is down.
//
// Host keys are handled like any other SSH host: your first `ssh mpd-<NNN>`
// prompts you to approve the key and records it in your own
// ~/.ssh/known_hosts, and a later change trips the usual warning. The only
// non-default touch is HostKeyAlias, which stores the entry under the stable
// name (mpd-<NNN>, and mpd-<NNN>-runtime for the container) rather than the
// DHCP address — so the approval survives an IP change and each VM's runtime
// doesn't collide with the next one's under the shared HostName "runtime".
// mpd-virt's own behind-the-scenes SSH (adopt/start, which can't stop to ask
// you) keeps its key handling to a per-VM file, so it never writes to your
// known_hosts and your first manual connect is a clean, normal prompt.
// HostKeyAliases are the names host keys are stored under, which are not
// the aliases you type: render() pins `-vm` and `-socks` under the bare
// name and the runtime stanza under `<name>-runtime`. Kept beside render()
// — an alias added there without a line here leaves an orphan entry.
func HostKeyAliases(id vmid.ID) []string {
	name := id.Name()
	return []string{name, name + "-runtime"}
}

func render(id vmid.ID, ip, user string) string {
	name := id.Name()
	socksPort := strconv.Itoa(SocksPort)

	lines := []string{
		beginMarker(id),
		"Host " + name,
		"    HostName " + runtimeHostName,
		"    User " + user,
		"    ProxyJump " + user + "@" + ip,
		"    HostKeyAlias " + name + "-runtime",
		"",
		// Both names: the alias for typing, the address because ProxyJump
		// above names it literally and ssh matches Host patterns against
		// the string it was given — so the jump to the IP gets the same
		// HostKeyAlias as `ssh <alias>` instead of a second entry keyed by IP.
		"Host " + VMAlias(id) + " " + ip,
		"    HostName " + ip,
		"    User " + user,
		"    HostKeyAlias " + name,
	}
	lines = append(lines,
		"",
		"Host "+name+"-socks",
		"    HostName "+ip,
		"    User "+user,
		"    HostKeyAlias "+name,
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
