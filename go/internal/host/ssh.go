// Package host drives a VM over SSH from the Mac. It is a thin wrapper
// over internal/exec's ssh, so the rest of mpd-virt gets reachability and
// remote-command primitives without any other package touching ssh
// directly. Every mpd-virt backend drives its VM this way — the same
// shape whether the VM is a native container or an adopted VM.
package host

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/exec"
)

// Target identifies a VM: a user at an address, plus where its ssh host
// key is pinned.
type Target struct {
	User string
	Host string
	// KnownHostsFile pins the VM's host key in a per-VM file (recorded on
	// first contact, refused if it ever changes). Empty falls back to the
	// user's own ~/.ssh/known_hosts — only for targets that are not adopted
	// VMes (a LAN probe, a not-yet-created VM).
	KnownHostsFile string
	// HostKeyAlias stores and looks up the host key under a stable name
	// (mpd-<NNN>) instead of the current address, so a VM that moves to a
	// new DHCP lease keeps its key continuity instead of a fresh TOFU.
	HostKeyAlias string
}

func (t Target) addr() string { return t.User + "@" + t.Host }

// sshArgs are the non-interactive options every mpd-virt ssh call uses:
// key auth only (no password prompt), accept a new host key on first
// contact but NEVER a changed one, and a short connect timeout. remote is
// the command to run. The known-hosts file and alias are passed explicitly
// so the pinning holds even though the managed ~/.ssh/config block matches
// the VM's bare IP (command-line options win over config-file ones).
func (t Target) sshArgs(remote string) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=6",
	}
	if t.KnownHostsFile != "" {
		args = append(args, "-o", "UserKnownHostsFile="+quoteIfSpaces(t.KnownHostsFile))
	}
	if t.HostKeyAlias != "" {
		args = append(args, "-o", "HostKeyAlias="+t.HostKeyAlias)
	}
	return append(args, t.addr(), remote)
}

// quoteIfSpaces wraps a path in double quotes when it contains whitespace —
// -o values go through ssh's config-line parser, which splits on spaces.
func quoteIfSpaces(p string) string {
	if strings.ContainsAny(p, " \t") {
		return `"` + p + `"`
	}
	return p
}

// Run executes a remote command over ssh and captures its output.
func (t Target) Run(ctx context.Context, remote string) (exec.Result, error) {
	return exec.Capture(ctx, exec.Cmd{Name: "ssh", Args: t.sshArgs(remote)})
}

// Stream runs a remote command over ssh with its output streamed live to
// this process's stdout/stderr, returning the remote exit code. Used for
// the long bootstrap steps.
func (t Target) Stream(ctx context.Context, remote string) (int, error) {
	return exec.Run(ctx, exec.Cmd{Name: "ssh", Args: t.sshArgs(remote)})
}

// Reachable reports whether key-auth ssh to the target succeeds. For poll
// loops, where the only decision is whether to retry.
func (t Target) Reachable(ctx context.Context) bool {
	r, err := t.Run(ctx, "true")
	return err == nil && r.Code == 0
}

// CheckReachable probes the same thing as Reachable but returns an error
// naming the actual cause, for the paths where the next thing a human
// does depends on which failure it was.
//
// Worth the classification because the failures are indistinguishable at
// the exit code and their remedies are unrelated. A changed host key —
// the routine consequence of rolling a snapshot back or rebuilding a VM
// on the same address — is refused by StrictHostKeyChecking=accept-new,
// which accepts a host key on first contact but never a changed one. A
// single "is the key authorized there?" sends the reader to
// authorized_keys when the fix was one ssh-keygen away.
//
// The stale entry is named, never removed: what proves the VM on this
// address is the intended one is its host key, and adoption pushes CA
// material to whatever answers. Key auth does not stand in for that — a
// rogue endpoint can accept an authentication it never verified — so the
// removal stays a decision a human makes with the fingerprints in view.
func (t Target) CheckReachable(ctx context.Context) error {
	r, err := t.Run(ctx, "true")
	if err == nil && r.Code == 0 {
		return nil
	}
	detail := r.Stderr
	if err != nil && detail == "" {
		detail = err.Error()
	}
	return t.classify(detail)
}

// classify turns ssh's stderr into the error CheckReachable returns. Split
// out so the mapping can be tested against real ssh output without a VM
// to fail against.
func (t Target) classify(detail string) error {
	switch {
	case strings.Contains(detail, "REMOTE HOST IDENTIFICATION HAS CHANGED"),
		strings.Contains(detail, "Host key verification failed"):
		name, file, where := t.Host, "", "known_hosts"
		if t.HostKeyAlias != "" {
			name = t.HostKeyAlias
		}
		if t.KnownHostsFile != "" {
			file = " -f " + t.KnownHostsFile
			where = t.KnownHostsFile
		}
		return fmt.Errorf(`the host key of %s does not match the one pinned in %s.

Expected when the VM was rolled back to a snapshot or rebuilt on the
same address. If you did neither, do not clear it — that is the warning
working.

    ssh-keygen -R %s%s

then re-run this command.`, t.Host, where, name, file)

	case strings.Contains(detail, "Permission denied"):
		return fmt.Errorf("%s refused key auth — is this Mac's public key in ~/.ssh/authorized_keys on the VM?", t.addr())

	case strings.Contains(detail, "Connection timed out"),
		strings.Contains(detail, "No route to host"),
		strings.Contains(detail, "Connection refused"),
		strings.Contains(detail, "Name or service not known"):
		return fmt.Errorf("no ssh answer from %s — is the VM up, and is that its current address?", t.Host)
	}
	return fmt.Errorf("cannot ssh to %s: %s", t.addr(),
		oneOf(strings.TrimSpace(detail), "ssh failed without output"))
}

// Install copies a local file into the VM at remotePath with the given
// octal mode, creating parent directories. It runs as the dev user with
// no sudo: the CA lands under /var/lib/mpd, which 30-mpd-build leaves
// owned by that user, and mpd --vm-setup — also that user — must own the
// tree to chmod it. A sudo push would leave the parent dirs root-owned
// and break vm-setup.
func (t Target) Install(ctx context.Context, localPath, remotePath, mode string) error {
	dir := path.Dir(remotePath)
	if r, err := t.Run(ctx, "mkdir -p "+dir); err != nil {
		return err
	} else if r.Failed() {
		return fmt.Errorf("mkdir %s: %s", dir, oneOf(r.Stderr, r.Stdout))
	}

	r, err := exec.Capture(ctx, exec.Cmd{Name: "scp", Args: t.scpArgs(localPath, remotePath)})
	if err != nil {
		return err
	}
	if r.Failed() {
		return fmt.Errorf("scp %s: %s", path.Base(remotePath), oneOf(r.Stderr, r.Stdout))
	}

	if r, err := t.Run(ctx, "chmod "+mode+" "+remotePath); err != nil {
		return err
	} else if r.Failed() {
		return fmt.Errorf("chmod %s: %s", remotePath, oneOf(r.Stderr, r.Stdout))
	}
	return nil
}

// ScpTree copies a local directory to remoteDest, keeping the mode bits
// (so a bin/ script stays executable). remoteDest must not already exist —
// scp copies *into* an existing directory, which would nest the tree one
// level deeper. The parent must exist; callers stage under a mktemp -d.
func (t Target) ScpTree(ctx context.Context, localDir, remoteDest string) error {
	r, err := exec.Capture(ctx, exec.Cmd{Name: "scp", Args: t.scpTreeArgs(localDir, remoteDest)})
	if err != nil {
		return err
	}
	if r.Failed() {
		return fmt.Errorf("scp -r %s: %s", localDir, oneOf(r.Stderr, r.Stdout))
	}
	return nil
}

// ScpTreeLive is ScpTree with scp's own progress meter left on: stdout and
// stderr go to the terminal instead of being captured, so a long copy shows
// per-file percentage, rate and ETA.
//
// A captured scp says nothing for as long as the copy takes. That is right
// for a few kilobytes of dev tools and wrong once an overlay carries
// something big — an IDE tarball seeded through vm/home is minutes of
// silence in the middle of an adoption, indistinguishable from a hang.
// Callers choose by size. The cost is the error: scp has already printed
// its own message, so this returns the exit code, not the text.
func (t Target) ScpTreeLive(ctx context.Context, localDir, remoteDest string) error {
	code, err := exec.Run(ctx, exec.Cmd{Name: "scp", Args: t.scpTreeArgs(localDir, remoteDest)})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("scp -r %s: exit %d (scp's own message is above)", localDir, code)
	}
	return nil
}

// WriteRemote writes content to a file on the VM at remotePath with the
// given octal mode, as the dev user (via a local temp file + Install).
func (t Target) WriteRemote(ctx context.Context, content, remotePath, mode string) error {
	tmp, err := os.CreateTemp("", "mpd-virt-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return t.Install(ctx, tmp.Name(), remotePath, mode)
}

// scpArgs mirror sshArgs — same options, same host-key pinning. dest is
// the remote path (this host's side).
func (t Target) scpArgs(localPath, dest string) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=6",
	}
	if t.KnownHostsFile != "" {
		args = append(args, "-o", "UserKnownHostsFile="+quoteIfSpaces(t.KnownHostsFile))
	}
	if t.HostKeyAlias != "" {
		args = append(args, "-o", "HostKeyAlias="+t.HostKeyAlias)
	}
	return append(args, localPath, t.addr()+":"+dest)
}

// scpTreeArgs are scpArgs for a directory: -r to recurse and -p to keep
// the mode bits, which is what carries the execute bit on bin/ scripts.
// dest must not exist — scp copies *into* an existing directory, which
// would nest the tree one level deeper on every push.
func (t Target) scpTreeArgs(localDir, dest string) []string {
	return append([]string{"-r", "-p"}, t.scpArgs(localDir, dest)...)
}

func oneOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Line runs a remote command and returns its trimmed single-line stdout.
// A non-zero remote exit becomes an error carrying stderr.
func (t Target) Line(ctx context.Context, remote string) (string, error) {
	r, err := t.Run(ctx, remote)
	if err != nil {
		return "", err
	}
	if r.Failed() {
		msg := r.Stderr
		if msg == "" {
			msg = r.Stdout
		}
		return "", &RemoteError{Remote: remote, Code: r.Code, Msg: msg}
	}
	return strings.TrimSpace(r.Stdout), nil
}

// RemoteError is a non-zero exit from a remote command.
type RemoteError struct {
	Remote string
	Code   int
	Msg    string
}

func (e *RemoteError) Error() string {
	if e.Msg != "" {
		return e.Remote + ": exit " + itoa(e.Code) + ": " + e.Msg
	}
	return e.Remote + ": exit " + itoa(e.Code)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
