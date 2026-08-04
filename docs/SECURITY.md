# Security: the host-side trust model

The model is "the Mac is the trust origin; the VM is disposable":

| Asset | Lives on | Compromise impact |
|---|---|---|
| mpd root CA private key | Mac (`~/.mpd-virt/conf/caroot/`) | Can sign arbitrary `*.mpd.test` certs (name-constrained; limited blast radius) |
| SSH private key | Mac (`~/.ssh/`) | Root in any mpd VM (the dev user has passwordless sudo) |

A Mac compromise gives you everything.

A *VM* compromise (e.g. via a malicious project) does not climb back to
the Mac: the root CA's private key is not on the VM (only the name-constrained
per-VM intermediate is), and SSH is one-way — the VM holds no keys to reach
the Mac.

This is the host-side half of the boundary only. The VM-side reachability
boundary is documented where it is implemented (the mpd repo's
`docs/SECURITY.md` and mpd-proxy).

## Why two CAs

The root's private key never leaves the Mac. Each VM instead gets its own
intermediate, name-constrained to that VM's zone alone
(`permitted;DNS:<NNN>.mpd.test`, `pathlen:0`), which the in-VM `mpd` uses to
sign its service and project certificates:

```
mpd Root CA                        key: this Mac, and only this Mac
└── mpd VM 200 CA                  key: pushed to VM 200
      permitted;DNS:200.mpd.test
      └── 200.mpd.test, moodle.200.mpd.test, …   signed inside the VM
```

So a compromised VM can forge names in its own zone and nowhere else — not
another VM's zone, and not names issued directly under `mpd.test`. The root's
own `permitted;DNS:mpd.test` constraint means trusting it can vouch for no
domain outside `*.mpd.test`, which is what makes System-Keychain trust safe.
The VM CA lives under `~/.mpd-virt/<NNN>/` rather than `conf/` because that
is its lifetime: `delete` takes it with the VM, and a re-created VM at the
same id gets a fresh one. Its validity is capped by whatever the root has
left, since nothing may outlive its issuer.

LAN machines that are not VMs — `forge.mpd.test`, `runner.mpd.test`, … — get
leaves signed directly by the root on the Mac; see
[`LAN_SERVERS.md`](LAN_SERVERS.md).

## Backing up the CA

Losing `~/.mpd-virt/conf/caroot/` means regenerating the CA, re-trusting it
on the Mac, and re-pushing it to every VM. There is deliberately no
export/import-identity flow: Time Machine catches `~/.mpd-virt/conf/` by
default when enabled, and that is the backup. It matters more than the VMs
themselves — the LAN-server certificates under `~/.mpd-virt/servers/` are
installed on machines that cannot be rebuilt from a script (see
[`LAN_SERVERS.md`](LAN_SERVERS.md)).

## Known gaps

- **No CA rotation yet.** The root CA is valid for 365 days
  (`go/internal/ca/ca.go` — macOS caps user-trusted root lifetimes), and
  expiry today only produces an error; the only recovery is regenerating the
  root, re-trusting it, and re-adopting every VM. The planned fix is a
  `mpd-virt refresh-trust <NNN>` verb (paired with an in-VM
  `mpd --vm-refresh-trust`) that pushes the fresh root, regenerates the
  per-VM intermediate and everything signed under it, and refreshes the
  trust stores — non-destructive to project state.
