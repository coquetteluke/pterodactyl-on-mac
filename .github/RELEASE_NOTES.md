Unofficial macOS build of Pterodactyl Wings, which runs game servers as host
processes because macOS cannot run Linux containers.

## Install

```
curl -fsSL https://raw.githubusercontent.com/__REPO__/main/install.sh | bash
```

It asks what you want and sets it up. The defaults suit most people, so you can
press enter through all of it.

The first question is whether this Mac should only run game servers for a Panel
somewhere else, a Raspberry Pi for instance, or run the Panel too. Pass `--node`
or `--full` to answer that up front, or `--yes` to skip the questions entirely.

If your Panel is on another machine, set the node's **Daemon Port to 8443**, not
443: an unprivileged process cannot bind a port below 1024.

## Isolation is on by default

Every server gets its own account, its own view of the disk and its own firewall
rules, so one server cannot read another's files, read the token that
authenticates this node to your Panel, or reach the rest of your network. Wings
runs as root to set that up; the servers themselves end up with fewer privileges
than without it. Pass `--no-isolate` to skip it.

## Managing an existing node

The same command handles the rest. Run it again and it asks, or go straight
there:

```
curl -fsSL https://raw.githubusercontent.com/__REPO__/main/install.sh | bash -s -- --update
```

`--update` replaces the binary and restarts wings without interrupting running
game servers. Also `--turn-isolation-on`, `--turn-isolation-off`, `--uninstall`
and `--purge`.

## Or install by hand

Download the binary below, `arm64` for Apple silicon or `amd64` for Intel, and
check it against `SHA256SUMS`:

```
shasum -a 256 -c SHA256SUMS --ignore-missing
```

A binary downloaded through a browser is quarantined by Gatekeeper and will not
run, since this is unsigned and unnotarised. Clear it with:

```
xattr -d com.apple.quarantine wings_darwin_arm64
```

`curl` does not set the quarantine bit, so the install command above is
unaffected.

## Before you deploy it

**Do not rent servers on this to other people.** The isolation boundaries are
real, but the resource boundaries are not: macOS has no cgroups and no disk
quotas, so one server can still degrade or halt the machine everything else runs
on. Memory and CPU limits are enforced by supervision rather than the kernel,
and react rather than prevent.

That is not fixable on macOS, and it is why this is for a machine where you own
every server. If you are hosting for other people, use upstream Wings on Linux.

Full detail is in the [README](https://github.com/__REPO__#what-isolation-does-and-does-not-cover).
