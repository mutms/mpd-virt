package backend

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/paths"
	"github.com/mutms/mpd-virt/go/internal/vmid"
)

// libvirt backend — a KVM VM on a Linux host, driven with virsh against the
// system daemon (docs/LIBVIRT.md for the one-time host prep). `create`
// materializes a VM from the amd64 Debian cloud qcow2 plus a cidata seed,
// like utm.go does on macOS; start/stop/state/delete are virsh verbs.
//
// Networking: libvirt's `default` NAT network is 192.168.122.0/24, so each
// mpd-<NNN> box is pinned (via cloud-init) to 192.168.122.<NNN>, gateway .1
// — which is also how locate finds it.

const (
	libvirtURI            = "qemu:///system"
	libvirtSubnet         = "192.168.122"
	libvirtDefaultDiskGiB = 80
	libvirtDefaultCPUs    = 4
)

// libvirtCanonicalIP is the pinned address for a libvirt box: 192.168.122.<NNN>.
func libvirtCanonicalIP(id vmid.ID) string { return libvirtSubnet + "." + strconv.Itoa(int(id)) }

func virsh(ctx context.Context, args ...string) (exec.Result, error) {
	return exec.Capture(ctx, exec.Cmd{Name: "virsh", Args: append([]string{"-c", libvirtURI}, args...)})
}

// libvirtCreate defines and boots a fresh VM, returning its pinned IP once
// cloud-init's first boot is done and sshd answers.
func libvirtCreate(ctx context.Context, out io.Writer, id vmid.ID, opts CreateOpts) (string, error) {
	name := id.Name()
	if libvirtDomainExists(ctx, name) {
		return "", fmt.Errorf("libvirt already has a VM named %s — `mpd-virt remove %s --full`, or `virsh undefine` it first", name, id.String())
	}
	dir := paths.LibvirtDir(name)
	if st, err := os.Stat(filepath.Dir(dir)); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s is missing — one-time host prep (docs/LIBVIRT.md):\n    sudo install -d -o $USER -g $USER -m 0755 %s", filepath.Dir(dir), filepath.Dir(dir))
	}
	memMiB := parseSizeMiB(opts.Memory)
	if memMiB == 0 {
		return "", fmt.Errorf("cannot parse --memory %q (use e.g. 8g or 8192m)", opts.Memory)
	}
	diskGiB := parseSizeGiB(opts.Disk)
	if diskGiB == 0 {
		diskGiB = libvirtDefaultDiskGiB
	}
	ip := libvirtCanonicalIP(id)

	base, err := ensureCloudQcow2(ctx, out)
	if err != nil {
		return "", err
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	diskPath := filepath.Join(dir, "disk.qcow2")
	seedPath := filepath.Join(dir, "seed.iso")

	// A full copy, not a backing file: the cache lives under ~ (0700), where
	// libvirt-qemu could not read it. Sparse, so only used blocks cost disk.
	fmt.Fprintf(out, "  ▶ disk %s (%d GB, from %s)\n", diskPath, diskGiB, cloudQcow2)
	if r, err := exec.Capture(ctx, exec.Cmd{Name: "qemu-img", Args: []string{"convert", "-f", "qcow2", "-O", "qcow2", base, diskPath}}); err != nil {
		return "", err
	} else if r.Failed() {
		return "", fmt.Errorf("qemu-img convert failed: %s", shortErr(r))
	}
	if r, err := exec.Capture(ctx, exec.Cmd{Name: "qemu-img", Args: []string{"resize", diskPath, fmt.Sprintf("%dG", diskGiB)}}); err != nil {
		return "", err
	} else if r.Failed() {
		return "", fmt.Errorf("qemu-img resize failed: %s", shortErr(r))
	}

	fmt.Fprintf(out, "  ▶ writing cidata seed → %s\n", seedPath)
	if err := makeCidataISO(ctx, seedPath, opts.User, opts.PubKey, name, libvirtNetworkConfig(ip)); err != nil {
		return "", err
	}

	xmlPath := filepath.Join(dir, "domain.xml")
	if err := os.WriteFile(xmlPath, []byte(libvirtDomainXML(name, id, memMiB, libvirtDefaultCPUs, diskPath, seedPath)), 0o644); err != nil {
		return "", err
	}
	fmt.Fprintf(out, "  ▶ virsh define %s (%d MiB, %d cpus)\n", name, memMiB, libvirtDefaultCPUs)
	if r, err := virsh(ctx, "define", xmlPath); err != nil {
		return "", err
	} else if r.Failed() {
		return "", fmt.Errorf("virsh define failed: %s", shortErr(r))
	}
	ok := false
	defer func() {
		if !ok {
			fmt.Fprintf(out, "  ⚠ create failed — removing half-built VM %s\n", name)
			_ = libvirtDelete(ctx, io.Discard, id)
		}
	}()

	fmt.Fprintf(out, "  ▶ virsh start %s (cloud-init runs on first boot) …\n", name)
	if r, err := virsh(ctx, "start", name); err != nil {
		return "", err
	} else if r.Failed() {
		return "", fmt.Errorf("virsh start failed: %s", shortErr(r))
	}

	t := host.Target{
		User: opts.User, Host: ip,
		KnownHostsFile: paths.EnsureKnownHosts(id), HostKeyAlias: name,
	}
	if !waitReachable(ctx, t, 300*time.Second) {
		return "", fmt.Errorf("VM %s did not come up at %s within 5 min — `virsh console %s` to inspect", name, ip, name)
	}
	if err := waitCloudInitDone(ctx, out, t, 300*time.Second); err != nil {
		return "", err
	}
	ok = true
	fmt.Fprintf(out, "  ▶ libvirt VM ready: %s\n", ip)
	return ip, nil
}

// libvirtNetworkConfig pins the box's address on the default network. The
// interface is matched by driver, not name, so it works whatever name the
// machine type gives it.
func libvirtNetworkConfig(ip string) string {
	gw := libvirtSubnet + ".1"
	return fmt.Sprintf(`version: 2
ethernets:
  primary:
    match:
      driver: virtio_net
    addresses: [%s/24]
    gateway4: %s
    nameservers:
      addresses: [%s]
`, ip, gw, gw)
}

// libvirtDomainXML is the VM definition — the one mpd's setup/linux flow
// ran for real (q35, host-passthrough, the timer set, memballoon), minus
// its spice display: Debian's qemu is built without spice. The <video>
// stays even headless: libvirt launches qemu with -nodefaults, and without
// a video device the kernel triple-faults under nested KVM before its
// first message — GRUB loops on "Booting Debian GNU/Linux". The MAC is
// derived from the number so the box keeps its address across re-creates.
func libvirtDomainXML(name string, id vmid.ID, memMiB, cpus int, diskPath, seedPath string) string {
	return fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <memory unit='MiB'>%d</memory>
  <currentMemory unit='MiB'>%d</currentMemory>
  <vcpu placement='static'>%d</vcpu>
  <os>
    <type arch='x86_64' machine='q35'>hvm</type>
    <boot dev='hd'/>
    <boot dev='cdrom'/>
  </os>
  <features>
    <acpi/>
    <apic/>
    <vmport state='off'/>
  </features>
  <cpu mode='host-passthrough' check='none' migratable='on'/>
  <clock offset='utc'>
    <timer name='rtc' tickpolicy='catchup'/>
    <timer name='pit' tickpolicy='delay'/>
    <timer name='hpet' present='no'/>
  </clock>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>restart</on_reboot>
  <on_crash>destroy</on_crash>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2' discard='unmap'/>
      <source file='%s'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='%s'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
    </disk>
    <interface type='network'>
      <mac address='52:54:00:00:00:%02x'/>
      <source network='default'/>
      <model type='virtio'/>
    </interface>
    <serial type='pty'><target type='isa-serial' port='0'/></serial>
    <console type='pty'><target type='serial' port='0'/></console>
    <channel type='unix'>
      <target type='virtio' name='org.qemu.guest_agent.0'/>
    </channel>
    <video>
      <model type='virtio' heads='1'/>
    </video>
    <memballoon model='virtio'>
      <stats period='10'/>
    </memballoon>
    <rng model='virtio'>
      <backend model='random'>/dev/urandom</backend>
    </rng>
  </devices>
</domain>
`, name, memMiB, memMiB, cpus, diskPath, seedPath, int(id))
}

// libvirtDomainExists reports whether virsh knows the domain.
func libvirtDomainExists(ctx context.Context, name string) bool {
	r, err := virsh(ctx, "dominfo", name)
	return err == nil && !r.Failed()
}

// libvirtState is virsh's state word ("running", "shut off", "paused"), or
// "" when the domain is unknown or virsh is absent.
func libvirtState(ctx context.Context, name string) string {
	r, err := virsh(ctx, "domstate", name)
	if err != nil || r.Failed() {
		return ""
	}
	return strings.TrimSpace(r.Stdout)
}

// libvirtPower runs one power verb: start, or a graceful ACPI shutdown for
// "stop". Best-effort, like the other backends.
func libvirtPower(ctx context.Context, out io.Writer, id vmid.ID, verb string) bool {
	if verb == "stop" {
		verb = "shutdown"
	}
	fmt.Fprintf(out, "  ▶ virsh %s %s\n", verb, id.Name())
	r, err := virsh(ctx, verb, id.Name())
	if err != nil {
		fmt.Fprintf(out, "    … virsh unavailable here (%v)\n", err)
		return false
	}
	if r.Failed() {
		fmt.Fprintf(out, "    … %s (continuing)\n", shortErr(r))
		return false
	}
	return true
}

// libvirtDelete destroys the VM and removes its files — the inverse of
// libvirtCreate. A running domain is stopped first.
func libvirtDelete(ctx context.Context, out io.Writer, id vmid.ID) error {
	name := id.Name()
	if libvirtState(ctx, name) == "running" {
		_, _ = virsh(ctx, "destroy", name)
	}
	if libvirtDomainExists(ctx, name) {
		fmt.Fprintf(out, "  ▶ virsh undefine %s\n", name)
		if r, err := virsh(ctx, "undefine", name); err != nil {
			return err
		} else if r.Failed() {
			return fmt.Errorf("virsh undefine %s failed: %s", name, shortErr(r))
		}
	}
	if err := os.RemoveAll(paths.LibvirtDir(name)); err != nil {
		return fmt.Errorf("removing %s: %w", paths.LibvirtDir(name), err)
	}
	return nil
}
