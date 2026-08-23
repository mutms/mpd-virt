package backend

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/paths"
)

// Cloud-init image cache + NoCloud seed-ISO generation, shared by backends
// that materialize a Debian VM from a cloud image (UTM today).
// Only the ~290 MB .tar.xz is cached (a slow link can't
// spare a 3 GB raw download); the raw disk inside is re-extracted per create
// — cheap on Apple Silicon, and a stray multi-GB raw on disk is more
// annoying than re-running tar. Everything shells to macOS built-ins —
// curl, tar, hdiutil — so nothing needs installing.

// Debian Trixie "generic", arm64. Bump in lockstep with the sibling
// mpd's image pin when refreshing — all three constants together: the
// SHA-512 comes from the SHA512SUMS file in the same dated directory, and
// pinning it means the archive that becomes every VM's operating system is
// exactly the published one, whatever a mirror or CDN served.
//
// "generic", NOT "genericcloud". genericcloud is built on Debian's cloud
// kernel, whose module tree contains no DRM drivers at all, so a VM built
// from it has no /dev/dri: the text console works, and anything graphical
// — gdm, a Wayland greeter — is a black screen with no useful error in
// any log. generic carries the full kernel and is still cloud-init driven,
// for roughly 90 MB more download.
const (
	cloudBase          = "https://cloud.debian.org/images/cloud/trixie/20260819-2575"
	cloudArchive       = "debian-13-generic-arm64-20260819-2575.tar.xz"
	cloudArchiveSHA512 = "2ddf5cb28ff545d47645a1860bcd9e62d08e97f8454b19986327f7cd728f9dd689e127c449ca273b7bc2a8c6ccca2168186da6eb6265010a5f1f4f6022693caf"
)

func cachedArchivePath() string { return filepath.Join(paths.CloudImages(), cloudArchive) }

// ensureBaseArchive downloads the Debian generic cloud-image archive into
// ~/.mpd-virt/conf/cloud-images/ on first use and returns its path.
// Idempotent: instant if already cached. The download streams to a
// .partial renamed on success, so an interrupted download never looks
// complete.
func ensureBaseArchive(ctx context.Context, out io.Writer) (string, error) {
	dst := cachedArchivePath()
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if err := os.MkdirAll(paths.CloudImages(), 0o755); err != nil {
		return "", err
	}
	partial := dst + ".partial"
	url := cloudBase + "/" + cloudArchive
	fmt.Fprintf(out, "  ▶ downloading %s (~290 MB, first create only) …\n", cloudArchive)
	// --proto '=https' pins the whole redirect chain to TLS — -L must never
	// be talked down to plaintext, wherever the mirror sends us.
	code, err := exec.Run(ctx, exec.Cmd{Name: "curl", Args: []string{
		"-L", "--fail", "--proto", "=https", "--progress-bar", "-o", partial, url,
	}})
	if err != nil {
		return "", err
	}
	if code != 0 {
		_ = os.Remove(partial)
		return "", fmt.Errorf("curl failed to download %s (exit %d)", url, code)
	}
	if err := verifySHA512(partial, cloudArchiveSHA512); err != nil {
		_ = os.Remove(partial)
		return "", fmt.Errorf("downloaded %s failed verification: %w", cloudArchive, err)
	}
	fmt.Fprintf(out, "  ✓ archive SHA-512 verified\n")
	if err := os.Rename(partial, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// verifySHA512 streams the file and compares its SHA-512 to want (lowercase
// hex). The archive becomes the OS of every VM this backend creates, so a
// mismatch — a truncated download, a tampering mirror — is fatal, never a
// warning.
func verifySHA512(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("SHA-512 mismatch:\n  got  %s\n  want %s", got, want)
	}
	return nil
}

// materializeDisk produces a per-VM disk at destPath: extract the raw from
// the cached archive, then grow it sparsely to diskGiB. Refuses to clobber
// destPath or to shrink below the extracted image.
func materializeDisk(ctx context.Context, out io.Writer, destPath string, diskGiB int) error {
	archive, err := ensureBaseArchive(ctx, out)
	if err != nil {
		return err
	}
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("destination disk already exists: %s", destPath)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}

	// Extract to a temp dir so we can find whatever raw the archive holds
	// (Debian ships disk.raw) and move it to destPath.
	tmp, err := os.MkdirTemp("", "mpd-virt-rawx-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	fmt.Fprintf(out, "  ▶ extracting %s → %s …\n", cloudArchive, destPath)
	if code, err := exec.Run(ctx, exec.Cmd{Name: "tar", Args: []string{"-xJf", archive, "-C", tmp}}); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("tar failed to extract %s (exit %d)", cloudArchive, code)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return err
	}
	var raw string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".raw") || strings.HasPrefix(e.Name(), "disk.") {
			raw = e.Name()
			break
		}
	}
	if raw == "" {
		return fmt.Errorf("no .raw disk image found inside %s", cloudArchive)
	}
	if err := os.Rename(filepath.Join(tmp, raw), destPath); err != nil {
		return err
	}

	targetBytes := int64(diskGiB) * 1024 * 1024 * 1024
	info, err := os.Stat(destPath)
	if err != nil {
		return err
	}
	if targetBytes < info.Size() {
		_ = os.Remove(destPath)
		return fmt.Errorf("requested disk size %d GB is smaller than the cloud image (%d GB) — pass a larger --disk",
			diskGiB, info.Size()/(1024*1024*1024))
	}
	if targetBytes > info.Size() {
		fmt.Fprintf(out, "  ▶ growing disk to %d GB (sparse)\n", diskGiB)
		// Truncate up extends the file with a hole — no space is written;
		// the guest's growpart + resize2fs claims it on first boot.
		if err := os.Truncate(destPath, targetBytes); err != nil {
			return err
		}
	}
	return nil
}

// makeCidataISO writes meta-data + user-data (+ optional network-config)
// into a temp dir and `hdiutil makehybrid`s it into an ISO labelled
// `cidata` — exactly what cloud-init's NoCloud datasource looks for.
func makeCidataISO(ctx context.Context, outputPath, username, sshPubKey, localHostname, networkConfig string) error {
	work, err := os.MkdirTemp("", "mpd-virt-cidata-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	files := map[string]string{
		"meta-data": cidataMetaData(localHostname),
		"user-data": cidataUserData(username, sshPubKey, localHostname),
	}
	if networkConfig != "" {
		files["network-config"] = networkConfig
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o644); err != nil {
			return err
		}
	}

	if parent := filepath.Dir(outputPath); parent != "" {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}
	_ = os.Remove(outputPath)

	// macOS has hdiutil built in; Linux uses genisoimage (docs/LIBVIRT.md).
	cmd := exec.Cmd{Name: "hdiutil", Args: []string{
		"makehybrid", "-o", outputPath, "-iso", "-joliet", "-default-volume-name", "cidata", work,
	}}
	if runtime.GOOS != "darwin" {
		cmd = exec.Cmd{Name: "genisoimage", Args: []string{
			"-output", outputPath, "-volid", "cidata", "-joliet", "-rock", work,
		}}
	}
	r, err := exec.Capture(ctx, cmd)
	if err != nil {
		return err
	}
	if r.Failed() {
		return fmt.Errorf("%s failed (exit %d): %s", cmd.Name, r.Code, shortErr(r))
	}
	return nil
}

// Debian Trixie "generic", amd64, as qcow2 — the libvirt backend's base.
// Same dated directory as the arm64 archive; bump the three together.
const (
	cloudQcow2       = "debian-13-generic-amd64-20260819-2575.qcow2"
	cloudQcow2SHA512 = "ae204682c015fd026838b71f1ce82585368dbb8c050b779ffd8a21a90a6c94f20648133dd078ee8fca9f0aa956e6901a943899be69ee24480035da6aeecd4f68"
)

// ensureCloudQcow2 downloads and verifies the amd64 qcow2 into the cache on
// first use and returns its path — ensureBaseArchive's twin.
func ensureCloudQcow2(ctx context.Context, out io.Writer) (string, error) {
	dst := filepath.Join(paths.CloudImages(), cloudQcow2)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if err := os.MkdirAll(paths.CloudImages(), 0o755); err != nil {
		return "", err
	}
	partial := dst + ".partial"
	url := cloudBase + "/" + cloudQcow2
	fmt.Fprintf(out, "  ▶ downloading %s (~400 MB, first create only) …\n", cloudQcow2)
	code, err := exec.Run(ctx, exec.Cmd{Name: "curl", Args: []string{
		"-L", "--fail", "--proto", "=https", "--progress-bar", "-o", partial, url,
	}})
	if err != nil {
		return "", err
	}
	if code != 0 {
		_ = os.Remove(partial)
		return "", fmt.Errorf("curl failed to download %s (exit %d)", url, code)
	}
	if err := verifySHA512(partial, cloudQcow2SHA512); err != nil {
		_ = os.Remove(partial)
		return "", fmt.Errorf("downloaded %s failed verification: %w", cloudQcow2, err)
	}
	fmt.Fprintf(out, "  ✓ image SHA-512 verified\n")
	if err := os.Rename(partial, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// cidataMetaData is the NoCloud meta-data: instance id + a first-boot
// hostname. Seeding the final mpd-<NNN> name here means adoption's hostname
// assert passes on boot one.
func cidataMetaData(localHostname string) string {
	return fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", localHostname, localHostname)
}

// cidataUserData is the #cloud-config: create only the dev user (no
// `debian` default) with passwordless sudo and key-only auth, grow the
// rootfs to fill the disk we extended, and start sshd, so the VM comes
// up adoption-ready.
func cidataUserData(username, sshPubKey, localHostname string) string {
	return fmt.Sprintf(`#cloud-config
hostname: %s
manage_etc_hosts: true

users:
  - name: %s
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    lock_passwd: true
    ssh_authorized_keys:
      - %s

ssh_pwauth: false

growpart:
  mode: auto
  devices: ['/']

resize_rootfs: true

runcmd:
  - systemctl enable --now ssh
`, localHostname, username, sshPubKey)
}
