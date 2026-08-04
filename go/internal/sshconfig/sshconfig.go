// Package sshconfig maintains one managed block per box in ~/.ssh/config,
// so `ssh mpd-<NNN>` reaches the box at its current address.
//
// The block sits between name-stamped markers so several boxes coexist in
// one config and each can be found and stripped cleanly. One fence holds
// every stanza for a box — the box itself, a ProxyJump alias per in-VM
// runtime, and the `-socks` backup alias:
//
//	# >>> mpd-<NNN> (managed by mpd-virt) >>>
//	Host mpd-<NNN>
//	    HostName <ip>
//	    ...
//	Host mpd-<NNN>-php            # and -node, -util
//	    HostName php.runtime.<NNN>.mpd.test
//	    ProxyJump mpd-<NNN>
//	    ...
//	Host mpd-<NNN>-socks
//	    HostName <ip>
//	    DynamicForward 1080
//	    ...
//	# <<< mpd-<NNN> <<<
//
// The runtime aliases (`-php`/`-node`/`-util`) reach the box's runtime
// containers for IDE use: `ssh mpd-<NNN>-php` jumps through the box, whose
// own dnsmasq resolves php.runtime.<NNN>.mpd.test. The `-socks` alias is the
// browser fallback: `ssh -N mpd-<NNN>-socks` opens a SOCKS5 proxy on
// 127.0.0.1:1080 tunnelled through the box, so a browser pointed at it (with
// remote DNS) reaches *.mpd.test using the box's resolver.
//
// Both paths ride plain SSH to the box's direct IP, so they work even when
// the mpd-proxy WireGuard overlay is offline — that is the whole point of
// keeping them here. The "mpd Root CA" already in the Keychain makes the TLS
// trust work regardless of path.
//
// MPD_VIRT_SSH_CONFIG overrides the file, keeping tests off the real one.
package sshconfig

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// SocksPort is the local SOCKS5 port the `-socks` alias forwards through the
// box. Fixed (not per-VM) so a browser's proxy setting is configured once and
// only the `ssh -N mpd-<NNN>-socks` target changes between boxes.
const SocksPort = 1080

// SocksAlias is the ssh Host name of a box's SOCKS backup alias.
func SocksAlias(id vmid.ID) string { return id.Name() + "-socks" }

// runtimes are the in-VM runtime containers each box exposes over SSH. Each
// gets a `mpd-<NNN>-<runtime>` alias that ProxyJumps through the box to
// <runtime>.runtime.<zone>:22, which the box's own dnsmasq resolves — so the
// aliases work over plain SSH even with the mpd-proxy overlay down. Matches the
// sibling mpd's runtime names.
var runtimes = []string{"php", "node", "util"}

// Path is the ssh config file mpd-virt manages (or $MPD_VIRT_SSH_CONFIG).
func Path() string {
	if p := os.Getenv("MPD_VIRT_SSH_CONFIG"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".ssh", "config")
}

func beginMarker(id vmid.ID) string {
	return "# >>> " + id.Name() + " (managed by mpd-virt) >>>"
}

func endMarker(id vmid.ID) string { return "# <<< " + id.Name() + " <<<" }

// render is the self-contained managed block for one box: the box's own Host
// stanza, a ProxyJump alias per runtime, and the `-socks` backup alias (see the
// package doc). The base and `-socks` stanzas target the box's direct IP, and
// the runtime aliases jump through it — all over plain SSH, independent of the
// mpd-proxy overlay, which is exactly why they still work when it is down.
func render(id vmid.ID, ip, user string) string {
	name := id.Name()
	socksPort := strconv.Itoa(SocksPort)

	lines := []string{
		beginMarker(id),
		"Host " + name,
		"    HostName " + ip,
		"    User " + user,
		"    StrictHostKeyChecking no",
		"    UserKnownHostsFile /dev/null",
	}
	for _, rt := range runtimes {
		lines = append(lines,
			"",
			"Host "+name+"-"+rt,
			"    HostName "+rt+".runtime."+id.Zone(),
			"    User "+user,
			"    ProxyJump "+name,
			"    StrictHostKeyChecking no",
			"    UserKnownHostsFile /dev/null",
		)
	}
	lines = append(lines,
		"",
		"# Backup for when mpd-proxy is down: `ssh -N "+name+"-socks`",
		"# opens a SOCKS5 proxy on 127.0.0.1:"+socksPort+" tunnelled through the box.",
		"Host "+name+"-socks",
		"    HostName "+ip,
		"    User "+user,
		"    StrictHostKeyChecking no",
		"    UserKnownHostsFile /dev/null",
		"    DynamicForward "+socksPort,
		"    ServerAliveInterval 30",
		"    ServerAliveCountMax 3",
		endMarker(id),
	)
	return strings.Join(lines, "\n")
}

// Write inserts (or replaces) the managed block for a box, creating ~/.ssh
// and the config file if missing. Idempotent.
func Write(id vmid.ID, ip, user string) error {
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

// Strip removes a box's managed block. No-op if absent.
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

// Contains reports whether a managed block for the box exists.
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
