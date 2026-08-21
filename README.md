# mpd-virt

macOS host-side orchestrator for [mpd](https://github.com/mutms/mpd) — the
Moodle development VMs. `mpd-virt` creates or adopts
a Debian VM, bootstraps it over SSH, issues its certificates, and wires up
reachability so `https://*.mpd.test` works from your Mac.

Siblings: [mpd](https://github.com/mutms/mpd) (runs inside the VM),
[mpd-proxy](https://github.com/mutms/mpd-proxy) (optional root-only network
helper), [mudev](https://github.com/mutms/mudev) (Moodle recipes).

## Quickstart

You need Go 1.24+ to build and an SSH keypair. What you *manage* is a Debian
Trixie box, and it can live **anywhere mpd-virt can reach over SSH** — a VM on
your Mac (Apple `container`, UTM, Parallels), a Proxmox VM, a cloud instance,
or bare metal. An Apple-Silicon Mac is not a requirement; the Apple
`container` backend is just one of several.

```bash
make install                                        # → ~/.local/bin/mpd-virt
```

**Simplest path — adopt a plain Debian box (works anywhere).** Install stock
Debian Trixie (the minimal netinst is ideal — no desktop needed) with an SSH
server and your key, and set its hostname to `mpd-141`. Then, *on the box*,
run mpd's one-shot prep — it converts the network stack, sets up mDNS + the
guest agent, and prints the exact adopt command to run next:

```bash
bash <(wget -qO- https://raw.githubusercontent.com/mutms/mpd/main/setup/mpd-prepare-adopt.sh)
```

It may ask for one reboot; re-run it until every check is green, then on your
Mac run the command it printed:

```bash
mpd-virt adopt 141 --backend=generic                # IP found by mDNS
mpd-virt adopt 141 10.0.0.141 --backend=generic     # …or given explicitly, if mDNS can't reach it
```

**Or, on Apple Silicon, create one locally (experimental).** The Apple
`container` CLI lets `create` provision *and* adopt a fresh VM in a single
step — no separate Debian install or prep. It is the quickest way to a box,
but the `container` runtime is young, so treat this backend as experimental;
UTM, Parallels, or an adopted Debian box are the steadier choices:

```bash
container build -t mpd-virt-container-apple container/   # base image, once
mpd-virt create 141 --backend=container             # provision + adopt mpd-141
```

Either way, open a browser tunnel to the box:

```bash
ssh -N mpd-141-socks                                # SOCKS5 on 127.0.0.1:1080
```

Use a **dedicated** browser so your everyday one is untouched — Firefox is
ideal, since it has its own proxy setting *and* its own certificate store.
Install Firefox, then in its Settings → Network Settings set a manual SOCKS v5
proxy `127.0.0.1:1080` and tick "Proxy DNS when using SOCKS v5"; import the
root CA (`~/.mpd-virt/conf/caroot/rootCA.pem`) under Settings → Privacy &
Security → Certificates → View Certificates → Authorities → Import. Now open
`https://141.mpd.test` — no `sudo`, no system changes, your main browser and
system proxy left alone.

The SOCKS tunnel reaches **one VM at a time** (a single `:1080`). Once you run
VMs regularly — or several at once — [mpd-proxy](https://github.com/mutms/mpd-proxy)
is the usability upgrade: one `sudo mpd-proxy up` sets up a WireGuard tunnel
plus split DNS so **every** `*.mpd.test` name across **all** your VMs resolves
transparently, for every app, with no SOCKS proxy to configure anywhere. Pair
it with trusting the CA system-wide once (the one step that needs `sudo`) and
your everyday browser just works:

```bash
sudo security add-trusted-cert -d -r trustRoot \
    -k /Library/Keychains/System.keychain ~/.mpd-virt/conf/caroot/rootCA.pem
```

From there the in-VM `mpd` takes over — see
[mpd's USAGE](https://github.com/mutms/mpd/blob/main/docs/USAGE.md) for your
first Moodle site.

Verbs: `adopt create start stop update remove list server ca uninstall` —
`mpd-virt --help` for the surface, [AGENTS.md](AGENTS.md) for the details
(backends, ids, registry, ssh-config, state layout).

## Documentation

Deliberately short here; the depth lives where an AI assistant (or you) can
find it:

- [AGENTS.md](AGENTS.md) — the detailed reference: verbs, backends, VM
  identity, registry and state layout, code map
- [docs/SECURITY.md](docs/SECURITY.md) — trust model, why two CAs, CA backup
- [docs/LAN_SERVERS.md](docs/LAN_SERVERS.md) — certificates for non-VM LAN
  machines
- [container/README.md](container/README.md) — the base image

macOS-only for now; Linux host support may come later in this same codebase.

## AI disclosure

Majority of this project was written with the help of Claude (Anthropic). Everything it
produced was reviewed, corrected where needed and accepted by a human maintainer before
being committed; the design decisions and the final state of the code are the maintainers'.

## License

Copyright (C) 2026 Petr Skoda. [GPL-3.0](LICENSE) or later.

Moodle is a registered trademark of [Moodle Pty Ltd](https://moodle.com).
