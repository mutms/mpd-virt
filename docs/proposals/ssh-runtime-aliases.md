# SSH runtime aliases (ProxyJump) + in-VM hostname alignment

*Salvaged from the pruned `mpd-virt.md` proposal — the surrounding design is implemented/superseded; this is the part that is not yet built.*

> Status check: `internal/sshconfig/sshconfig.go` writes only the box's own
> `Host mpd-<NNN>` block today and explicitly notes the runtime aliases below
> are "not implemented yet — they need internal/net."

## SSH config runtime-alias block

`mpd-virt setup`/`takeover` writes a managed block to `~/.ssh/config` that
gives the user predictable Host aliases for the VM **and each of its runtime
containers**, all reachable from the Mac with no IP memorization.

The runtime set is **fixed and known** — today's mpd ships three runtimes
(`php`, `node`, `util`). Their hostnames inside the VM are stable
(`<runtime>.runtime.mpd.test`, resolved by the in-VM dnsmasq). So the SSH
block is a **static template** that mpd-virt writes once per VM at setup
time, never re-synced. SSH to a runtime that hasn't been started yet returns
"Connection refused" — fine; the user starts the runtime inside the VM and
retries.

Block shape (one per VM, written between the standard managed-block markers —
the box's own `Host mpd-<NNN>` entry is already written by mpd-virt; the
runtime entries below are the unbuilt addition):

```
# >>> mpd (managed by mpd-virt) >>>
Host mpd-<NNN>
    HostName <box-reachable-address>
    User <dev-user>
    StrictHostKeyChecking no

Host mpd-<NNN>-php
    HostName php.runtime.mpd.test
    User user
    ProxyJump mpd-<NNN>
    StrictHostKeyChecking no

Host mpd-<NNN>-node
    HostName node.runtime.mpd.test
    User user
    ProxyJump mpd-<NNN>
    StrictHostKeyChecking no

Host mpd-<NNN>-util
    HostName util.runtime.mpd.test
    User user
    ProxyJump mpd-<NNN>
    StrictHostKeyChecking no
# <<< mpd <<<
```

User-visible UX:

- `ssh mpd-222` — direct SSH to the VM.
- `ssh mpd-222-php` — SSH into the php runtime, automatically ProxyJumping
  through the VM. `<runtime>.runtime.mpd.test` resolves via the VM's dnsmasq
  during the inner hop.
- `ssh mpd-222-node`, `ssh mpd-222-util` — same pattern.
- PHPStorm Gateway / VSCode Remote-SSH point at these Host aliases directly;
  the ProxyJump is transparent.
- `scp mpd-222-php:/srv/projects/foo/bar.txt .` works.

**Block lifecycle:**

- Written by `mpd-virt setup`/`takeover` (full block, all entries).
- Re-asserted idempotently (same content unless the box address / dev user
  changed).
- Removed on uninstall (strips just the marked block).
- **Never automatically re-synced** when runtimes are created/destroyed
  inside the VM. The block is static; absent runtimes manifest as
  "Connection refused" on `ssh`, which is a clear enough signal.

**If the fixed runtime list ever grows** (e.g. mpd adds a `python` runtime):
the user re-runs setup after upgrading mpd; the block gets the new entry.
Annual-rare event; no auto-detection needed.

## In-VM hostname alignment (dependency on the in-VM `mpd` binary)

For the SSH aliases to feel natural end-to-end, the **runtime container
hostnames inside the VM should match the SSH alias names**. Today the in-VM
mpd names runtime containers like `mpd-runtime-<runtime>-<suffix>` (e.g.
`mpd-runtime-php-222`). So the user typing `ssh mpd-222-php` would land inside
a container whose internal hostname is `mpd-runtime-php-222` — same VM,
different word order, mildly confusing in a terminal with several tabs open.

**Required alignment**: change the in-VM runtime-naming convention from
`mpd-runtime-<runtime>-<suffix>` to `mpd-<NNN>-<runtime>`, matching the SSH
alias exactly. Concretely:

- Same prefix as the VM (`mpd-`) rather than the runtime-specific
  `mpd-runtime-` — emphasizes that the runtime is *in* a specific mpd VM, not
  a free-standing thing.
- Octet before runtime — matches the SSH-config word order.
- DNS names inside the VM (`php.runtime.mpd.test` and friends) stay unchanged.
  They're the external addressing identity; the container hostname is only the
  shell-prompt identity.

**Where the change lives**: in the in-VM `mpd` binary, wherever podman is
invoked to create runtime containers with `--hostname <…>`. `<NNN>` comes from
the VM's own hostname (the VM is named `mpd-<NNN>`, so reading `/etc/hostname`
and splitting on the last `-` gives the octet).

**Sequencing**: this in-VM change is independent of `mpd-virt` itself, but
they're a matched pair — landing one without the other leaves the asymmetry in
place. Land the in-VM hostname-template change together with (or before) the
runtime-alias block, so day-one users get the consistent in-container shell
prompt.
