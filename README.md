# Pterodactyl on Mac

Run Pterodactyl game servers natively on macOS. No virtual machine, no Docker.

An **unofficial** fork of [Pterodactyl Wings](https://github.com/pterodactyl/wings),
tracking **v1.13.2**. Not affiliated with or endorsed by the Pterodactyl project.

## Quick start

```bash
curl -fsSL https://raw.githubusercontent.com/coquetteluke/pterodactyl-on-mac/main/install.sh | bash
```

It asks a few questions and sets everything up. The defaults are the right
answer for most people, so you can press enter through all of them.

You get, by default:

- **An account, a sandbox and firewall rules for every server**, so one server
  cannot read another's files, read your Panel token, or reach the rest of your
  network. This is on unless you turn it off.
- **Wings starting automatically** when the Mac boots.

The first question is whether this Mac should just run game servers for a Panel
elsewhere, or run the Panel too. Add `--full` or `--node` to answer it up front,
or `--yes` to skip the questions entirely.

**That one command is the only one you need.** Run it again later on a machine
that already has a node and it asks what to do instead: update, reinstall, turn
isolation on or off, or remove it.

## Status

This was written with heavy AI assistance. I am not a Go developer and I do not
really know GitHub. It is genuinely tested, and the tests are real ones, but
read it before you trust it with anything you care about.

Bug reports are very welcome. Active maintenance is not promised.

## ⚠️ Before you deploy it

**Do not rent servers on this to other people.** Isolation on this fork is real
but resource limits are not: macOS has no cgroups and no disk quotas, so one
server can still degrade or halt the machine everything else runs on. That is
not fixable, and it is why this is for a machine where you own every server.

Full detail, including exactly what is and is not enforced, is in
[what isolation does and does not cover](#what-isolation-does-and-does-not-cover).

## Contents

**Getting going**
- [Quick start](#quick-start)
- [Install](#install) - the long version
- [Updating](#updating) - and why your servers stay up
- [Uninstalling](#uninstalling)
- [Walkthrough: your first server](#walkthrough-your-first-server)

**Isolation**
- [What isolation does and does not cover](#what-isolation-does-and-does-not-cover)
- [Per-server accounts](#per-server-accounts)
- [Filesystem sandbox](#filesystem-sandbox)
- [Process isolation](#process-isolation)
- [Network isolation](#network-isolation)
- [Resource limits](#resource-limits) - memory and CPU

**When it breaks**
- [Troubleshooting](docs/troubleshooting.md) - the specific errors people hit,
  what causes them, and the macOS Local Network permission trap
- [Where to look](docs/troubleshooting.md#where-to-look-when-something-breaks) -
  which log says what

**Background**
- [Why this exists](#why-this-exists)
- [What works](#what-works)
- [What is different or missing](#what-is-different-or-missing)
- [Performance: native versus a VM](docs/performance.md) - measured, with the
  page-cache caveat that makes most such comparisons wrong
- [Notes for anyone porting Wings elsewhere](docs/porting.md)
- [Tests](#tests) / [Support](#support) / [License](#license)

## Install

There are two ways to run this, depending on where your Panel lives.

### This Mac as a node, Panel elsewhere

The common case: you already have a Panel somewhere else, on a Raspberry Pi or
another machine on your network, and you want this Mac to run game servers for
it.

```bash
curl -fsSL https://raw.githubusercontent.com/coquetteluke/pterodactyl-on-mac/main/install.sh | bash
```

That picks the right binary for your Mac (Apple silicon or Intel), verifies it
against the published `SHA256SUMS`, installs it to `~/.local/bin/wings`, creates
the data directories, and prints the configuration you still need to do.

You then create the node in your existing Panel and paste its config here. Set
the node's **Daemon Port to 8443**, not 443, because an unprivileged process
cannot bind a port below 1024.

### The questions it asks

Three, each with a default you can accept by pressing enter:

| question | default | flag to answer it up front |
| --- | --- | --- |
| Node only, or Panel too? | node only | `--node` / `--full` |
| Turn on isolation? | **yes** | `--isolate` / `--no-isolate` |
| Start wings at boot? | yes | `--launchagent` |

`--yes` accepts all three without asking. When there is no terminal at all, as
in a script or CI, the defaults are used, so isolation is on unless you pass
`--no-isolate`.

The binary is always checked against the published `SHA256SUMS`, and the install
is refused if that check cannot be made rather than falling back to a warning.
`--no-verify` overrides that if you have a reason to.

### Isolation

On by default. It gives every server its own account, its own view of the disk
and its own firewall rules, moves wings to a **LaunchDaemon running as root** so
it can create those accounts and load the rules, and adds the anchor lines to
`/etc/pf.conf` (backing the original up to `/etc/pf.conf.wings-backup`, and
restoring it automatically if the edit does not parse). The servers themselves
end up with fewer privileges than without it, since each drops to its own
account.

Then restart your servers from the Panel. Each one picks up its account, its
sandbox and its firewall rules on its next boot.

If your Panel lives on another machine, `config.yml` does not exist until you
have created the node and pasted its configuration in, and isolation has to be
written into that file. The installer notices and leaves you one command to run
once you have:

```bash
sudo ~/.local/bin/pterodactyl-isolate
```

That is the same script, so you can also use it to turn isolation on later, on a
node that is already running.

To undo it:

```bash
sudo ~/.local/bin/pterodactyl-isolate --revert
```

which hands the server files back, deletes the accounts, drops the firewall
rules, restores `/etc/pf.conf` and puts wings back to running as you. It refuses
while servers are still running, because changing who owns their files
underneath them leaves them unable to write their own worlds; stop them first,
or pass `--force`.

`uninstall.sh` reverses all of it too. Per-server accounts are kept unless you
pass `--purge`, since they own your server files.

### Everything on one Mac

Panel and node together, nothing else needed:

```bash
curl -fsSL https://raw.githubusercontent.com/coquetteluke/pterodactyl-on-mac/main/install.sh | bash -s -- --full
```

Or just run the plain command and pick the second option when it asks.

This installs PHP, MariaDB and nginx through Homebrew, downloads the Panel, sets
up its database, creates an admin account, and installs wings alongside it. The
Panel ends up on `http://<your-mac>.local:8088`, with the generated admin and
database passwords written to `CREDENTIALS.txt` in the Panel directory.

Three things it deliberately does differently from the Linux install guide, all
of which will otherwise waste your afternoon:

- **PHP is pinned to 8.3.** Panel 1.15 accepts `^8.2 || ^8.3`, and Homebrew's
  unversioned `php` is 8.4 or newer, which composer refuses outright.
- **No Redis.** Homebrew's current Redis build aborts on startup over a broken
  module path in its own shipped config. The Panel runs perfectly well on the
  file cache and database queue, so it is simply not installed.
- **Port 8088, not 8080.** Homebrew's stock `nginx.conf` already serves its
  welcome page on 8080 and that block is parsed first, so it would win as the
  default server for the port and the Panel would silently never be reached.

Creating the node itself is still a few clicks in the UI, since the Panel has no
command for it. The installer prints exactly what to fill in.

If piping a script into your shell makes you uneasy, which is fair, read it
first, or just grab the binary yourself from the
[releases page](https://github.com/coquetteluke/pterodactyl-on-mac/releases):

```bash
curl -fsSLO https://github.com/coquetteluke/pterodactyl-on-mac/releases/latest/download/wings_darwin_arm64
curl -fsSL  https://github.com/coquetteluke/pterodactyl-on-mac/releases/latest/download/SHA256SUMS | shasum -a 256 -c --ignore-missing
chmod +x wings_darwin_arm64 && mv wings_darwin_arm64 ~/.local/bin/wings
```

Use `wings_darwin_amd64` on an Intel Mac.

If you download the binary through a **browser** rather than with `curl`, macOS
attaches a quarantine attribute and Gatekeeper refuses to run it, because this
is unsigned and unnotarised. Clear it:

```bash
xattr -d com.apple.quarantine wings_darwin_arm64
```

`curl` does not set that attribute, so neither the install command nor the
`curl` lines above are affected.

### Updating

```bash
curl -fsSL https://raw.githubusercontent.com/coquetteluke/pterodactyl-on-mac/main/install.sh | bash -s -- --update
```

Or run the plain command on a machine that already has a node, and updating is
the first thing it offers.

It checks the installed version against the latest release and stops if there is
nothing to do. Otherwise it downloads the new binary, verifies it against the
published `SHA256SUMS`, replaces it, and restarts wings.

**Your game servers keep running the whole time.** They are detached into their
own sessions specifically so they survive wings restarting, and the new wings
adopts them by pid on the way back up, so players are not disconnected. Nothing
about your configuration or isolation is touched.

The replacement is a rename rather than a write, which is what makes swapping a
running binary safe: writing to one directly fails with `ETXTBSY`.

### Uninstalling

Run the same install command and pick **Remove it**, or go straight there:

```bash
curl -fsSL https://raw.githubusercontent.com/coquetteluke/pterodactyl-on-mac/main/install.sh | bash -s -- --uninstall
```

This stops any running servers, removes the wings binary and its LaunchAgent,
and **leaves all of your data alone**. Server files, worlds and the Panel
database all stay, and it prints where they are so you can decide.

Stopping the servers first matters: they run detached so they survive a wings
restart, so simply deleting wings would leave a game server running with
nothing supervising it.

```bash
# also remove the Panel, its services and its nginx site
curl -fsSL .../uninstall.sh | bash -s -- --full

# and delete the data too: server files, Panel directory, database
curl -fsSL .../uninstall.sh | bash -s -- --full --purge
```

`--purge` makes you type `DELETE` to confirm, and refuses outright if there is
no terminal to confirm on. Add `--yes` only if you are scripting it and certain.
Homebrew packages are left installed either way, since something else on the
machine may be using them; the uninstaller prints the command to remove them.

### Building from source

Requires Go 1.24+ and whatever runtime your servers need on the host.

```bash
go build -o wings .
```

Cross-compiling from another platform works too: `GOOS=darwin go build .`

### After installing

Wings still needs a `config.yml` from your Panel (Admin → Nodes → your node →
Configuration). Three settings matter for this fork:

```yaml
system:
  environment: native        # run servers as host processes
  root_directory: /Users/you/pterodactyl
  data: /Users/you/pterodactyl/volumes
api:
  port: 8443                 # an unprivileged process cannot bind 443
ignore_panel_config_updates: true
```

That last line stops the Panel pushing `api.port = daemonListen` back down and
breaking the daemon the next time you save the node. If you would rather keep
the Panel in charge, change the node's Daemon Port instead, or put a reverse
proxy in front of a high port.

`environment` accepts `docker`, `native`, or `auto`. `auto` is the default and
selects `native` on macOS and `docker` everywhere else, so an existing Linux
config keeps its current behaviour with no change.

Pick a data directory your user can write to. `/var/lib/pterodactyl`, the
upstream default, needs root.

## Walkthrough: your first server

The steps below are in the order that avoids the traps. Most of them exist
because something here behaves differently from a Linux install.

**1. Install wings** with one of the commands above.

**2. Create the node in your Panel.** Admin → Nodes → Create Node.

| field | value |
| --- | --- |
| FQDN | the Mac's IP or `.local` name, reachable *from the Panel* |
| Communicate over SSL | **off**, unless you have set up TLS yourself |
| Behind Proxy | off, unless there really is one |
| Daemon Port | **8443** |
| Daemon SFTP Port | 2022 |

Not 443. An unprivileged process cannot bind a port below 1024, and using 443
is what pushes people into running wings as root.

**3. Save the config, then fix its paths.** Copy the YAML from the node's
Configuration tab to `~/pterodactyl/config.yml`, then change the directories.
The Panel generates Linux defaults, which on macOS live under a root-owned
`/var`:

```yaml
system:
  root_directory: /Users/you/pterodactyl      # NOT /var/lib/pterodactyl
  data: /Users/you/pterodactyl/volumes
  log_directory: /Users/you/pterodactyl/logs
  environment: native
api:
  port: 8443
ignore_panel_config_updates: true
```

If you skip this, wings fails with a permission error, `sudo` makes it go away,
and you end up running every game server as root with root-owned files you
cannot read. It is the single easiest way to get into a mess here.

**4. Install what your servers actually run.** There is no container image to
supply a runtime:

```bash
brew install --cask temurin@25     # or whatever your game needs
brew install jq wget               # the installer does this for you
```

`/usr/bin/java` existing means nothing. That is Apple's stub, which prints
"Unable to locate a Java Runtime" and exits 1. Check with `java -version`.

**5. Start wings in the foreground** the first time, so you can read errors:

```bash
~/.local/bin/wings --config ~/pterodactyl/config.yml
```

The node should go green in the Panel. Then stop it and install the LaunchAgent
if you want it to survive reboots.

**6. Create the server** in the Panel as usual.

**7. Check the startup command.** Stock eggs use
`-XX:MaxRAMPercentage=95.0`, which sizes the heap off the whole machine when
there is no container. Replace it with an explicit `-Xmx` under the server's
memory limit.

## What isolation does and does not cover

Upstream Wings runs every server in its own Docker container, and a great deal
of Pterodactyl's security model rests on that. This fork has no containers.

**Out of the box there is no isolation at all.** Every server runs as the same
user with that user's full filesystem access, so one server can read and modify
another's files, and can read `config.yml`, which holds the node token that
authenticates to your Panel. Every server can also reach anything the machine
can: your Panel, other servers, the rest of your network.

Most of that can be rebuilt from what macOS does have, but **all of it is opt-in
and off by default**. One command turns on all three
([isolation](#isolation)):

| what it covers | how it works | |
| --- | --- | --- |
| Filesystem | an account per server, plus the kernel sandbox | [details](#per-server-accounts) |
| Network | pf rules matched on each server's uid | [details](#network-isolation) |
| Processes | separate accounts refuse cross-server signals; the sandbox can hide other processes entirely | [details](#process-isolation) |
| Process count | `RLIMIT_NPROC`, enforced by the kernel, so a fork bomb hits its own ceiling | [details](#process-isolation) |
| Memory and CPU | supervision, not kernel quotas | [details](#resource-limits) |

**What still has no equivalent here**, and cannot be given one. These are the
reasons this is not safe to rent out, and none of them is going to be fixed:

- **Memory and CPU limits react rather than prevent.** macOS has no cgroups and
  no public per-process CPU quota. Wings watches and intervenes: memory is
  sampled once a second, and CPU is capped by stopping the process in short
  bursts. Enough to stop one server ruining a machine; not a kernel quota. A
  server that allocates several gigabytes instantly still wins the race.
- **The filesystem sandbox denies by exception, not by default.** It withholds
  what you name. It does not confine a server to a private view of the disk the
  way a mount namespace does, so a server can still read world-readable files
  elsewhere on the machine.
- **No disk quotas.** APFS does not support them at all, so a server can fill
  the disk and take the machine down with it. Neither does upstream enforce a
  quota at write time, but on Linux you can at least put servers on a quota'd
  filesystem. Here you cannot.

So the shape of it: **the isolation boundaries are real, and the resource
boundaries are not.** A hostile server can be kept out of your files, off your
network and away from your other servers. It cannot reliably be stopped from
degrading or halting the machine everything runs on.

So: **do not rent servers on this to strangers.** If you are running a homelab
node where you own every server, the isolation above is worth turning on and is
a real improvement over nothing. If you are hosting for other people, use
upstream Wings on Linux.

---

## Per-server accounts

Containers give upstream Wings two things: isolation and resource limits. The
isolation half can be recovered without them, the pre-container way, by giving
each server its own operating system account so that ordinary unix permissions
keep them apart.

```yaml
system:
  user:
    per_server: true
```

With this on, Wings provisions a hidden account per server (`ptero-<uuid8>`, no
shell, no home, uid allocated from 700-999), chowns that server's data directory
to it, and drops to that account when spawning the process. A server can then no
longer read another server's files, or `config.yml` with your node token.

It requires **Wings to run as root**, since only root may change a process's
user, so it needs a LaunchDaemon rather than a LaunchAgent. Servers themselves
still run unprivileged, more so than before in fact.

Two things worth knowing:

- **This alone does not make the node safe for multi-tenant hosting.** Accounts
  keep servers out of each other's files, but a server can still fill the disk,
  see every other process on the machine, and reach your Panel unless you also
  turn on [network isolation](#network-isolation). Combine it with the
  [sandbox](#filesystem-sandbox) and network rules, and read the list of what is
  still missing at the top of this file before trusting it with anyone else's
  workload.
- Existing servers are chowned on their next boot, so their files stop being
  readable by the account you normally log in as. Use SFTP or the Panel's file
  manager rather than editing them directly over ssh.

## Filesystem sandbox

Per-server accounts rely on unix permissions, and those are only as good as the
permission bits actually on disk. One world-readable file, one plugin that
writes `0644`, one operator who ran `chmod -R` to fix something, and a server
can read what it should not.

macOS has a kernel sandbox underneath the permission system, the one that
confines App Store applications. It is reachable through `sandbox-exec`, it
applies to a process and every child it spawns, and it is enforced regardless of
file ownership.

```yaml
system:
  sandbox:
    enabled: true
    deny:
      - /Users/you/.ssh
```

With this on, a server is denied the contents of wings' root directory apart
from its own data directory. That covers `config.yml` with your node token and
every other server's files, and it holds even if the permissions do not.

Unlike the other two, **this does not require root**, because it only ever takes
access away from the process being started.

Being precise about the limits, because they decide whether it is worth turning
on:

- **It is a blacklist, not a whitelist.** A deny-by-default profile would need an
  allowance for every JDK, native library and temp directory a server might
  touch, and would break the first time a plugin reached for something
  unforeseen. So a confined server can still read ordinary world-readable files
  elsewhere on the machine. What it cannot read is what you name.
- **It is not a mount namespace.** The server still sees the host's real paths
  and shares its PID space, so it can tell that other processes exist. It just
  cannot read their files.
- Path metadata inside wings' directory stays readable, because a server that
  cannot `stat` its own parents cannot `chdir` into its own directory. Names and
  sizes leak; contents do not.

## Process isolation

A PID namespace does two things: it stops a server seeing other processes, and
it stops it touching them. macOS has no namespaces, but both halves can be had
another way.

**Signalling is already handled** by [per-server accounts](#per-server-accounts).
A server runs as its own uid, and unix refuses a signal to a process owned by
anyone else. Nothing extra to switch on.

**Visibility** is the sandbox's job:

```yaml
system:
  sandbox:
    hide_processes: true
```

With this on, a server cannot enumerate or inspect any process but its own. It
is off by default because it is absolute: `ps`, `top` and `pgrep` stop working
inside the server entirely, including on itself, since they have to enumerate
before they can filter. A JVM does not care. A startup script that shells out to
`ps` to check whether something is running will break.

What this is not: pids are not renumbered, so a server still sees the real pid
space and can tell that pids exist. It just cannot look at them.

### Process count

The number of processes a server may run **is** enforced by the kernel, via
`RLIMIT_NPROC`. This is the one resource limit on macOS that refuses rather than
reacts: a fork bomb inside a server fails its own `fork` once it hits the
ceiling, instead of exhausting the machine's process table.

It reuses the same setting the Docker environment passes to a container, so a
node behaves the same either way:

```yaml
docker:
  container_pid_limit: 512
```

This only applies when the server has an account of its own. `RLIMIT_NPROC` is
counted per uid across every process that uid owns, not per process tree, so
without per-server accounts the limit would be shared between every server plus
Wings itself, and the first server to start would set a budget for the whole
machine. With no dedicated uid it is skipped rather than applied wrongly.

## Network isolation

Per-server accounts stop a server reading another server's *files*. They do
nothing about the network. A container gets its own network namespace, so
reaching the host or a sibling needs routing the daemon never sets up. macOS has
no namespaces, so by default every server shares the host's network stack and
can open a connection to anything the Mac can reach: the Panel, Wings' own API
on loopback, your router's admin page, a NAS, another server's RCON port.

macOS does ship pf, and pf can match rules on the **uid that owns the socket**.
Combined with per-server accounts that gives a real per-server policy, enforced
in the kernel.

```yaml
system:
  network_isolation:
    enabled: true
    allow_out:
      - 192.168.4.40      # a database on the LAN that plugins need
```

pf only evaluates an anchor the main ruleset references, and macOS's stock
`/etc/pf.conf` references only Apple's own. Add these two lines to the end of
it, or Wings will load rules that sit there enforcing nothing:

```
anchor "wings"
load anchor "wings" from "/etc/pf.anchors/com.pterodactyl.wings"
```

Wings checks for this on startup and refuses rather than pretending the policy
is live.

What each server ends up with:

| Destination | Allowed |
| --- | --- |
| Its own allocated ports, inbound | yes |
| DNS, outbound | yes |
| The public internet, e.g. session servers, plugin downloads | yes |
| Anything in `allow_out` | yes |
| Loopback, so Wings' API and any local database | **no** |
| RFC1918 and CGNAT, so the Panel, your LAN, other machines | **no** |
| Link-local and IPv6 ULA | **no** |

The exceptions in `allow_out` are evaluated before the private-range blocks, so
listing an address there genuinely reinstates it.

Requirements and limits worth being clear about:

- It needs `per_server: true`. Rules key on uid, so without dedicated accounts
  every server looks identical to pf. Wings refuses the combination rather than
  loading a policy that cannot tell servers apart.
- It needs **root**, same as per-server accounts, since only root loads pf rules.
- `allow_out` takes addresses and CIDR blocks, not hostnames. pf resolves a name
  once when rules load, so a hostname silently stops matching when the address
  behind it moves, which for a security control is worse than refusing it.
- Wings takes a reference-counted claim on pf (`pfctl -E`), so it will not
  disable pf out from under Internet Sharing or a VPN that also uses it.
- **This is still not a network namespace.** Servers share the host's addresses
  and can see their own traffic. What it buys is that a compromised server
  cannot pivot to the Panel, the node, or the rest of your network.

## Resource limits

### Watch out for `MaxRAMPercentage` in egg startup commands

Several stock eggs, including Paper, start the JVM with something like
`-Xms128M -XX:MaxRAMPercentage=95.0`. Inside a container that is exactly right:
the JVM reads the cgroup limit and sizes its heap to 95% of what the server was
given.

There is no cgroup here, so the JVM reads the **whole machine** instead. On a
16 GB Mac a server with a 4 GB limit will happily grow its heap toward 15 GB,
sail past its limit, and be killed for it.

Replace the percentage with an explicit ceiling that matches the Panel's memory
limit, for example on a 4 GB server:

```
java -Xms1024M -Xmx3584M -jar {{SERVER_JARFILE}}
```

Leave headroom: `-Xmx` bounds the Java heap, not the whole process, and the JVM
also needs metaspace, thread stacks and off-heap buffers on top.

### Memory: enforced, with caveats

Wings already samples every server's resident memory once a second for the
Panel's graphs. When a server stays above its limit for five consecutive
samples it is killed and reported to the Panel as an out-of-memory kill, which
is the same thing the Panel shows when a cgroup kills a container.

The threshold is the Panel's memory limit plus the same overhead allowance
Docker applies, so both environments kill at the same number. The five-sample
grace exists so that a JVM briefly touching its ceiling before a collection is
not mistaken for a runaway.

Two honest differences from a kernel limit:

- **It is about a second late.** A cgroup refuses the allocation that would
  cross the limit; this notices afterwards. A fast allocation burst can
  overshoot in between, so this protects the machine from a server that leaks,
  not from one that allocates several gigabytes instantly.
- **It kills rather than refuses.** There is no way to make an allocation fail
  from outside the process.

Turn it off with:

```yaml
system:
  enforce_memory_limit: false
```

A server with no configured limit is never killed for memory. Its ceiling is the
machine, and it was allowed to use it.

#### If you use `-XX:+AlwaysPreTouch`, the default 5% overhead is too tight

The default allowance is the Panel's limit plus 5%, which assumes a process
whose resident memory tracks what it is really using. A JVM started with
`AlwaysPreTouch` commits its whole heap at startup instead, so an 8 GB heap
shows roughly 9 GB resident from the first second and stays there. Against a
10 GB Panel limit that is 84% of the kill threshold while the server is idle,
and normal load pushes it over.

The symptom is a server killed and reported as out of memory while its own
metrics look fine, because the heap is nowhere near full. The heap is not the
problem; metaspace, code cache, thread stacks, direct buffers and the collector's
own structures all sit outside it.

Give the non-heap overhead room rather than inflating the Panel number, which is
what you budget with:

```yaml
docker:
  overhead:
    override: true
    default_multiplier: 1.25
```

Rule of thumb: leave at least 1.5 GB between your heap size and the kill
threshold, and be aware that on macOS a pre-touched heap does **not** shrink
back the way it appears to on Linux. The memory compressor reclaims cold pages
only over hours, not minutes, so a freshly restarted server sits at its
committed size for a long time.

### CPU: enforced, off by default

```yaml
system:
  enforce_cpu_limit: true
```

macOS has no scheduler quota, and the alternatives do not work: an unprivileged
process can lower its own priority but not restore it, and the background QoS
tier throttles I/O and timers while leaving compute alone -- a burner still sits
at 99.7% of a core after `taskpolicy -b`. Stopping and continuing the process is
what remains, and it caps accurately, within about 3% of target.

The catch is that a throttled process is frozen in bursts, and for a game server
those land inside its tick budget. So the limiter only ever throttles a server
that is **over** its limit:

- A server inside its allowance is **never signalled at all** -- not stopped,
  not continued. It cannot tell the limiter exists.
- Idle capacity stays usable. A cgroup quota would idle the CPU rather than lend
  it out; here a server may use whatever is going spare until something else
  wants it.
- Decisions are made on usage smoothed over ~250ms, so a server is not throttled
  for briefly spawning a helper process.
- A single stall is bounded by the 20ms control window. Measured: a 40%-limited
  runaway settles around 68%, and a 50%-limited one at 47% with 10ms stalls.

It is off by default because on a machine running one server there is nothing to
protect it from, and capping it can only make it slower. Turn it on when several
servers share a machine and one of them misbehaving would ruin the others.

## Why this exists

macOS cannot run Linux containers. Every "Docker for Mac" runtime (Docker
Desktop, Colima, OrbStack, Rancher) boots a Linux VM to do it, and Apple's own
`container` framework runs a micro-VM per container and requires Apple silicon.
So "run Pterodactyl on a Mac" has always meant "run a Linux VM on a Mac."

This fork replaces Wings' container layer with host processes, so a Mac can be a
Pterodactyl node with no VM involved.

## What works

- Full Panel integration: console, file manager, SFTP, power actions, and live
  CPU / memory / disk graphs
- Servers **survive a Wings restart**. stdin is a FIFO, stdout is a log file,
  and the process gets its own session, so a later Wings adopts it by pid
- Backups, transfers and the egg install flow
- Linux is unaffected: the Docker environment is untouched and still the default
  there

## What is different or missing

| | upstream | this fork |
| --- | --- | --- |
| filesystem isolation | mount namespace per server | unix accounts + kernel sandbox, opt-in |
| network isolation | network namespace per server | pf rules keyed on uid, opt-in |
| process isolation | PID namespace per server | separate accounts, plus optional process hiding |
| memory limit | enforced by the kernel | enforced by supervision, ~1s latency |
| CPU limit | enforced via cgroups | enforced by supervision, opt-in |
| egg install | runs in the installer image | runs on the host (see below) |
| server user | dedicated `pterodactyl` user | the user running Wings, or an account per server when isolation is on |

Egg install scripts hardcode `/mnt/server` and `/mnt/install`. There is nothing
to mount those onto, and macOS's sealed root filesystem means `/mnt` cannot even
be created, so the script text is rewritten to point at the real directories.
This works for essentially every real egg, but a script that assembles a path at
runtime (`cd /mnt/${dir}`) will not be caught and will fail.

They also assume the installer image's toolbox. `curl`, `tar` and `unzip` are on
macOS already; `jq` and `wget` are not, and plenty of eggs need them, so the
installer adds both. Anything the *startup* command invokes, such as `java`, `node`
or `python`, you must install yourself, since no image supplies it.

## Tests

```bash
go test ./...
```

The full upstream suite passes on macOS, including the sandbox escape tests on
the `openat` fallback path. The Linux build and test suite are unaffected, and
please keep it that way in any PR.

## Support

Provided as-is, with no support and no guarantee of tracking upstream releases.
Bugs in this fork are **not** the Pterodactyl project's problem, so please do not
open issues on their tracker for anything here.

## License

MIT, same as upstream. See [LICENSE](LICENSE); the original copyright notice is
retained. Pterodactyl is a trademark of its owners; this project is not
affiliated with them.
