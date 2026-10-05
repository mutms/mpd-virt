package backends

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// utm drives UTM Desktop via osascript (UTM's AppleScript dictionary) — macOS
// only; the App Store build ships no `utmctl`, so AppleScript is the only
// surface that works for everyone. `create` materializes a fresh Debian VM from
// the cloud .raw + a cidata seed (cloudinit.go); start/stop are thin osascript
// wrappers, and `remove --full` deletes the VM, bundle and all. No UUID (the
// registry and `list` don't use it).
//
// The cidata seed stays attached for the VM's life, as it does on libvirt and
// proxmox. UTM copies it into the VM's bundle on creation, so it needs no host
// file and is deleted with the VM. It does no harm on later boots: the
// instance-id never changes, and mpd's own cloud-init drop-in leaves cloud-init
// nothing but growing the disk.
//
// Networking: UTM's `mode:shared` is macOS vmnet, and the VM takes whatever
// lease vmnet's DHCP hands it — the seed carries no network-config. The address
// is found rather than known: UTM's guest-agent query first, then
// mpd-<NNN>.local over mDNS (locate's own fallback). The cidata seed installs
// qemu-guest-agent and avahi so both answer on a VM not yet adopted.
type utm struct{}

// UTM is this backend's name — UTM Desktop VM (macOS, osascript-driven). Stored in vm.json's
// "backend" and passed as --backend.
const UTM backend.Backend = "utm"

func init() { backend.Register(UTM, utm{}) }

const (
	utmAppPath        = "/Applications/UTM.app"
	utmDefaultDiskGiB = 80
	utmDefaultCPUs    = 4
)

func (utm) State(ctx context.Context, id vmid.ID) backend.State {
	return backend.Normalize(utmVMStatus(ctx, id.Name()))
}

func (utm) Power(ctx context.Context, out io.Writer, id vmid.ID, verb string, _ backend.State) bool {
	utmPower(ctx, out, id, verb)
	return true
}

func (utm) Candidates(ctx context.Context, id vmid.ID) []string {
	// The guest agent's answer is the address the VM holds right now. Empty
	// while the VM is off or the agent is not up yet — locate then falls
	// through to mDNS and the last recorded address.
	return utmQueryIPs(ctx, id.Name())
}

func (utm) Create(ctx context.Context, out io.Writer, id vmid.ID, opts backend.CreateOpts) (string, error) {
	return utmCreate(ctx, out, id, opts)
}

func (utm) Delete(ctx context.Context, out io.Writer, id vmid.ID) error {
	return utmDelete(ctx, out, id)
}

func (utm) Notes(context.Context, vmid.ID) string { return "" }
func (utm) Managed() bool                         { return true }
func (utm) Deletable() bool                       { return true }

// utmCreate provisions a fresh UTM VM and returns its current IP, ready for
// adoption. Untestable end-to-end without nested virt (the guest won't boot),
// but every step up to the boot wait — download, clone, seed, and the osascript
// VM creation — runs on any Mac with UTM installed.
func utmCreate(ctx context.Context, out io.Writer, id vmid.ID, opts backend.CreateOpts) (string, error) {
	if err := requireUTM(); err != nil {
		return "", err
	}
	name := id.Name()
	if utmVMExists(ctx, name) {
		return "", fmt.Errorf("UTM already has a VM named %s — pick a different id, or delete that VM in UTM first (and `mpd-virt remove %s` if it is adopted)", name, id.String())
	}

	// The CLI's --memory default is the single source of truth; a value that
	// does not parse is an error, not a silent fallback.
	memMiB := backend.ParseSizeMiB(opts.Memory)
	if memMiB == 0 {
		return "", fmt.Errorf("cannot parse --memory %q (use e.g. 10g or 10240m)", opts.Memory)
	}
	diskGiB := backend.ParseSizeGiB(opts.Disk)
	if diskGiB == 0 {
		diskGiB = utmDefaultDiskGiB
	}

	// Per-VM staging: the clone + seed live outside the UTM bundle, wiped first
	// so a half-failed prior attempt does not poison this run. UTM copies the
	// sources into its own bundle on import — disk and seed alike — so we clean
	// up after.
	staging := paths.UTMStaging(name)
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", err
	}
	diskPath := staging + "/" + name + ".raw"
	seedPath := staging + "/seed.iso"

	if err := backend.MaterializeDisk(ctx, out, diskPath, diskGiB); err != nil {
		return "", err
	}
	// No network-config: cloud-init falls back to DHCP on the one NIC.
	fmt.Fprintf(out, "  ▶ writing cidata seed → %s\n", seedPath)
	if err := backend.MakeCidataISO(ctx, seedPath, opts.User, opts.PubKey, name, ""); err != nil {
		return "", err
	}

	fmt.Fprintf(out, "  ▶ creating UTM VM %s (%d MiB, %d cpus, %d GB)\n", name, memMiB, utmDefaultCPUs, diskGiB)
	if _, err := runOsascript(ctx, utmCreateScript(name, memMiB, utmDefaultCPUs, diskPath, seedPath)); err != nil {
		return "", err
	}
	// From here a failure should remove the half-built VM so a retry is not
	// blocked by the name collision.
	ok := false
	defer func() {
		if !ok {
			fmt.Fprintf(out, "  ⚠ create failed — removing half-built UTM VM %s\n", name)
			_, _ = runOsascript(ctx, utmDeleteScript(name))
			_ = os.RemoveAll(staging)
		}
	}()

	// virtio-balloon is off on AppleScript-created VMs; without it the full
	// memory stays pinned even when the guest is idle.
	if _, err := runOsascript(ctx, utmBalloonScript(name)); err != nil {
		return "", err
	}

	fmt.Fprintf(out, "  ▶ starting UTM VM (cloud-init runs on first boot — 1–3 min) …\n")
	if _, err := runOsascript(ctx, utmStartScript(name)); err != nil {
		return "", err
	}

	// The VM is findable once cloud-init has installed the guest agent and
	// avahi, which is well into first boot.
	ip, err := backend.WaitLocated(ctx, id, UTM, 300*time.Second)
	if err != nil {
		return "", fmt.Errorf("UTM VM %s was not found on the network within 5 min (UTM's guest-agent query and %s.local both came up empty) — cloud-init may still be running or have failed; open the UTM console to inspect.\n\n%w", name, name, err)
	}
	// Pin the fresh VM's host key from the very first contact, in the same
	// per-VM file adoption will use — the key recorded while cloud-init's output
	// is still on the UTM console carries through the whole lifecycle.
	t := host.Target{
		User: opts.User, Host: ip,
		KnownHostsFile: paths.EnsureKnownHosts(id), HostKeyAlias: id.Name(),
	}
	if err := backend.WaitReachableOrWhy(ctx, t, 300*time.Second); err != nil {
		return "", fmt.Errorf("UTM VM %s did not accept ssh at %s within 5 min — cloud-init may still be running or have failed; open the UTM console to inspect.\n\n%w", name, ip, err)
	}
	if err := backend.WaitCloudInitDone(ctx, out, t, 300*time.Second); err != nil {
		return "", err
	}

	ok = true
	_ = os.RemoveAll(staging)
	fmt.Fprintf(out, "  ▶ UTM VM ready: %s\n", ip)
	return ip, nil
}

// utmDelete destroys the VM and everything in its bundle — disk and cidata seed
// — the inverse of utmCreate. UTM asks for no confirmation; `remove --full` has
// already taken it. A VM still running is forced off first: its disk is about
// to go, so there is nothing a graceful shutdown would save. A VM already gone
// is success, so a half-finished remove can be re-run.
func utmDelete(ctx context.Context, out io.Writer, id vmid.ID) error {
	if err := requireUTM(); err != nil {
		return err
	}
	name := id.Name()
	if !utmVMExists(ctx, name) {
		return nil
	}
	if utmVMStatus(ctx, name) != "stopped" {
		fmt.Fprintf(out, "  ▶ osascript UTM stop %s (forced)\n", name)
		_, _ = runOsascript(ctx, utmKillScript(name))
		if err := waitVMStopped(ctx, name, 60*time.Second); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "  ▶ osascript UTM delete %s\n", name)
	if _, err := runOsascript(ctx, utmDeleteScript(name)); err != nil {
		return err
	}
	return nil
}

// utmPower runs a start/stop power verb for a UTM VM via osascript, matching the
// best-effort contract of the container/parallels power path.
func utmPower(ctx context.Context, out io.Writer, id vmid.ID, verb string) {
	var script string
	switch verb {
	case "start":
		script = utmStartScript(id.Name())
	case "stop":
		script = utmStopScript(id.Name())
	default:
		return
	}
	fmt.Fprintf(out, "  ▶ osascript UTM %s %s\n", verb, id.Name())
	if _, err := runOsascript(ctx, script); err != nil {
		fmt.Fprintf(out, "    … %v (continuing — the VM may already be in that state)\n", err)
	}
}

// --- osascript plumbing -----------------------------------------------------

func requireUTM() error {
	if _, err := os.Stat(utmAppPath); err != nil {
		return fmt.Errorf("%s not found — install UTM (App Store or https://mac.getutm.app) and retry", utmAppPath)
	}
	return nil
}

// runOsascript runs one AppleScript via `osascript -e` and returns its trimmed
// stdout (some scripts return a value we parse, e.g. the VM status).
func runOsascript(ctx context.Context, script string) (string, error) {
	r, err := exec.Capture(ctx, exec.Cmd{Name: "osascript", Args: []string{"-e", script}})
	if err != nil {
		return "", err
	}
	if r.Failed() {
		return "", fmt.Errorf("osascript failed (exit %d): %s", r.Code, backend.ShortErr(r))
	}
	return strings.TrimSpace(r.Stdout), nil
}

// utmVMExists reports whether UTM knows a VM by that name.
func utmVMExists(ctx context.Context, name string) bool {
	script := fmt.Sprintf(`tell application "UTM"
	try
		set _ to id of virtual machine named %s
		return "yes"
	on error
		return "no"
	end try
end tell`, asQuote(name))
	out, err := runOsascript(ctx, script)
	return err == nil && out == "yes"
}

// utmVMStatus returns UTM's bare status word (started/stopped/paused/…), or ""
// on error.
func utmVMStatus(ctx context.Context, name string) string {
	out, err := runOsascript(ctx, fmt.Sprintf(`tell application "UTM"
	return (status of virtual machine named %s) as string
end tell`, asQuote(name)))
	if err != nil {
		return ""
	}
	return out
}

// utmQueryIPs asks UTM for the guest's addresses — its `query ip` command,
// answered by qemu-guest-agent inside the VM. Nil on any failure (VM off, agent
// not running yet).
func utmQueryIPs(ctx context.Context, name string) []string {
	out, err := runOsascript(ctx, fmt.Sprintf(`tell application "UTM"
	return query ip of virtual machine named %s
end tell`, asQuote(name)))
	if err != nil {
		return nil
	}
	return utmLANAddrs(out)
}

// utmLANAddrs picks the LAN candidates out of `query ip`'s answer, which
// osascript prints as one comma-separated list of every address on every
// interface. Only IPv4 is kept, minus loopback, link-local and the overlay
// range (the VM's own container bridge) — the filter proxmoxAgentIPs applies
// to the same guest-agent data.
func utmLANAddrs(list string) []string {
	var ips []string
	for _, f := range strings.Split(list, ",") {
		addr, err := netip.ParseAddr(strings.TrimSpace(f))
		if err != nil || !addr.Is4() {
			continue
		}
		if addr.IsLoopback() || addr.IsLinkLocalUnicast() || overlayRange.Contains(addr) {
			continue
		}
		ips = append(ips, addr.String())
	}
	return ips
}

// asQuote renders an AppleScript string literal, escaping backslash and quote.
func asQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func utmCreateScript(name string, memMiB, cpus int, diskPath, seedPath string) string {
	// Mirrors mpd/setup/macos-utm/lib/create-vm.sh: qemu, aarch64, shared
	// network, two drives (system disk + cidata seed). Neither drive is marked
	// removable, so UTM imports both: the images are copied into the bundle and
	// the staging files can go once create is done.
	return fmt.Sprintf(`tell application "UTM"
	set diskFile to POSIX file %s
	set seedFile to POSIX file %s
	make new virtual machine with properties {backend:qemu, configuration:{name:%s, architecture:"aarch64", memory:%d, cpu cores:%d, drives:{{source:diskFile}, {source:seedFile}}, network interfaces:{{mode:shared}}}}
end tell`, asQuote(diskPath), asQuote(seedPath), asQuote(name), memMiB, cpus)
}

func utmBalloonScript(name string) string {
	return fmt.Sprintf(`tell application "UTM"
	set vm to virtual machine named %s
	set config to configuration of vm
	set qemu additional arguments of config to {{argument string:"-device"}, {argument string:"virtio-balloon-pci,free-page-reporting=on"}}
	update configuration of vm with config
end tell`, asQuote(name))
}

func utmStartScript(name string) string {
	return fmt.Sprintf("tell application \"UTM\"\n\tstart virtual machine named %s\nend tell", asQuote(name))
}

// utmStopScript is a graceful ACPI stop; utmKillScript forces it off.
func utmStopScript(name string) string {
	return fmt.Sprintf("tell application \"UTM\"\n\tstop virtual machine named %s\nend tell", asQuote(name))
}

func utmKillScript(name string) string {
	return fmt.Sprintf("tell application \"UTM\"\n\tstop virtual machine named %s by force\nend tell", asQuote(name))
}

func utmDeleteScript(name string) string {
	return fmt.Sprintf("tell application \"UTM\"\n\tdelete virtual machine named %s\nend tell", asQuote(name))
}

func waitVMStopped(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if utmVMStatus(ctx, name) == "stopped" {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("UTM VM %s did not reach state=stopped within %s", name, timeout)
}
