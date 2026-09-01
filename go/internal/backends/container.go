package backends

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// container is the native Apple `container` backend: run the published base
// image, wait for systemd, seed the dev user + sudo + key over `container
// exec`, read the leased vmnet IP. Power is `container start/stop`; delete is
// `container delete` (the inverse of create).
type container struct{}

// Container is this backend's name — native Apple container. Stored in vm.json's
// "backend" and passed as --backend.
const Container backend.Backend = "container"

func init() { backend.Register(Container, container{}) }

func (container) State(ctx context.Context, id vmid.ID) backend.State {
	res, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{"inspect", id.Name()}})
	if err != nil || res.Failed() {
		return backend.StateUnknown
	}
	return backend.Normalize(parseContainerState(res.Stdout))
}

func (container) Power(ctx context.Context, out io.Writer, id vmid.ID, verb string, _ backend.State) bool {
	fmt.Fprintf(out, "  ▶ container %s %s\n", verb, id.Name())
	r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{verb, id.Name()}})
	if err != nil {
		fmt.Fprintf(out, "    … container unavailable here (%v) — assuming the VM is managed elsewhere\n", err)
		return false
	}
	if r.Failed() {
		fmt.Fprintf(out, "    … %s (continuing)\n", backend.ShortErr(r))
		return false
	}
	return true
}

func (container) Candidates(ctx context.Context, id vmid.ID) []string {
	if ip := containerIP(ctx, id.Name()); ip != "" {
		return []string{ip}
	}
	return nil
}

func (container) Create(ctx context.Context, out io.Writer, id vmid.ID, opts backend.CreateOpts) (string, error) {
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
		return "", fmt.Errorf("`container run` for %s failed: %s", name, backend.ShortErr(r))
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

// Delete destroys the container — the inverse of Create. It must already be
// stopped (`container delete` refuses a running one); the caller stops it first.
func (container) Delete(ctx context.Context, out io.Writer, id vmid.ID) error {
	fmt.Fprintf(out, "  ▶ container delete %s\n", id.Name())
	r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{"delete", id.Name()}})
	if err != nil {
		return err
	}
	if r.Failed() {
		return fmt.Errorf("`container delete %s` failed: %s", id.Name(), backend.ShortErr(r))
	}
	return nil
}

func (container) Notes(context.Context, vmid.ID) string { return "" }
func (container) Managed() bool                         { return true }
func (container) Deletable() bool                       { return true }

// DefaultContainerImage is the published, pre-baked base image
// `create --backend=container` runs (built from container/Containerfile):
// `container run` pulls it, so a fresh VM costs a pull rather than the apt run
// adoption would otherwise do. The tag is <debian point release>.<build>; bump
// the build number here when the image is republished because Debian has
// drifted far enough to slow adoption's package step again (see the
// Containerfile header). Only the Apple `container` VM is supported;
// --image overrides it, e.g. for a locally built tag.
func DefaultContainerImage() string {
	return "ghcr.io/mutms/mpd-virt-container-apple:13.6.3" // darwin/Apple `container`
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
// container/ setup script does by hand, so adoption finds a VM it can ssh into.
// Idempotent: re-running rotates the key.
func seedIdentity(ctx context.Context, name, user, pubkey string) error {
	sudoers := "/etc/sudoers.d/90-" + user
	authKeys := "/home/" + user + "/.ssh/authorized_keys"
	steps := [][]string{
		{"sh", "-c", fmt.Sprintf("id %s >/dev/null 2>&1 || useradd --create-home --shell /bin/bash %s", user, user)},
		{"usermod", "-aG", "sudo", user},
		{"sh", "-c", fmt.Sprintf("printf '%%s ALL=(ALL) NOPASSWD:ALL\\n' %s > %s", user, sudoers)},
		{"chmod", "0440", sudoers},
		{"install", "-d", "-m", "700", "-o", user, "-g", user, "/home/" + user + "/.ssh"},
		{"sh", "-c", fmt.Sprintf("printf '%%s\\n' %s > %s", backend.ShellQuote(pubkey), authKeys)},
		{"chmod", "600", authKeys},
		{"chown", user + ":" + user, authKeys},
	}
	for _, step := range steps {
		if r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: append([]string{"exec", name}, step...)}); err != nil {
			return err
		} else if r.Failed() {
			return fmt.Errorf("provisioning %s failed at `%s`: %s", name, strings.Join(step, " "), backend.ShortErr(r))
		}
	}
	return nil
}

// containerIP reads a native container's current vmnet address from
// `container inspect` — the authoritative source, since the address changes on
// every start and the name does not resolve through the OS. Empty on any
// failure (container stopped), so locate falls back.
func containerIP(ctx context.Context, name string) string {
	res, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{"inspect", name}})
	if err != nil || res.Failed() {
		return ""
	}
	ip, _ := parseContainerIP(res.Stdout)
	return ip
}

// parseContainerIP pulls the running vmnet address out of `container inspect`
// JSON — an array whose live address is at status.networks[].ipv4Address in
// CIDR form ("192.168.64.26/24"). configuration.networks carries no address.
func parseContainerIP(stdout string) (string, error) {
	var vms []struct {
		Status struct {
			Networks []struct {
				IPv4Address string `json:"ipv4Address"`
			} `json:"networks"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(stdout), &vms); err != nil {
		return "", fmt.Errorf("parsing container inspect JSON: %w", err)
	}
	for _, b := range vms {
		for _, n := range b.Status.Networks {
			if ip := backend.StripMask(n.IPv4Address); ip != "" {
				return ip, nil
			}
		}
	}
	return "", fmt.Errorf("no ipv4Address in inspect output")
}

// parseContainerState pulls the live state out of `container inspect` JSON — an
// array whose status.state is "running" or "stopped" (the same status object
// parseContainerIP reads the address from).
func parseContainerState(stdout string) string {
	var vms []struct {
		Status struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(stdout), &vms); err != nil {
		return ""
	}
	for _, b := range vms {
		if b.Status.State != "" {
			return b.Status.State
		}
	}
	return ""
}
