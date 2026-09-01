package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"

	"github.com/mutms/mpd-virt/go/internal/host"
	"github.com/mutms/mpd-virt/go/internal/paths"
)

// The developer's own general-purpose environment, carried from the Mac into
// every VM mpd-virt owns: ~/.mpd-virt/vm.env → the VM's shells. It lands in
// mpd's /var/lib/mpd/env, and mpd sources it from ~/.bashrc. Ambient
// environment, not part of mpd's mpd.env config layering.
//
// Scoped to the *developer*, not the VM: a developer routinely runs several
// VMs that should agree on how they behave. Holding the file here and
// pushing it is what makes one edit reach all of them.
//
// Pushed as the dev user, not root: /var/lib/mpd/env is dev-owned, and mpd's
// own --vm-setup only ensures that directory exists (it seeds nothing), so
// the two writers never fight.
type envFile struct {
	local  string // path on the Mac (absent = nothing to push)
	remote string // path in the VM
	name   string // for the pass/warn messages
}

func envFiles() []envFile {
	return []envFile{
		{paths.VMEnv(), "/var/lib/mpd/env/vm.env", "vm.env"},
	}
}

// pushEnvFile copies one env file to the VM when the Mac's copy differs from
// what is there, reporting whether it changed anything.
//
// No file on the Mac is "nothing to do", not "remove it from the VM" — the
// same rule the assets overlay follows, and here it also protects a sandbox
// VM adopted later: its own hand-written file survives until the developer
// actually puts one on the Mac. Digest-guarded like the LAN hosts file: this
// runs on every lifecycle verb, and the common case is that nothing moved.
func pushEnvFile(ctx context.Context, t host.Target, f envFile) (bool, error) {
	body, err := os.ReadFile(f.local)
	if err != nil {
		return false, nil
	}
	want := fmt.Sprintf("%x", sha256.Sum256(body))
	// `|| true` so a missing remote file is an empty digest rather than an
	// error: absent and stale are the same case, both "push it".
	got, err := t.Line(ctx, "sha256sum "+f.remote+" 2>/dev/null | cut -d' ' -f1 || true")
	if err != nil {
		return false, err
	}
	if got == want {
		return false, nil
	}
	if err := t.Install(ctx, f.local, f.remote, "0644"); err != nil {
		return false, err
	}
	return true, nil
}

// syncEnv is the best-effort wrapper the lifecycle verbs use. Like the assets
// overlay, these are the developer's own material: failing to carry one is
// never a reason to fail an adoption, a start, or an update. Nothing needs
// restarting after — it takes effect in the next shell that sources it.
func syncEnv(ctx context.Context, t host.Target, idPad string) {
	for _, f := range envFiles() {
		pushed, err := pushEnvFile(ctx, t, f)
		if err != nil {
			fmt.Printf("  ⚠ %s push failed: %v\n    retry with: mpd-virt update %s\n", f.name, err, idPad)
			continue
		}
		if pushed {
			pass(f.name + " pushed → " + f.remote)
		}
	}
}
