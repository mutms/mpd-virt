# Threat model + CA backup (host trust origin)

*Salvaged from the pruned `macos-host-state.md` proposal — the surrounding design is implemented/superseded; this is the part that is not yet built.*

## Threat model

The model is "the Mac is the trust origin; the VM is disposable":

| Asset | Lives on | Compromise impact |
|---|---|---|
| mpd CA private key | Mac (`~/.mpd-virt/conf/caroot/`) | Can sign arbitrary `*.mpd.test` certs (name-constrained; limited blast radius) |
| SSH private key | Mac (`~/.ssh/`) | Root in any mpd VM (dev user has passwordless sudo) |

A Mac compromise gives you everything.

A *VM* compromise (e.g. via a malicious project) does not climb back to
the Mac: the CA private key is not on the VM (only the cert is), and SSH
is one-way — the VM holds no keys to reach the Mac.

This is the host-side half of the boundary only. The VM-side reachability
boundary is documented where it is implemented (the mpd repo's
`docs/SECURITY.md` and the mpd-proxy design), not here.

## Open question: should the CA be backed up?

Losing `~/.mpd-virt/conf/caroot/` means regenerating the CA and
re-trusting it on the Mac, plus re-pushing to every VM. Worth an
`mpd-virt export-identity` / `import-identity` flow? Probably defer —
Time Machine catches `~/.mpd-virt/conf/` by default when enabled.
