# LAN servers under `mpd.test`

How machines on your local network that are **not** mpd VMs — a hypervisor's
web interface, a Git server, a CI runner, a NAS, anything you reach over
HTTPS — get names in the `mpd.test` tree, certificates that everything in this
setup already trusts, and DNS that answers identically from the Mac, from
every mpd VM, and from every container inside those VMs.

`mpd-virt` issues the material and publishes the names. It never logs into
those machines and knows nothing about what runs on them: they are not VMs it
created, and how you administer your own servers is your business. Where a
certificate goes and what to restart afterwards belongs in each machine's own
documentation.

The names and addresses used below are **examples**, so the commands stay
concrete rather than full of placeholders. Yours will differ — nothing here is
a required layout, and the only constraints mpd-virt actually enforces are the
naming rules in the next section.

## Why bother

Everything in this setup already trusts the mpd root CA: the Mac's System
Keychain, every VM's three trust stores, every runtime container. Giving a LAN
machine a name under `mpd.test` lets it join that trust relationship instead
of standing outside it — scripting a hypervisor's REST API, or a CI runner
cloning over HTTPS, with no `--insecure`, no `GIT_SSL_NO_VERIFY`, and no
custom CA bundle baked into container images.

It is safe to hand out these names because no VM can sign for them. The root's
private key stays on the Mac; each VM signs with an intermediate constrained
to its own zone, so a compromised VM can forge names in `<NNN>.mpd.test` and
nowhere else. See `README.md` for that chain.

## The naming rule

`mpd.test` holds two kinds of name, told apart by position:

```
mpd.test
├── 126.mpd.test          ← a VM zone: 3-digit first label
│   └── m45.126.mpd.test  ← signed inside VM 126, by its own constrained CA
├── forge.mpd.test        ← a LAN service: any non-numeric label
├── kitchenbox.mpd.test   ← named for the machine, not the software it runs
└── runner.mpd.test
```

`mpd-virt server add` enforces it: a 3-digit name is refused because it
would shadow a VM zone that VM's own CA owns, and a name outside
`mpd.test` is refused because the root CA is name-constrained and could
not sign it.

## Registering a server

Example — substitute your own names and addresses:

```sh
mpd-virt server add kitchenbox --ip 10.1.10.1
mpd-virt server add forge      --ip 10.1.10.100
mpd-virt server add runner     --ip 10.1.10.101
mpd-virt server list
```

State lands in `~/.mpd-virt/servers/<name>/` — an `env` file with the
address, and later the certificate beside it. Name and address is all the
registry holds; there is no field for what the machine runs.

**Name it after the machine, not the software.** The name becomes the DNS
name and the certificate's CN, so it should match the host's own hostname —
a box called `kitchenbox` that happens to run Proxmox is `kitchenbox`, not
`proxmox`. Making the DNS name disagree with the node's hostname means
keeping two names in step forever.

## Issuing a certificate

```sh
mpd-virt server cert forge
mpd-virt server cert forge --san www     # extra DNS names under mpd.test
```

Signed **directly by the root**, not via an intermediate: the signing
happens on the Mac where the root key already is, so an intermediate
would add a chain hop and one more file to install for no gain.

Validity is `min(397, days the root has left)`. 397 because macOS rejects
leaves of 398 days or more; the root cap because nothing may outlive its
issuer — a certificate valid past its CA's expiry stops verifying on the
CA's date while still reading as valid, which is a miserable thing to
debug.

Re-issuing is the same command. It refuses to replace a certificate with
more than 30 days left unless you pass `--force`.

> **The root CA is only valid for 365 days.** macOS rejects long-lived
> user-trusted roots, so this rotates annually — and when it does, every
> LAN certificate must be re-issued and redeployed:
> `mpd-virt server cert <name> --force`, then reinstall each.

**DNS names only — no IP SANs.** The root constrains `dNSName` to `mpd.test`,
but under RFC 5280 a name type absent from `permittedSubtrees` is
*unconstrained*, so an `iPAddress` SAN would be the one field the constraint
does not cover. Reach these hosts by name; that is what the DNS below is for.

## Installing it on the server

mpd-virt does not do this and prints no recipe for it. Where the certificate
goes, what owns it, and what to restart differs per machine and changes over
time — a copy of that inside mpd-virt would only be a staler second copy of
each machine's own runbook.

Two things that are the same everywhere:

- The **root CA** goes on every one of these machines, whether or not it
  serves TLS — it is what lets the machine *verify* the others.
  `~/.mpd-virt/conf/caroot/rootCA.pem` is the file, at a stable path, so
  there is no export step.
- On Debian-family hosts it must land with a **`.crt`** extension in
  `/usr/local/share/ca-certificates/`. `update-ca-certificates` reads only
  `*.crt`, so a `.pem` is silently ignored and the CA never takes effect.
  `scp` renames in flight, so this costs nothing.

A host that only dials out — a CI runner, a scripted API client — needs the
root and nothing else. `server cert` is for machines that listen.

## DNS: three resolvers, three mechanisms

| Where | `126.mpd.test` | `forge.mpd.test` |
|---|---|---|
| The Mac | VM 126's dnsmasq, via mpd-proxy (`/etc/resolver/mpd.test`) | `/etc/hosts` |
| Inside a VM | its own dnsmasq | its own dnsmasq |
| Inside a container | the VM's dnsmasq | the VM's dnsmasq |

### The Mac — by hand, deliberately

The Mac has a single `/etc/resolver/mpd.test` that hands every
`*.mpd.test` name to mpd-proxy, which forwards each VM's zone to that VM's
dnsmasq through the tunnel. `forge.mpd.test` would match it too — but
`/etc/hosts` is consulted *before* the resolver, so a hand-added
`forge.mpd.test` line short-circuits there and never reaches mpd-proxy.
That is the split: VM zones resolve dynamically via mpd-proxy, LAN servers
statically via `/etc/hosts`.

```sh
mpd-virt server list --etc-hosts | sudo tee -a /etc/hosts
sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder
```

`mpd-virt` will not write that file for you: it needs `sudo`, other tools
edit it too, and an ownership marker in it would need its own uninstall
path. `server list` reports which entries are missing.

### VMs and containers — automatic

```sh
mpd-virt server sync            # or one VM: mpd-virt server sync 126
```

This writes `~/.mpd-virt/conf/lan-hosts`, scp's it to
`/var/lib/mpd/conf/lan-hosts` on each running VM, and runs
`mpd --vm-setup`, which republishes it through dnsmasq as
`<stateDir>/dns/lan.hosts`. VMs that are down are reported and skipped.

`sync` is how you publish a *changed* registry to boxes that are already
running. It is not the only path: `takeover` and `create` push the file
before their first `mpd --vm-setup`, `update` pushes it before
`99-update.sh` (which re-runs vm-setup), and `start` pushes it and
republishes when it differs. So a freshly adopted VM answers for the LAN
names from the start, and a VM that was down while you added a server
picks them up on its next `start` — no separate sync to remember. Each of
those compares a remote `sha256sum` first, so the usual unchanged case
costs one remote command and republishes nothing.

Containers inherit this for free — they resolve through the VM's dnsmasq
at the bridge gateway and have no `/etc/hosts` of their own. That is the
point of the exercise:

```sh
ssh mpd-126 'podman run --rm --network mpd-internal alpine \
    getent hosts forge.mpd.test'
# 10.1.10.100  forge.mpd.test
```

The VM reaches the LAN directly — Parallels Shared and UTM Shared both route
to it — so nothing else is needed for the path.

## Verifying

```sh
# The certificate chains to the root and macOS accepts it:
openssl verify -CAfile ~/.mpd-virt/conf/caroot/rootCA.pem \
    ~/.mpd-virt/servers/forge/cert.pem
security verify-cert -c ~/.mpd-virt/servers/forge/cert.pem \
    -p ssl -s forge.mpd.test

# The server serves it:
openssl s_client -connect forge.mpd.test:443 -servername forge.mpd.test \
    </dev/null 2>/dev/null | grep -E 'Verify return code'
curl -sS -o /dev/null -w '%{http_code}\n' https://forge.mpd.test/

# A container inside a VM agrees:
ssh mpd-126 'podman run --rm --network mpd-internal alpine \
    getent hosts forge.mpd.test'
```

## Removing one

```sh
mpd-virt server delete forge
mpd-virt server sync            # retract the name inside the VMs
```

`delete` deletes the certificate and key along with the registry entry —
keeping key material for a machine nothing tracks is how an
unaccounted-for private key ends up on disk. It cannot remove the copy
already installed on the server; do that there.

## Notes and limits

- **The root can still sign for any IP.** It constrains `dNSName` only.
  mpd-virt never issues an `iPAddress` SAN, but narrowing the root itself
  means adding `permitted;IP:<your LAN>/24`, which costs a regeneration and
  a re-trust everywhere; the annual rotation is when to do it.
- **No revocation.** No CRL, no OCSP. Certificates expire; to retire one
  early, remove it from the server.
- **Back up `~/.mpd-virt/conf/`.** Losing it costs more than rebuilding
  VMs: these certificates are installed on machines that cannot be
  rebuilt from a script. Time Machine catches it by default when
  enabled — see `SECURITY.md`.
