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
	Memory string // memory: container "10g", or a VM RAM like "8g" (utm)
	Disk   string // VM disk size, e.g. "80g" (utm); ignored by the container backend
	User   string // dev account to seed
	PubKey string // the public key to authorize, one line ("ssh-ed25519 …")
}

// Create provisions a fresh box for the id through its backend and returns the
// IP it came up on, ready for takeover. Container runs the base image, waits
// for systemd, seeds the dev account + sudo + key, and reads the leased IP;
// utm materializes a fresh VM from the Debian cloud image (utm.go). Parallels/
// Proxmox need a template clone + cloud-init (not yet). A generic box is
// adopted, not created.
func Create(ctx context.Context, out io.Writer, id vmid.ID, be Backend, opts CreateOpts) (string, error) {
	switch be {
	case Container:
		return containerCreate(ctx, out, id, opts)
	case UTM:
		return utmCreate(ctx, out, id, opts)
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

	fmt.Fprintf(out, "  … handing the runtime's DNS to systemd-resolved\n")
	if err := seedResolver(ctx, out, name); err != nil {
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

// seedResolver hands systemd-resolved the upstream nameservers the
// container runtime configured for the guest.
//
// A container guest's network is set up by the runtime, not by
// systemd-networkd: it writes /etc/resolv.conf directly (the vmnet
// gateway) and no link ever reports DNS to systemd-resolved. mpd's VM
// then points resolved at its own dnsmasq for *.mpd.test, so the only
// nameserver resolved publishes is dnsmasq's own address — which dnsmasq
// discards as a local interface ("ignoring nameserver <ip> - local
// interface"), leaving it with nothing to forward to. Names in the zone
// still answer from the local hosts files, so nothing looks wrong until
// the runtime's first apt-get cannot resolve deb.debian.org.
//
// This is the container equivalent of what mpd-prepare-takeover.sh does
// for a VM by putting the link on systemd-networkd — which is not an
// option here, since DHCP would fight the runtime for an address it
// assigned itself.
func seedResolver(ctx context.Context, out io.Writer, name string) error {
	r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{
		"exec", name, "cat", "/etc/resolv.conf",
	}})
	if err != nil {
		return err
	}
	servers := nameservers(r.Stdout)
	if len(servers) == 0 {
		return fmt.Errorf("%s has no nameserver in /etc/resolv.conf — the container runtime configured no DNS, so the guest cannot install packages", name)
	}

	// A drop-in rather than an edit: mpd --vm-setup writes its own
	// resolved drop-in for the .test domain, and the two must coexist.
	// 00- sorts first so this is the base the zone config layers onto.
	write := fmt.Sprintf(
		"mkdir -p /etc/systemd/resolved.conf.d && printf '[Resolve]\\nDNS=%s\\n' > /etc/systemd/resolved.conf.d/00-upstream.conf",
		strings.Join(servers, " "))
	if r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{
		"exec", name, "sh", "-c", write,
	}}); err != nil {
		return err
	} else if r.Failed() {
		return fmt.Errorf("writing the resolver drop-in in %s failed: %s", name, shortErr(r))
	}

	// Applied only where resolved is actually running. On an image
	// without it the drop-in is still correct and takes effect the
	// moment the package arrives, so this is not a failure.
	if r, _ := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{
		"exec", name, "systemctl", "is-active", "--quiet", "systemd-resolved",
	}}); r.Code != 0 {
		fmt.Fprintf(out, "    upstream DNS recorded (%s) — systemd-resolved is not running yet\n",
			strings.Join(servers, " "))
		return nil
	}
	if r, err := exec.Capture(ctx, exec.Cmd{Name: "container", Args: []string{
		"exec", name, "systemctl", "reload-or-restart", "systemd-resolved",
	}}); err != nil {
		return err
	} else if r.Failed() {
		return fmt.Errorf("reloading systemd-resolved in %s failed: %s", name, shortErr(r))
	}
	fmt.Fprintf(out, "    upstream DNS: %s\n", strings.Join(servers, " "))
	return nil
}

// nameservers extracts the addresses from resolv.conf(5) content. Fields,
// not a substring search: a commented-out line names a server too.
func nameservers(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			out = append(out, fields[1])
		}
	}
	return out
}

// shellQuote single-quotes a value for safe use inside `sh -c`.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
