// Package exec is mpd-virt's single gateway for running external
// processes, mirroring the sibling mpd's internal/exec constraint: no
// other package imports os/exec.
//
// Unlike mpd — which pins absolute paths because it runs privileged
// operations inside the VM — mpd-virt runs on the developer's Mac and
// resolves allow-listed commands with the normal PATH. macOS tool
// locations vary (Homebrew, /usr/bin, /usr/local/bin) and nothing here
// runs privileged on the host, so an allow-list by name is the right
// guard: a command not on the list cannot be run at all.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"strings"
)

// allowed is the set of command names mpd-virt may run. Widening it is a
// deliberate act — it grows what the host tool can execute.
var allowed = map[string]bool{
	"ssh": true,
	"scp": true,
	// ssh-keygen renders the pinned host-key fingerprint at adoption, so the
	// first-contact key is shown for comparison against the box's console.
	"ssh-keygen": true,
	// Backend power control (start/stop): the native Apple container CLI and
	// the Parallels Desktop Pro CLI. Both run on the Mac hosting the box.
	"container": true,
	"prlctl":    true,
	// UTM backend: osascript drives UTM Desktop (no utmctl in the App Store
	// build); curl/tar fetch + unpack the Debian cloud image; hdiutil builds
	// the cidata seed ISO. All macOS built-ins.
	"osascript": true,
	"curl":      true,
	"tar":       true,
	"hdiutil":   true,
	// libvirt backend (Linux host): virsh defines/powers the VM, qemu-img
	// makes its disk from the cloud image, genisoimage builds the seed.
	"virsh":       true,
	"qemu-img":    true,
	"genisoimage": true,
	// Read-only System Keychain checks: is the mpd root CA trusted, and is a
	// stale one present. A macOS built-in; mpd-virt only reads, never installs
	// trust (that stays an explicit `sudo security add-trusted-cert` the user
	// runs — see internal/cli/catrust.go).
	"security": true,
}

// Cmd describes one external command.
type Cmd struct {
	// Name is a bare command name; it must be on the allow-list.
	Name string
	Args []string
}

// Result is the outcome of a captured command.
type Result struct {
	Code   int
	Stdout string
	Stderr string
}

// Failed reports whether the command exited non-zero.
func (r Result) Failed() bool { return r.Code != 0 }

// Run executes cmd, streaming stdout and stderr live to this process's
// own os.Stdout / os.Stderr. Same error contract as Capture: a non-zero
// exit is not an error, only a failure to start (or a disallowed name).
// Used for the long, verbose bootstrap steps a caller wants to watch.
//
// ssh output is remote output — a box this tool's own threat model calls
// compromised — so it streams through the terminal sanitizer (SGR colors
// pass, every other escape sequence is dropped). Local tools (curl's
// progress bar needs the real tty) stream untouched.
func Run(ctx context.Context, cmd Cmd) (int, error) {
	if !allowed[cmd.Name] {
		return -1, fmt.Errorf("command %q is not allow-listed in internal/exec", cmd.Name)
	}
	c := osexec.CommandContext(ctx, cmd.Name, cmd.Args...)
	if cmd.Name == "ssh" {
		c.Stdout, c.Stderr = newFilterWriter(os.Stdout), newFilterWriter(os.Stderr)
	} else {
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
	}
	if err := c.Run(); err != nil {
		var ee *osexec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

// Capture runs cmd and captures stdout and stderr separately, with
// trailing newlines trimmed. A non-zero exit is NOT an error: err is
// non-nil only when the command is not allow-listed or could not start.
//
// Both streams are scrubbed of terminal escape sequences and stray control
// bytes before anything parses or prints them: ssh output is remote (and so
// untrusted), and even local tools relay guest-influenced strings (prlctl's
// guest-reported fields, container inspect). Nothing captured here is
// binary, so the scrub is loss-free for every legitimate caller.
func Capture(ctx context.Context, cmd Cmd) (Result, error) {
	if !allowed[cmd.Name] {
		return Result{Code: -1}, fmt.Errorf("command %q is not allow-listed in internal/exec", cmd.Name)
	}
	c := osexec.CommandContext(ctx, cmd.Name, cmd.Args...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr

	err := c.Run()
	res := Result{
		Stdout: Sanitize(strings.TrimRight(stdout.String(), "\n")),
		Stderr: Sanitize(strings.TrimRight(stderr.String(), "\n")),
	}
	if err != nil {
		var ee *osexec.ExitError
		if errors.As(err, &ee) {
			res.Code = ee.ExitCode()
			return res, nil
		}
		return res, err
	}
	return res, nil
}
