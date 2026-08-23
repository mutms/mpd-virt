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
the Mac. The few strings a VM reports back (its hostname, file digests, the
wg0 public key) are consumed inertly: external commands on the Mac go through
`internal/exec`'s allow-list with argument vectors (no shell), captured output
is scrubbed of terminal escape sequences before anything prints or parses it,
the wg key is validated to be exactly a key before it reaches the
root-privileged mpd-proxy, and every discovered address must parse as a
literal IPv4 address before it may enter the registry or `~/.ssh/config`
(which validates again at the sink).

This is the host-side half of the boundary only. The VM-side reachability
boundary is documented where it is implemented (the mpd repo's
`docs/security.md` and mpd-proxy).

## Host key continuity

The VM's ssh host key is what proves the machine at an address is the VM
that was adopted — key auth cannot stand in for it (a rogue endpoint can
accept an authentication it never verified), and adoption pushes CA material
to whatever answers. So the key is pinned: first contact (adopt/create)
records it into `~/.mpd-virt/<NNN>/known_hosts`, adopt prints the
fingerprint for comparison against the VM's console, and every later
connection — mpd-virt's own verbs and the managed `~/.ssh/config` aliases
alike — refuses a changed key (`StrictHostKeyChecking accept-new` against
the per-VM file). The pin is stored under the stable alias `mpd-<NNN>`
(`HostKeyAlias`), so a VM that moves to a new DHCP lease keeps its
continuity instead of getting a fresh trust-on-first-use.

A legitimately re-keyed VM (rebuilt, rolled back to a snapshot) is the one
case the refusal message names, with the exact `ssh-keygen -R` to run;
`remove` retires the pin with the VM.

## What rides the WireGuard overlay

The overlay is Mac → VM by intent, but WireGuard has no notion of direction —
which would leave every Mac listener bound to 0.0.0.0 (dev servers, debug
ports) reachable from a compromised VM. mpd-proxy therefore filters inbound
tunnel traffic down to replies inside its own process (its `filter.go`, on
the utun between WireGuard's decrypt and the kernel — no pf rules, nothing
else to install): VM-initiated TCP connections and inbound probes are
dropped; only responses to traffic the Mac originated pass. The SOCKS tier
has no equivalent exposure — a `DynamicForward` opens no server-to-client
channels.

Two consequences worth knowing. The SOCKS browser proxies *everything*
(remote DNS included) through the compromised-by-assumption VM — full
visibility, plain-HTTP tampering — so "a dedicated browser" is load-bearing,
not a convenience. And an agent or IDE is only inside the boundary when it
runs inside the VM: an AI agent running on the Mac that merely SSHes into
the runtime keeps its Mac-side tools, and a prompt injection in hostile
project code walks straight back out through them. Run the agent binary in
the runtime container.

## Supply-chain pins

What executes on a VM before mpd is built there is pinned: mpd's three
bootstrap scripts are fetched at a commit hash (`bootstrapRef` in
`internal/cli/adopt.go`, mirrored by `MPD_BOOTSTRAP_REF` in `container/Containerfile`), and the Debian cloud image the utm backend
downloads is verified against a pinned SHA-512 (`internal/backend/
cloudinit.go`) over an https-only redirect chain. The mpd checkout itself —
and `update` — deliberately track `mutms/mpd` main: that repo is part of the
trusted computing base, and the checkout leaves auditable history on the VM
that pipe-to-bash never would. Bump both pins deliberately, together with a
review of what changed.

Everything under `~/.mpd-virt` is owner-only (0700/0600, re-asserted on
every run); nothing there — CA keys, the proxmox API token, pinned host
keys — is any other user's to read.

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
is its lifetime: `remove` takes it with the adoption, and a re-adopted VM at the
same id gets a fresh one. Its validity is capped by whatever the root has
left, since nothing may outlive its issuer.

LAN machines that are not VMs — `forge.mpd.test`, `runner.mpd.test`, … — get
leaves signed directly by the root on the Mac; see
[`lan-servers.md`](lan-servers.md).

## Backing up the CA

Losing `~/.mpd-virt/conf/caroot/` means regenerating the CA, re-trusting it
on the Mac, and re-pushing it to every VM. There is deliberately no
export/import-identity flow: Time Machine catches `~/.mpd-virt/conf/` by
default when enabled, and that is the backup. It matters more than the VMs
themselves — the LAN-server certificates under `~/.mpd-virt/servers/` are
installed on machines that cannot be rebuilt from a script (see
[`lan-servers.md`](lan-servers.md)).

## Known gaps

- **No CA rotation yet.** The root CA is valid for 365 days
  (`go/internal/ca/ca.go` — macOS caps user-trusted root lifetimes), and
  expiry today only produces an error; the only recovery is regenerating the
  root, re-trusting it, and re-adopting every VM. The planned fix is a
  `mpd-virt refresh-trust <NNN>` verb (paired with an in-VM
  `mpd --vm-refresh-trust`) that pushes the fresh root, regenerates the
  per-VM intermediate and everything signed under it, and refreshes the
  trust stores — non-destructive to project state.
