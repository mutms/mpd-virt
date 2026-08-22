package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mutms/mpd-virt/go/internal/backend"
	"github.com/mutms/mpd-virt/go/internal/vmid"
	"github.com/spf13/cobra"
)

// createCmd provisions a brand-new box through its backend and then adopts it
// with the adoption flow. For --backend=container it runs the base image, seeds
// identity over `container exec`, reads the leased IP, and hands off — image to
// adopted box in one command.
func createCmd() *cobra.Command {
	var username, backendFlag, image, memory, disk, pubkeyPath string
	cmd := &cobra.Command{
		Use:   "create <NNN> --backend=<backend>",
		Short: "Create a fresh box from its backend's base image and adopt it",
		Long: "Creates a fresh box on its backend and adopts it. Backends: container\n" +
			"(Apple container, base image --image), utm (Debian cloud image,\n" +
			"--memory, --disk) and proxmox (full clone of the mpd-template VM,\n" +
			"TEMPLATE_VMID in proxmox.env). parallels boxes are created by hand and adopted.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := vmid.Parse(args[0])
			if err != nil {
				return err
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
			return runAdopt(cmd.Context(), id, ip, username, be)
		},
	}
	cmd.Flags().StringVar(&username, "username", defaultUser(), "dev user to create on the box")
	cmd.Flags().StringVar(&backendFlag, "backend", "", "platform to create on ("+backend.List()+") — required")
	cmd.Flags().StringVar(&image, "image", backend.DefaultContainerImage(), "base image to run — container backend")
	cmd.Flags().StringVar(&memory, "memory", "10g", "memory: container --memory, or VM RAM (utm)")
	cmd.Flags().StringVar(&disk, "disk", "", "VM disk size for utm, e.g. 80g (default 80g; ignored by container)")
	cmd.Flags().StringVar(&pubkeyPath, "pubkey", "", "public key to authorize (default ~/.ssh/id_ed25519.pub, then id_rsa.pub)")
	_ = cmd.MarkFlagRequired("backend")
	return cmd
}

// readPubKey loads the public key to seed into the new box. An explicit --pubkey
// path wins; otherwise it tries the usual ~/.ssh defaults — the key ssh will
// present to the box when adoption connects.
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
