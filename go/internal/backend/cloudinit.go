package backend

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/paths"
)

// Cloud-init image cache + NoCloud seed-ISO generation, shared by backends
// that materialize a Debian VM from a cloud image (UTM today).
// Only the ~200 MB .tar.xz is cached (a slow link can't
// spare a 3 GB raw download); the raw disk inside is re-extracted per create
// — cheap on Apple Silicon, and a stray multi-GB raw on disk is more
// annoying than re-running tar. Everything shells to macOS built-ins —
// curl, tar, hdiutil — so nothing needs installing.

// Debian Trixie generic-cloud, arm64. Bump in lockstep with the sibling
// mpd's image pin when refreshing.
const (
	cloudBase    = "https://cloud.debian.org/images/cloud/trixie/20260722-2547"
	cloudArchive = "debian-13-genericcloud-arm64-20260722-2547.tar.xz"
)

func cachedArchivePath() string { return filepath.Join(paths.CloudImages(), cloudArchive) }

// ensureBaseArchive downloads the Debian generic-cloud archive into
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
	fmt.Fprintf(out, "  ▶ downloading %s (~200 MB, first create only) …\n", cloudArchive)
	code, err := exec.Run(ctx, exec.Cmd{Name: "curl", Args: []string{
		"-L", "--fail", "--progress-bar", "-o", partial, url,
	}})
	if err != nil {
		return "", err
	}
	if code != 0 {
		_ = os.Remove(partial)
		return "", fmt.Errorf("curl failed to download %s (exit %d)", url, code)
	}
	if err := os.Rename(partial, dst); err != nil {
		return "", err
	}
	return dst, nil
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

	r, err := exec.Capture(ctx, exec.Cmd{Name: "hdiutil", Args: []string{
		"makehybrid", "-o", outputPath,
		"-iso", "-joliet",
		"-default-volume-name", "cidata",
		work,
	}})
	if err != nil {
		return err
	}
	if r.Failed() {
		return fmt.Errorf("hdiutil makehybrid failed (exit %d): %s", r.Code, shortErr(r))
	}
	return nil
}

// cidataMetaData is the NoCloud meta-data: instance id + a first-boot
// hostname. Seeding the final mpd-<NNN> name here means takeover's hostname
// assert passes on boot one.
func cidataMetaData(localHostname string) string {
	return fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", localHostname, localHostname)
}

// cidataUserData is the #cloud-config: create only the dev user (no
// `debian` default) with passwordless sudo and key-only auth, grow the
// rootfs to fill the disk we extended, and start sshd, so the box comes
// up takeover-ready.
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
