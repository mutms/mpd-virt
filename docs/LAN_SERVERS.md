# LAN servers under `mpd.test`

How the machines on the home LAN — Proxmox, Forgejo, the runner — get
names in the `mpd.test` tree, certificates that everything here already
trusts, and DNS that answers the same way from the Mac, from every mpd
VM, and from every container inside those VMs.

This is a **reference for setting those machines up by hand.** `mpd-virt`
issues the material and tells you what to install; it does not log into
these machines. They are not VMs it created, and restarting services on
them unasked is not its job.

Current inventory:

| Name               | Address         | Kind      |
|--------------------|-----------------|-----------|
| `proxmox.mpd.test` | `192.168.1.99`  | `proxmox` |
| `forge.mpd.test`   | `192.168.1.100` | `forgejo` |
| `runner.mpd.test`  | `192.168.1.101` | `generic` |

## Why bother

Everything in this setup already trusts the mpd root CA: the Mac's System
Keychain, every VM's three trust stores, every runtime container. Giving
LAN machines names under `mpd.test` means they can join that trust
relationship instead of standing outside it — driving the Proxmox REST
API from a script, or a Forgejo runner cloning over HTTPS, with no
`--insecure`, no `GIT_SSL_NO_VERIFY`, and no custom CA bundle baked into
container images.

The precondition was getting the root key off the VMs. While every VM
held it, any VM could mint a certificate for `forge.mpd.test` and
impersonate Forgejo to the runners. Each VM now signs with an
intermediate constrained to its own zone, so it can forge names in
`126.mpd.test` and nowhere else. See `README.md` for that chain.

## The naming rule

`mpd.test` holds two kinds of name, told apart by position:

```
mpd.test
├── 126.mpd.test          ← a VM zone: 3-digit first label
│   └── m45.126.mpd.test  ← signed inside VM 126, by its own constrained CA
├── forge.mpd.test        ← a LAN service: any non-numeric label
├── proxmox.mpd.test
└── runner.mpd.test
```

`mpd-virt server add` enforces it: a 3-digit name is refused because it
would shadow a VM zone that VM's own CA owns, and a name outside
`mpd.test` is refused because the root CA is name-constrained and could
not sign it.

## Registering a server

```sh
mpd-virt server add proxmox --ip 192.168.1.99  --kind proxmox
mpd-virt server add forge   --ip 192.168.1.100 --kind forgejo
mpd-virt server add runner  --ip 192.168.1.101 --kind generic
mpd-virt server list
```

State lands in `~/.mpd-virt/servers/<name>/` — an `env` file with the
address and kind, and later the certificate beside it. `--kind` only
selects which deployment hints `server deploy` prints.

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

`mpd-virt server deploy <name>` prints the exact commands for that
machine. The shape is always the same three steps.

### 1. Trust the root CA

Do this first and on every LAN machine, including ones that only *talk*
to the others. Debian family:

```sh
mpd-virt ca export > /tmp/mpd-root.crt
scp /tmp/mpd-root.crt user@192.168.1.99:/tmp/
ssh user@192.168.1.99 'sudo install -m 644 /tmp/mpd-root.crt \
    /usr/local/share/ca-certificates/mpd-root.crt \
    && sudo update-ca-certificates'
```

Only the *public* certificate leaves the Mac. The root private key never
does — not to a VM, not to a LAN server, not anywhere.

### 2. Install the leaf

**Proxmox.** `pvenode` takes the pair directly and restarts `pveproxy`
itself; the web UI drops for a second.

```sh
scp ~/.mpd-virt/servers/proxmox/cert.pem user@192.168.1.99:/tmp/cert.pem
scp ~/.mpd-virt/servers/proxmox/key.pem  user@192.168.1.99:/tmp/key.pem
ssh user@192.168.1.99 'sudo pvenode cert set /tmp/cert.pem /tmp/key.pem --force'
```

**Forgejo.** Needs `app.ini` to be serving HTTPS itself:

```ini
[server]
PROTOCOL  = https
CERT_FILE = custom/https/cert.pem
KEY_FILE  = custom/https/key.pem
ROOT_URL  = https://forge.mpd.test/
```

```sh
ssh user@192.168.1.100 'sudo install -o git -g git -m 644 /tmp/cert.pem \
      /var/lib/forgejo/custom/https/cert.pem \
    && sudo install -o git -g git -m 600 /tmp/key.pem \
      /var/lib/forgejo/custom/https/key.pem \
    && sudo systemctl restart forgejo'
```

If Forgejo sits behind a reverse proxy instead, the certificate belongs
on the proxy and `--kind caddy` prints that form.

**Anything else** (`--kind generic`): install the pair wherever the
service expects it.

### 3. Give the server its own hosts entries

Each LAN machine needs to resolve the *others* — this is what lets a
runner reach `forge.mpd.test`. `server deploy <name>` prints the block
for that machine, excluding itself:

```
192.168.1.100	forge.mpd.test
192.168.1.101	runner.mpd.test
```

Append to `/etc/hosts` on the server.

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

The VM reaches `192.168.1.x` directly — Parallels Shared and UTM Shared
both route to the host LAN — so nothing else is needed for the path.

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
mpd-virt server rm forge
mpd-virt server sync --all      # retract the name inside the VMs
```

`rm` deletes the certificate and key along with the registry entry —
keeping key material for a machine nothing tracks is how an
unaccounted-for private key ends up on disk. It cannot remove the copy
already installed on the server; do that there.

## Notes and limits

- **DNS SANs only.** The root constrains `dNSName`, but under RFC 5280 a
  name type absent from `permittedSubtrees` is *unconstrained* — so an IP
  SAN would be the one field of these certificates the name constraint
  does not cover. Reach hosts by name, not by address.
- **No revocation.** No CRL, no OCSP. Certificates expire; to retire one
  early, remove it from the server.
- **`~/.mpd-virt/conf/` is not backed up.** Losing it now costs more than
  rebuilding VMs: these certificates are installed on machines that
  cannot be rebuilt from a script. Whether that directory should be
  backed up is still open — see
  `docs/proposals/macos-host-state.md`.
