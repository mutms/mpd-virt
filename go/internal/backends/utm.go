package backends

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
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
// wrappers. Deleting the VM itself is UTM's business — mpd-virt's `remove` only
// un-adopts. No UUID (the registry and `list` don't use it); a pinned vmnet IP
// instead of a guest-IP query.
//
// Networking: UTM's `mode:shared` uses macOS vmnet, fixed at 192.168.64.0/24 by
// vmnet.framework. So each mpd-<NNN> UTM VM is pinned (via cloud-init
// network-config) to 192.168.64.<NNN>, gateway .1 — which is also how locate
// finds it, since UTM exposes no clean guest-IP query.
type utm struct{}

func init() { backend.Register(backend.UTM, utm{}) }

const (
	utmAppPath        = "/Applications/UTM.app"
	utmSubnet         = "192.168.64"
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
	// UTM exposes no clean guest-IP query, so the VM is pinned to its canonical
	// vmnet address; offer it only when the VM actually exists, and let locate's
	// ssh probe confirm it is up.
	if utmVMExists(ctx, id.Name()) {
		return []string{utmCanonicalIP(id)}
	}
	return nil
}

func (utm) Create(ctx context.Context, out io.Writer, id vmid.ID, opts backend.CreateOpts) (string, error) {
	return utmCreate(ctx, out, id, opts)
}

func (utm) Delete(context.Context, io.Writer, vmid.ID) error {
	return fmt.Errorf("--full does not delete UTM VMs (its AppleScript bridge is not wired for it); delete it in UTM")
}

func (utm) Notes(context.Context, vmid.ID) string { return "" }
func (utm) Managed() bool                         { return true }
func (utm) Deletable() bool                       { return false }

// utmCanonicalIP is the pinned vmnet address for a UTM VM: 192.168.64.<NNN>.
func utmCanonicalIP(id vmid.ID) string { return utmSubnet + "." + strconv.Itoa(int(id)) }

// utmCreate provisions a fresh UTM VM and returns its (pinned) IP, ready for
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
	canonIP := utmCanonicalIP(id)

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
	// sources into its own bundle on import, so we clean up after.
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
	netCfg := utmNetworkConfig(canonIP)
	fmt.Fprintf(out, "  ▶ writing cidata seed → %s\n", seedPath)
	if err := backend.MakeCidataISO(ctx, seedPath, opts.User, opts.PubKey, name, netCfg); err != nil {
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

	// Pin the fresh VM's host key from the very first contact, in the same
	// per-VM file adoption will use — the key recorded while cloud-init's output
	// is still on the UTM console carries through the whole lifecycle.
	t := host.Target{
		User: opts.User, Host: canonIP,
		KnownHostsFile: paths.EnsureKnownHosts(id), HostKeyAlias: id.Name(),
	}
	if !backend.WaitReachable(ctx, t, 300*time.Second) {
		return "", fmt.Errorf("UTM VM %s did not come up at %s within 5 min — cloud-init may still be running or have failed; open the UTM console to inspect", name, canonIP)
	}
	if err := backend.WaitCloudInitDone(ctx, out, t, 300*time.Second); err != nil {
		return "", err
	}

	// Detach the cidata CD cleanly: graceful shutdown → delete seed.iso on the
	// host → prune the now-zero-sized drive → restart.
	fmt.Fprintf(out, "  ▶ detaching cidata CD (shutdown → prune → restart) …\n")
	_, _ = t.Run(ctx, "sudo shutdown -h now")
	if err := waitVMStopped(ctx, name, 120*time.Second); err != nil {
		return "", err
	}
	_ = os.Remove(seedPath)
	if _, err := runOsascript(ctx, utmDetachZeroDrivesScript(name)); err != nil {
		return "", err
	}
	if _, err := runOsascript(ctx, utmStartScript(name)); err != nil {
		return "", err
	}
	if !backend.WaitReachable(ctx, t, 180*time.Second) {
		return "", fmt.Errorf("UTM VM %s did not come back at %s within 3 min after the cidata detach — inspect via UTM", name, canonIP)
	}

	ok = true
	_ = os.RemoveAll(staging)
	fmt.Fprintf(out, "  ▶ UTM VM ready: %s\n", canonIP)
	return canonIP, nil
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

// asQuote renders an AppleScript string literal, escaping backslash and quote.
func asQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// utmNetworkConfig is the cloud-init v2 network-config pinning the VM to its
// canonical vmnet address from boot one.
func utmNetworkConfig(ip string) string {
	gateway := utmSubnet + ".1"
	return fmt.Sprintf(`version: 2
ethernets:
  enp0s1:
    addresses: [%s/24]
    gateway4: %s
    nameservers:
      addresses: [%s]
`, ip, gateway, gateway)
}

func utmCreateScript(name string, memMiB, cpus int, diskPath, seedPath string) string {
	// Mirrors mpd/setup/macos-utm/lib/create-vm.sh: qemu, aarch64, shared
	// network, two drives (system disk + cidata seed).
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

// utmDetachZeroDrivesScript drops any drive whose host source file has vanished
// (host size == 0) — how the historical macos-utm flow prunes the cidata CD
// after the seed.iso is removed on the host.
func utmDetachZeroDrivesScript(name string) string {
	return fmt.Sprintf(`tell application "UTM"
	set vm to virtual machine named %s
	set config to configuration of vm
	set vmDrives to drives of config
	set keptDrives to {}
	repeat with vmDrive in vmDrives
		if (host size of vmDrive) is not 0 then
			set end of keptDrives to vmDrive
		end if
	end repeat
	set drives of config to keptDrives
	update configuration of vm with config
end tell`, asQuote(name))
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
