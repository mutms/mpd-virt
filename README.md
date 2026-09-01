# mpd-virt

Host-side (macOS or Linux) orchestrator for [mpd](https://github.com/mutms/mpd) — the
Moodle development VMs. `mpd-virt` creates or adopts
a Debian VM, bootstraps it over SSH, issues its certificates, and wires up
reachability so `https://*.mpd.test` works from your machine.

Siblings: [mpd](https://github.com/mutms/mpd) (runs inside the VM),
[mpd-proxy](https://github.com/mutms/mpd-proxy) (optional root-only network
helper), [mudev](https://github.com/mutms/mudev) (Moodle recipes).

## Quickstart

You need Go (any version — go.mod picks the compiler) and an SSH keypair. What you *manage* is a Debian
Trixie VM, and it can live **anywhere mpd-virt can reach over SSH** — a VM on
your Mac (Apple `container`, UTM, Parallels), a KVM VM on a Linux box, a
Proxmox VM, a cloud instance, or bare metal. SSH (tcp/22) is all that is
required; WireGuard (udp/51820) reachable too is recommended, it is what
mpd-proxy's transparent `*.mpd.test` overlay uses.

First, [download and install Go](https://go.dev/doc/install).

Then clone the mpd-virt source and build it:

```bash
mkdir -p ~/Developer/mpd-virt                       # -p: create ~/Developer too if missing
cd ~/Developer/mpd-virt
git clone https://github.com/mutms/mpd-virt.git .
make install                                        # builds bin/mpd-virt → ~/.local/bin/mpd-virt
```

`make install` drops the binary in `~/.local/bin`; make sure that is on your
`PATH` (`export PATH="$HOME/.local/bin:$PATH"` in your shell profile) so
`mpd-virt` is found. Check with `mpd-virt --version`.

**Simplest path — adopt a plain Debian VM (any hypervisor, any host).** Install stock
Debian Trixie (the minimal netinst is ideal — no desktop needed) with an SSH
server and your key, and set its hostname to `mpd-141`. Then, *on the VM*,
run mpd's one-shot prep — it converts the network stack, sets up mDNS + the
guest agent, and prints the exact adopt command to run next:

```bash
bash <(wget -qO- https://raw.githubusercontent.com/mutms/mpd/main/setup/mpd-prepare-adopt.sh)
```

It may ask for one reboot; re-run it until every check is green, then on your
host run the command it printed:

```bash
mpd-virt adopt 141 --backend=generic                # IP found by mDNS
mpd-virt adopt 141 10.0.0.141 --backend=generic     # …or given explicitly, if mDNS can't reach it
```

**Or, on Apple Silicon, create one locally (experimental).** The Apple
`container` CLI lets `create` provision *and* adopt a fresh VM in a single
step — no separate Debian install or prep. It is the quickest way to a VM,
but the `container` VM is young, so treat this backend as experimental;
UTM, Parallels, or an adopted Debian VM are the steadier choices:

```bash
mpd-virt create 141 --backend=container   # pulls the published base image, creates + provisions mpd-141
```

Either way you now have a running mpd VM. `ssh mpd-141` lands where you
work — follow
[mpd's USAGE](https://github.com/mutms/mpd/blob/main/docs/usage.md) from
there.

All your projects are listed in the portal at `https://141.mpd.test/`. To
reach it, either open a SOCKS5 tunnel and point a dedicated Firefox at
`127.0.0.1:1080` (SOCKS v5, proxy DNS on, the root CA imported — it has
its own proxy setting and certificate store, so nothing else changes):

```bash
ssh -N mpd-141-socks
```

or run [mpd-proxy](https://github.com/mutms/mpd-proxy), which makes every
`*.mpd.test` name work in every app on your host:

```bash
sudo mpd-proxy up
mpd-virt start 141
```

Verbs: `adopt create start stop update remove list server ca uninstall` —
`mpd-virt --help` for the surface, [AGENTS.md](AGENTS.md) for the details
(backends, ids, registry, ssh-config, state layout).

## Documentation

Deliberately short here; the depth lives where an AI assistant (or you) can
find it:

- [AGENTS.md](AGENTS.md) — the detailed reference: verbs, backends, VM
  identity, registry and state layout, code map
- [docs/security.md](docs/security.md) — trust model, why two CAs, CA backup
- [docs/lan-servers.md](docs/lan-servers.md) — certificates for non-VM LAN
  machines
- [container/README.md](container/README.md) — the base image


## AI disclosure

Majority of this project was written with the help of Claude (Anthropic). Everything it
produced was reviewed, corrected where needed and accepted by a human maintainer before
being committed; the design decisions and the final state of the code are the maintainers'.

## License

Copyright (C) 2026 Petr Skoda. [GPL-3.0](LICENSE) or later.

Moodle is a registered trademark of [Moodle Pty Ltd](https://moodle.com).
