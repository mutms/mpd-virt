package backends

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// container is the native Apple `container` backend: run the published base
// image, wait for systemd, seed the dev user + sudo + key over `container
// exec`, read the IP `container` assigned on its vmnet network. Power is `container start/stop`; delete is
// `container delete` (the inverse of create).
type container struct{}

// Container is this backend's name — native Apple container. Stored in vm.json's
// "backend" and passed as --backend.
const Container backend.Backend = "container"

func init() { backend.Register(Container, container{}) }

func (container) State(ctx context.Context, id vmid.ID) backend.State {
	res, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{"inspect", id.Name()}})
	if err != nil {
		return backend.StateUnknown
	}
	if res.Failed() {
		if containerNotFound(res) {
			return backend.StateAbsent
		}
		return backend.StateUnknown
	}
	return backend.Normalize(parseContainerState(res.Stdout))
}

// containerNotFound reports whether a failed `container inspect` failed because
// no container has that name ("container not found: mpd-141") — as opposed to
// the CLI or its API server being unavailable, which stays "can't say". The
// difference lets stop/remove skip the shutdown wait for a container already
// deleted by hand.
func containerNotFound(r exec.Result) bool {
	return strings.Contains(strings.ToLower(r.Stderr+r.Stdout), "container not found")
}

// containerStopGrace is how long the hard-stop fallback lets the guest's
// systemd shut down before `container stop` SIGKILLs it. The CLI's own default
// is 5s — too short for a VM running databases. Only reached when the guest
// could not be shut down over ssh.
const containerStopGrace = "30"

func (container) Power(ctx context.Context, out io.Writer, id vmid.ID, verb string, _ backend.State) bool {
	fmt.Fprintf(out, "  ▶ container %s %s\n", verb, id.Name())
	args := []string{verb, id.Name()}
	if verb == "stop" {
		args = []string{verb, "--time", containerStopGrace, id.Name()}
	}
	r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: args})
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
	if err := checkContainerVersion(ctx); err != nil {
		return "", err
	}
	if containerExists(ctx, name) {
		return "", fmt.Errorf("a container named %s already exists — remove it first:\n    container stop %s && container rm %s", name, name, name)
	}

	// No --cpus leaves the count to `container` itself: 4, or `[container] cpus`
	// in ~/.config/container/config.toml.
	// --read-only-path NONE: a container gets /proc/sys read-only by default,
	// and this is a VM — podman, WireGuard and sysctl.d all write there.
	args := []string{"run", "-d", "--name", name, "--cap-add", "ALL", "--read-only-path", "NONE", "--memory", opts.Memory}
	cpus := "default cpus"
	if opts.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(opts.CPUs))
		cpus = fmt.Sprintf("%d cpus", opts.CPUs)
	}
	args = append(args, opts.Image)
	fmt.Fprintf(out, "  ▶ container run --name %s (%s, %s, %s)\n", name, opts.Image, opts.Memory, cpus)
	if r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: args}); err != nil {
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
	return "ghcr.io/mutms/mpd-virt-container-apple:13.7.1" // darwin/Apple `container`
}

// containerMinVersion is the oldest `container` release create accepts. One
// floor instead of per-flag feature checks: the CLI moves fast and ships
// security fixes, so the answer to an old one is "update", not a fallback.
var containerMinVersion = [3]int{1, 7, 1}

// checkContainerVersion refuses a `container` CLI older than
// containerMinVersion. A version that cannot be read or parsed (a local dev
// build) passes: `container run` is then the judge.
func checkContainerVersion(ctx context.Context) error {
	r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{"system", "version", "--format", "json"}})
	if err != nil {
		return fmt.Errorf("the `container` CLI is not available: %w", err)
	}
	have, ok := parseContainerVersion(r.Stdout)
	if !ok || !versionLess(have, containerMinVersion) {
		return nil
	}
	min := containerMinVersion
	return fmt.Errorf("Apple `container` %d.%d.%d is too old — mpd-virt needs %d.%d.%d or later.\n    Update it: https://github.com/apple/container/releases",
		have[0], have[1], have[2], min[0], min[1], min[2])
}

// parseContainerVersion pulls the CLI's own version out of `container system
// version --format json` — the entry whose appName is "container" (the API
// server's entry carries a full sentence, not a version). Anything after the
// three numbers ("1.7.1-6-g16e8d488") is ignored.
func parseContainerVersion(stdout string) ([3]int, bool) {
	var comps []struct {
		AppName string `json:"appName"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(stdout), &comps); err != nil {
		return [3]int{}, false
	}
	for _, c := range comps {
		if c.AppName != "container" {
			continue
		}
		var v [3]int
		if n, _ := fmt.Sscanf(strings.TrimSpace(c.Version), "%d.%d.%d", &v[0], &v[1], &v[2]); n == 3 {
			return v, true
		}
	}
	return [3]int{}, false
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// containerExists reports whether a container of that name is already known
// (running or stopped), so create refuses to clobber it. `ls -a -q` prints one
// container ID per line — the name, since create passes it as --name.
func containerExists(ctx context.Context, name string) bool {
	r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{"ls", "-a", "-q"}})
	if err != nil || r.Failed() {
		return false
	}
	for _, line := range strings.Split(r.Stdout, "\n") {
		if strings.TrimSpace(line) == name {
			return true
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
// `container inspect` — the authoritative source, since `container` hands out
// the next free address on every start (no DHCP, nothing to pin) and the bare
// name does not resolve through the OS. Empty on any
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
