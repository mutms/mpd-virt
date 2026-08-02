// Package host drives a box over SSH from the Mac. It is a thin wrapper
// over internal/exec's ssh, so the rest of mpd-virt gets reachability and
// remote-command primitives without any other package touching ssh
// directly. Every mpd-virt backend drives its box this way — the same
// shape whether the box is a native container or an adopted VM.
package host

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/exec"
)

// Target identifies a box: a user at an address.
type Target struct {
	User string
	Host string
}

func (t Target) addr() string { return t.User + "@" + t.Host }

// sshArgs are the non-interactive options every mpd-virt ssh call uses:
// key auth only (no password prompt), accept a new host key on first
// contact, and a short connect timeout. remote is the command to run.
func (t Target) sshArgs(remote string) []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=6",
		t.addr(), remote,
	}
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

// Reachable reports whether key-auth ssh to the target succeeds.
func (t Target) Reachable(ctx context.Context) bool {
	r, err := t.Run(ctx, "true")
	return err == nil && r.Code == 0
}

// Install copies a local file into the box at remotePath with the given
// octal mode, creating parent directories. It runs as the dev user with
// no sudo: the CA lands under /var/lib/mpd, which 20-git-clone leaves
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

// WriteRemote writes content to a file on the box at remotePath with the
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

// scpArgs mirror sshArgs: non-interactive, key auth, accept a new host
// key, short timeout. dest is the remote path (this host's side).
func (t Target) scpArgs(localPath, dest string) []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=6",
		localPath, t.addr() + ":" + dest,
	}
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
