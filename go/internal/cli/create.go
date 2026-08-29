package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/backends"
	"github.com/mutms/mpd-virt/go/internal/vmid"
	"github.com/spf13/cobra"
)

// createCmd provisions a brand-new VM through its backend and then adopts it
// with the adoption flow. For --backend=container it runs the base image, seeds
// identity over `container exec`, reads the leased IP, and hands off — image to
// adopted VM in one command.
func createCmd() *cobra.Command {
	var username, backendFlag, image, memory, disk, pubkeyPath string
	cmd := &cobra.Command{
		Use:   "create <NNN> --backend=<backend>",
		Short: "Create a fresh VM from its backend's base image and adopt it",
		Long: "Creates a fresh VM on its backend and adopts it. Backends: container\n" +
			"(Apple container, base image --image), utm (Debian cloud image,\n" +
			"--memory, --disk), proxmox (full clone of the mpd-template VM,\n" +
			"template_vmid in backends/proxmox.json) and libvirt (KVM on a Linux host, Debian\n" +
			"cloud image, --memory, --disk). parallels VMs are created by hand and adopted.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := vmid.Parse(args[0])
			if err != nil {
				return err
			}
			// Fall back to the configured default_backend when --backend is
			// omitted, matching adopt; Parse still rejects an empty backend when
			// none is configured.
			if backendFlag == "" {
				def, err := configuredDefaultBackend()
				if err != nil {
					return err
				}
				if def != "" {
					backendFlag = string(def)
					fmt.Printf("backend %s (default from config.json)\n", backendFlag)
				}
			}
			be, err := backend.Parse(backendFlag)
			if err != nil {
				return err
			}
			pubkey, err := readPubKey(pubkeyPath)
			if err != nil {
				return err
			}
			ip, err := backend.Create(cmd.Context(), cmd.OutOrStdout(), id, be, backend.CreateOpts{
				Image: image, Memory: memory, Disk: disk, User: username, PubKey: pubkey,
			})
			if err != nil {
				return err
			}
			fmt.Printf("created %s → %s\n", id.Name(), ip)
			if err := runAdopt(cmd.Context(), id, ip, username, be); err != nil {
				return err
			}
			// Only on create, never on adopt: this VM was built here a
			// minute ago and its key is already pinned, so writing the
			// developer's known_hosts hands over a trust decision that has
			// been made rather than making a new one. See knownhosts.go.
			pinHostKeys(cmd.Context(), vmTarget(id, username, ip), id)
			return nil
		},
	}
	cmd.Flags().StringVar(&username, "username", defaultUser(), "dev user to create on the VM")
	cmd.Flags().StringVar(&backendFlag, "backend", "", "platform to create on ("+backend.List()+") — required unless default_backend is set in config.json")
	cmd.Flags().StringVar(&image, "image", backends.DefaultContainerImage(), "base image to run — container backend")
	cmd.Flags().StringVar(&memory, "memory", "10g", "memory: container --memory, or VM RAM (utm, libvirt)")
	cmd.Flags().StringVar(&disk, "disk", "", "VM disk size for utm/libvirt, e.g. 80g (default 80g; ignored by container)")
	cmd.Flags().StringVar(&pubkeyPath, "pubkey", "", "public key to authorize (default ~/.ssh/id_ed25519.pub, then id_rsa.pub)")
	return cmd
}

// readPubKey loads the public key to seed into the new VM. An explicit --pubkey
// path wins; otherwise it tries the usual ~/.ssh defaults — the key ssh will
// present to the VM when adoption connects.
func readPubKey(path string) (string, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		for _, name := range []string{"id_ed25519.pub", "id_rsa.pub", "id_ecdsa.pub"} {
			p := filepath.Join(home, ".ssh", name)
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
		if path == "" {
			return "", fmt.Errorf("no default SSH public key in ~/.ssh — pass one with --pubkey")
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading public key %s: %w", path, err)
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", fmt.Errorf("public key %s is empty", path)
	}
	return key, nil
}
