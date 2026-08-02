package backend

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// CreateOpts carries what Create needs beyond the id and backend.
type CreateOpts struct {
	Image  string // base image to run (container backend), e.g. mpd-virt-container-apple
	Memory string // container memory, e.g. "10g"
	User   string // dev account to seed
	PubKey string // the public key to authorize, one line ("ssh-ed25519 …")
}

// Create provisions a fresh box for the id through its backend and returns the
// IP it came up on, ready for takeover. Only the container backend is wired: it
// runs the base image, waits for systemd, seeds the dev account + sudo + key,
// and reads the leased IP. Parallels/Proxmox need a template clone + cloud-init
// (not yet). A generic box is adopted, not created.
func Create(ctx context.Context, out io.Writer, id vmid.ID, be Backend, opts CreateOpts) (string, error) {
	switch be {
	case Container:
		return containerCreate(ctx, out, id, opts)
	case Parallels, Proxmox:
		return "", fmt.Errorf("create is not implemented for the %s backend yet (needs a template clone + cloud-init) — create the box yourself, then `mpd-virt takeover`", be)
	default:
		return "", fmt.Errorf("a %s box is adopted, not created — use `mpd-virt takeover %s <IP> --backend %s`", be, id.Pad(), be)
	}
}

// DefaultContainerImage is the base image `create --backend=container` runs,
// derived from the runtime this machine hosts. Deliberately named
// mpd-virt-container-<runtime> so a future wsl build slots in without a new
// convention; --image overrides it (e.g. a published ghcr.io/… reference).
func DefaultContainerImage() string {
	return "mpd-virt-container-apple" // darwin/Apple `container` today
}

func containerCreate(ctx context.Context, out io.Writer, id vmid.ID, opts CreateOpts) (string, error) {
	name := id.Name()
	if containerExists(ctx, name) {
		return "", fmt.Errorf("a container named %s already exists — remove it first:\n    container stop %s && container rm %s", name, name, name)
	}

	fmt.Fprintf(out, "  ▶ container run --name %s (%s, %s)\n", name, opts.Image, opts.Memory)
	if r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{
		"run", "-d", "--name", name, "--cap-add", "ALL", "--memory", opts.Memory, opts.Image,
	}}); err != nil {
		return "", err
	} else if r.Failed() {
		return "", fmt.Errorf("`container run` for %s failed: %s", name, shortErr(r))
	}

	fmt.Fprintf(out, "  … waiting for systemd in %s\n", name)
	if err := waitSystemd(ctx, name); err != nil {
		return "", err
	}

	fmt.Fprintf(out, "  … provisioning dev user %s (sudo + key)\n", opts.User)
	if err := seedIdentity(ctx, name, opts.User, opts.PubKey); err != nil {
		return "", err
	}

	ip := containerIP(ctx, name)
	if ip == "" {
		return "", fmt.Errorf("%s is up but `container inspect` reported no IP", name)
	}
	return ip, nil
}

// containerExists reports whether a container of that name is already known
// (running or stopped), so create refuses to clobber it.
func containerExists(ctx context.Context, name string) bool {
	r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{"ls", "-a"}})
	if err != nil || r.Failed() {
		return false
	}
	for _, line := range strings.Split(r.Stdout, "\n") {
		for _, f := range strings.Fields(line) {
			if f == name {
				return true
			}
		}
	}
	return false
}

// waitSystemd blocks until the container's systemd finishes booting. Both
// "running" and "degraded" mean up (degraded = booted with a unit failed, still
// usable); is-system-running prints the state even when it exits non-zero.
func waitSystemd(ctx context.Context, name string) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		r, _ := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{
			"exec", name, "sh", "-c", "systemctl is-system-running",
		}})
		switch strings.TrimSpace(r.Stdout) {
		case "running", "degraded":
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s did not finish booting systemd in time (last state: %q)", name, strings.TrimSpace(r.Stdout))
		}
		time.Sleep(2 * time.Second)
	}
}

// seedIdentity adds the dev account, passwordless sudo, and the authorized key
// to a freshly booted container over `container exec` — the same steps the
// containers/apple setup script does by hand, so takeover finds a box it can
// ssh into. Idempotent: re-running rotates the key.
func seedIdentity(ctx context.Context, name, user, pubkey string) error {
	sudoers := "/etc/sudoers.d/90-" + user
	authKeys := "/home/" + user + "/.ssh/authorized_keys"
	steps := [][]string{
		{"sh", "-c", fmt.Sprintf("id %s >/dev/null 2>&1 || useradd --create-home --shell /bin/bash %s", user, user)},
		{"usermod", "-aG", "sudo", user},
		{"sh", "-c", fmt.Sprintf("printf '%%s ALL=(ALL) NOPASSWD:ALL\\n' %s > %s", user, sudoers)},
		{"chmod", "0440", sudoers},
		{"install", "-d", "-m", "700", "-o", user, "-g", user, "/home/" + user + "/.ssh"},
		{"sh", "-c", fmt.Sprintf("printf '%%s\\n' %s > %s", shellQuote(pubkey), authKeys)},
		{"chmod", "600", authKeys},
		{"chown", user + ":" + user, authKeys},
	}
	for _, step := range steps {
		if r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: append([]string{"exec", name}, step...)}); err != nil {
			return err
		} else if r.Failed() {
			return fmt.Errorf("provisioning %s failed at `%s`: %s", name, strings.Join(step, " "), shortErr(r))
		}
	}
	return nil
}

// shellQuote single-quotes a value for safe use inside `sh -c`.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
