# LAN servers under `mpd.test`

How machines on your local network that are **not** mpd VMs — a hypervisor's
web interface, a Git server, a CI runner, a NAS, anything you reach over
HTTPS — get names in the `mpd.test` tree, certificates that everything in this
setup already trusts, and DNS that answers identically from the Mac, from
every mpd VM, and from every container inside those VMs.

`mpd-virt` issues the material and tells you what to install. It never logs
into those machines: they are not VMs it created, and how you administer your
own servers is your business.

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
├── kitchenbox.mpd.test   ← named for the machine; --kind selects the recipe
└── runner.mpd.test
```

`mpd-virt server add` enforces it: a 3-digit name is refused because it
would shadow a VM zone that VM's own CA owns, and a name outside
`mpd.test` is refused because the root CA is name-constrained and could
not sign it.

## Registering a server

Example — substitute your own names and addresses:

```sh
mpd-virt server add kitchenbox --ip 192.168.1.99  --kind proxmox
mpd-virt server add forge      --ip 192.168.1.100 --kind forgejo
mpd-virt server add runner     --ip 192.168.1.101 --kind generic
mpd-virt server list
```

State lands in `~/.mpd-virt/servers/<name>/` — an `env` file with the
address and kind, and later the certificate beside it.

**Name it after the machine, not the software.** The name becomes the DNS
name and the certificate's CN, so it should match the host's own
hostname — a box called `kitchenbox` that happens to run Proxmox is
`kitchenbox`, not `proxmox`. `--kind` is a separate field precisely so the two
need not agree: it only selects which
deployment hints `server deploy` prints and whether an IP SAN is added by
default. Making the DNS name disagree with the node's hostname means
keeping two names in step forever.

`--ssh user@host` is optional; it just makes the printed recipe name a
real target instead of a placeholder.

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
> `mpd-virt server cert <name> --force` then `server deploy` for each.

## Installing it on the server

```sh
mpd-virt server deploy <name>
```

prints the exact commands for that machine: where to put the root CA, where
that service expects its certificate and key, and the `/etc/hosts` block so it
can reach the other servers by name. `--kind` selects which form is printed.

mpd-virt stops there and does not run them. These are not machines it created,
it has no business restarting services on them unasked, and how you administer
your own servers is your business — the recipes exist to save you looking up
`pvenode` syntax, not to prescribe a setup.

Two details it is worth knowing before you paste anything:

- The root CA must land with a **`.crt`** extension in
  `/usr/local/share/ca-certificates/`. `update-ca-certificates` reads only
  `*.crt`, so a `.pem` is silently ignored and the CA never takes effect.
- Certificates for `--kind proxmox` carry an **IP SAN** by default, because
  such a box is typically also reached as `https://<ip>:8006/` and a name-only
  certificate would fail verification on the URL actually used. Override with
  `--no-ip`, or add one elsewhere with `--ip`.


## DNS: three resolvers, three mechanisms

| Where | `126.mpd.test` | `forge.mpd.test` |
|---|---|---|
| The Mac | VM 126's dnsmasq, via `/etc/resolver/126.mpd.test` | `/etc/hosts` |
| Inside a VM | its own dnsmasq | its own dnsmasq |
| Inside a container | the VM's dnsmasq | the VM's dnsmasq |

### The Mac — by hand, deliberately

`/etc/resolver/` holds one file per VM zone, so `forge.mpd.test` matches
none of them and the lookup goes to the system resolver, which asks the
internet about a reserved TLD and gets nothing. `/etc/hosts` is consulted
first, which is why hand-editing it works.

```sh
mpd-virt server list --etc-hosts | sudo tee -a /etc/hosts
sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder
```

`mpd-virt` will not write that file for you: it needs `sudo`, other tools
edit it too, and an ownership marker in it would need its own uninstall
path. `server list` reports which entries are missing.

### VMs and containers — automatic

```sh
mpd-virt server sync --all      # or: mpd-virt server sync 126
```

This writes `~/.mpd-virt/conf/lan-hosts`, scp's it to
`/var/lib/mpd/conf/lan-hosts` on each running VM, and runs
`mpd --vm-setup`, which republishes it through dnsmasq as
`<stateDir>/dns/lan.hosts`. `mpd-virt setup` does the same thing, so a
freshly provisioned VM knows the LAN names without a separate step. VMs
that are down are reported and skipped; the next `setup` picks them up.

Containers inherit this for free — they resolve through the VM's dnsmasq
at the bridge gateway and have no `/etc/hosts` of their own. That is the
point of the exercise:

```sh
ssh mpd-126 'podman run --rm --network mpd-internal alpine \
    getent hosts forge.mpd.test'
# 192.168.1.100  forge.mpd.test
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
mpd-virt server sync --all      # retract the name inside the VMs
```

`delete` deletes the certificate and key along with the registry entry —
keeping key material for a machine nothing tracks is how an
unaccounted-for private key ends up on disk. It cannot remove the copy
already installed on the server; do that there.

## Notes and limits

- **IP SANs are not covered by the root's name constraint.** The root
  constrains `dNSName` to `mpd.test`, but says nothing about
  `iPAddress`, and under RFC 5280 a name type absent from
  `permittedSubtrees` is *unconstrained*. So the root can sign for any
  address — which is true whether or not we issue IP SANs, so putting one
  on the Proxmox certificate does not widen the exposure. Narrowing it
  properly means adding `permitted;IP:<your LAN>/24` to the root, which
  costs a regeneration and a re-trust everywhere; the annual rotation is
  when to do it.
- **No revocation.** No CRL, no OCSP. Certificates expire; to retire one
  early, remove it from the server.
- **`~/.mpd-virt/conf/` is not backed up.** Losing it now costs more than
  rebuilding VMs: these certificates are installed on machines that
  cannot be rebuilt from a script. Whether that directory should be
  backed up is still open — see
  `docs/proposals/macos-host-state.md`.
