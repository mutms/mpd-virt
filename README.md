# mpd-virt

macOS host-side orchestrator for [mpd](https://github.com/mutms/mpd) — the
Moodle development VMs I use for my daily work. `mpd-virt` creates or adopts
a Debian VM, bootstraps it over SSH, issues its certificates, and wires up
reachability so `https://*.mpd.test` works from your Mac. Then it stays out
of the way.

Siblings: [mpd](https://github.com/mutms/mpd) (runs inside the VM),
[mpd-proxy](https://github.com/mutms/mpd-proxy) (optional root-only network
helper), [mudev](https://github.com/mutms/mudev) (Moodle recipes).

## Quickstart

Needs Go 1.24+, an Apple-Silicon Mac with the Apple `container` CLI, and an
SSH keypair. (Other backends — UTM, Parallels, any reachable Debian box, a
Proxmox VM — are covered in [AGENTS.md](AGENTS.md).)

```bash
make install                                        # → ~/.local/bin/mpd-virt
container build -t mpd-virt-container-apple containers/apple/   # base image, once
mpd-virt create 141 --backend=container             # provision + adopt mpd-141
sudo security add-trusted-cert -d -r trustRoot \
    -k /Library/Keychains/System.keychain ~/.mpd-virt/conf/caroot/rootCA.pem
ssh -N mpd-141-socks                                # SOCKS5 on 127.0.0.1:1080
```

Point a browser at SOCKS5 `127.0.0.1:1080` with remote DNS (or run
`sudo mpd-proxy up` for transparent access from every app) and open
`https://141.mpd.test`. From there the in-VM `mpd` takes over — see
[mpd's USAGE](https://github.com/mutms/mpd/blob/main/docs/USAGE.md) for your
first Moodle site.

Verbs: `takeover create start stop update delete list server ca uninstall` —
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
- [containers/apple/README.md](containers/apple/README.md) — the base image

macOS-only for now; Linux and Windows/WSL host support may come later in this
same codebase.

## About

I built these tools for my own daily Moodle work and release them because I
like open source — try them, break them, send issues or PRs.

mpd and its related tools are my first fully AI-driven project — the code and
docs are largely written by [Claude Code](https://claude.com/claude-code)
(Anthropic) under my direction (design and review stay human).

## License

Copyright (C) 2026 Petr Skoda. [GPL-3.0](LICENSE) or later.

Moodle is a registered trademark of [Moodle Pty Ltd](https://moodle.com).
