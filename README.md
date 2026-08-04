fyi this is 100% claude ai. i dont know how to code nor work github. idk if ill be actively maintaining this. submit any errors u get please. ty

# Pterodactyl on Mac

An **unofficial** fork of [Pterodactyl Wings](https://github.com/pterodactyl/wings)
that runs game servers natively on macOS, as ordinary host processes instead of
Docker containers.

Not affiliated with or endorsed by the Pterodactyl project. Tracking Wings
**v1.13.2**.

---

## ⚠️ Read this before deploying it

**This fork removes the container boundary. Do not use it for shared or
multi-tenant hosting.**

Upstream Wings runs every server in its own Docker container, and a great deal
of Pterodactyl's security model rests on that. This fork has no containers, so:

- **Servers are not isolated from each other or from the host** by default.
  Every server process runs as the same user, with that user's full filesystem
  access. One server can read and modify another server's files, and can read
  `config.yml` which contains the node token that authenticates to your Panel.
  This one is fixable; see [per-server accounts](#per-server-accounts). The two
  below are not.
- **Resource limits are enforced by supervision, not by the kernel.** macOS has
  no cgroups. Wings can hold a server to both its memory and CPU limits (see
  [resource limits](#resource-limits)), but it does so by watching and reacting,
  which is slower and coarser than a kernel quota: memory is checked once a
  second, and CPU is capped by stopping the process in short bursts. Good enough
  to stop one server ruining a machine; not a substitute for real isolation.
- **There is no network isolation.** Servers bind host ports directly.

If you are renting servers to other people, use upstream Wings on Linux. This
fork is intended for a **single-tenant** machine: a homelab node where you own
every server on it.

---

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
| isolation | container per server | none, just plain processes |
| memory limit | enforced by the kernel | enforced by supervision, ~1s latency |
| CPU limit | enforced via cgroups | enforced by supervision, opt-in |
| egg install | runs in the installer image | runs on the host (see below) |
| server user | dedicated `pterodactyl` user | the user running Wings |

Egg install scripts hardcode `/mnt/server` and `/mnt/install`. There is nothing
to mount those onto, and macOS's sealed root filesystem means `/mnt` cannot even
be created, so the script text is rewritten to point at the real directories.
This works for essentially every real egg, but a script that assembles a path at
runtime (`cd /mnt/${dir}`) will not be caught and will fail.

They also assume the installer image's toolbox. `curl`, `tar` and `unzip` are on
macOS already; `jq` and `wget` are not, and plenty of eggs need them, so the
installer adds both. Anything the *startup* command invokes, such as `java`, `node`
or `python`, you must install yourself, since no image supplies it.

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
the data directories, and prints the configuration you still need to do. Add
`-s -- --launchagent` to also install a LaunchAgent that keeps it running.

You then create the node in your existing Panel and paste its config here. Set
the node's **Daemon Port to 8443**, not 443, because an unprivileged process
cannot bind a port below 1024.

### Everything on one Mac

Panel and node together, nothing else needed:

```bash
curl -fsSL https://raw.githubusercontent.com/coquetteluke/pterodactyl-on-mac/main/install.sh | bash -s -- --full
```

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

### Uninstalling

```bash
curl -fsSL https://raw.githubusercontent.com/coquetteluke/pterodactyl-on-mac/main/uninstall.sh | bash
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

## Where to look when something breaks

Everything is a plain file. Assuming the paths above:

| what | where |
| --- | --- |
| wings' own log | `~/pterodactyl/logs/wings.stderr.log` (or the terminal) |
| a server's console | `~/pterodactyl/native/<server-uuid>/console.log` |
| a server's install log | `~/pterodactyl/logs/install/<server-uuid>.log` |
| the server's files | `~/pterodactyl/volumes/<server-uuid>/` |
| what wings is running as | `ps aux \| grep "[w]ings"` |

If those paths do not exist, wings is using different ones, so check
`root_directory` in your `config.yml`. If they exist but you get "no such file"
or empty globs, wings is running as root and your shell cannot see into them;
that is a sign to go back to step 3.

## Is it actually faster than a VM?

Mostly no. The win is memory, not speed.

These are measured on one machine, a 2019 Intel MacBook Pro with 16 GB running a
Paper 26.2 server and 43 plugins, comparing the same server before and after
it was moved off a Lima VM (8 vCPU, 12 GiB, `vz`) onto the host.

| | VM | native |
| --- | --- | --- |
| server startup | 39.7s (n=16) | ~38.6s |
| cold read, 130 MB / 244 files | 257 ms | 220 ms |
| memory reserved | 12 GiB | what the server uses (5.5 GB) |
| LAN round trip | ~0.4 ms | ~0.4 ms |

**Startup time does not improve.** Apple's Virtualization.framework is
hardware-accelerated, so CPU-bound JVM work already ran at close to native
speed. Sixteen boots from the VM era averaged 39.7s; the quietest native boot
was 38.6s. That is a wash, and it is the honest answer to "will my server run
faster": it will not, noticeably.

**Disk I/O improves by around 14%**, which is the virtio-blk and disk-image
layers going away. Worth having for a Minecraft server, which does a lot of
small random reads, but not transformative.

Beware of measuring this badly: dropping caches *inside* the guest does not
evict the disk image from the *host's* page cache, so the VM's "cold" read is
served from host RAM and looks about twice as fast as native. Both layers have
to be purged for the comparison to mean anything.

**Memory is the real difference.** A VM reserves what you give it whether the
workload needs it or not. 12 GiB of a 16 GiB machine was committed to running a
process that fits in 5.5 GB. Getting that back is the reason to do this, and on
a machine with plenty of RAM to spare the case is much weaker.

**Networking is unchanged** if the VM was already bridged. Lima's default
userspace port forwarding costs real latency, roughly 31 ms as observed here
before bridging, but `socket_vmnet` removes that without leaving the VM.

The operational difference does not show up in a benchmark: the VM is another
thing that has to be running. Backups on this node silently stopped for two
days because the VM was powered off, not because anything failed.

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

- **This does not make the node safe for multi-tenant hosting.** There are still
  no resource limits, so one server can exhaust the machine's memory and take
  every other server down with it. Filesystem isolation without resource
  isolation is not enough to rent servers to strangers.
- Existing servers are chowned on their next boot, so their files stop being
  readable by the account you normally log in as. Use SFTP or the Panel's file
  manager rather than editing them directly over ssh.

## Troubleshooting

### "An error occurred on the remote host ... (code: 404)" when creating a server

The Panel reached *an* HTTP server and got a 404, so something answered but it
was not wings. Nearly always the node's **Daemon Port** is pointing at the
wrong thing, commonly at the Panel's own nginx if you installed both on one
machine.

Check what is actually listening, from the machine running the Panel:

```bash
nc -z -w3 <node-ip> 8443 && echo "wings up" || echo "wings NOT listening"
curl -s -o /dev/null -w '%{http_code}\n' http://<node-ip>:8443/api/system
```

An unauthenticated request to a healthy wings returns **401**, not 404 or 200.
A 404 means you are talking to a web server; a connection failure means wings
is not running at all.

Then, in Admin → Nodes → your node → Settings:

- **Daemon Port: 8443.** Not 443, which an unprivileged process cannot bind, and
  not the Panel's port.
- **SSL: off**, unless you have put a TLS terminator in front of wings
  yourself. wings ships no certificate, so with SSL on the Panel tries HTTPS
  against a plain HTTP daemon.
- **Behind Proxy: off**, unless there really is a proxy.

And on the node, `config.yml` must contain:

```yaml
api:
  port: 8443
system:
  environment: native
  root_directory: /Users/you/pterodactyl
  data: /Users/you/pterodactyl/volumes
ignore_panel_config_updates: true
```

Start it in the foreground the first time so you can read the errors:

```bash
~/.local/bin/wings --config ~/pterodactyl/config.yml
```

### The node shows offline even though wings is running

If `api.port` in `config.yml` disagrees with the node's Daemon Port, the Panel
is knocking on the wrong door. The Panel pushes `api.port = daemonListen` every
time a node is saved, which is why `ignore_panel_config_updates: true` is
recommended. Without it, saving the node in the UI silently rewrites your
config back to a port the daemon cannot bind, and it stops coming back after a
restart.

### A new server exits with code 1 immediately and prints nothing

The install script did not produce a jar, so there is nothing to run. Check the
install log at `<data-dir>/logs/install/<server-uuid>.log`.

The usual cause is a missing command-line tool. Egg install scripts are written
for the Alpine and Debian installer images, where `jq` and `wget` are a given;
macOS ships neither. The Paper script in particular pipes `curl` through `jq` to
build its download URL, so without `jq` the URL is empty, `curl` writes nothing,
the install appears to succeed, and the server exits 1 on boot with no output.

```bash
brew install jq wget
```

Then hit **Reinstall Server** in the Panel. The installer does this for you on a
fresh install; you only need it by hand if you set wings up before this was
added, or installed the binary manually.

### Servers get killed shortly after starting

Check the startup command for `-XX:MaxRAMPercentage`; see
[the note above](#watch-out-for-maxrampercentage-in-egg-startup-commands). The
JVM is sizing its heap off the whole machine rather than the server's limit.

### wings only starts with sudo, and my files are all root-owned

Your `config.yml` still has the Panel's Linux defaults, `/var/lib/pterodactyl`,
which needs root on macOS. Point it at your home directory instead of
escalating; see step 3 of the walkthrough. To undo it:

```bash
sudo pkill -f 'bin/wings'
sed -i '' "s|/var/lib/pterodactyl|$HOME/pterodactyl|g; s|/var/log/pterodactyl|$HOME/pterodactyl/logs|g" ~/pterodactyl/config.yml
sudo cp -a /var/lib/pterodactyl/volumes/. ~/pterodactyl/volumes/ 2>/dev/null
sudo chown -R "$USER":staff ~/pterodactyl
~/.local/bin/wings --config ~/pterodactyl/config.yml
```

Running as root also means your game servers run as root, which is a worse
position than upstream Pterodactyl puts you in, where the container contains
a compromised plugin.

### The "accept the EULA" dialog never appears

That dialog is driven entirely by the Panel matching the line "you need to
agree to the eula in order to run the server" in the live console stream. If
the server's output is not reaching the Panel, the dialog cannot fire, and
neither can the Java-version or PID-limit prompts.

Fixed in v1.13.2-mac.7: the console tail was being torn down the instant the
process exited, discarding anything written in the previous 50ms, which for a
server that fails immediately is all of it. Update, or accept it manually by
setting `eula=true` in `eula.txt` through the Panel's file manager.

### Servers cannot reach a database on another machine

That is macOS Local Network permission, not networking. See below.

## macOS gotcha: Local Network permission

If your servers cannot reach anything on your LAN, such as a database on another
machine, and you get `EHOSTUNREACH` / `NoRouteToHostException` while the
same connection works fine from your shell, this is **not** a networking problem.

macOS 15+ requires Local Network permission, and a process started by `launchd`
has no UI, so it can never trigger the permission prompt. It just fails. Grant
it under **System Settings → Privacy & Security → Local Network**. Apple-signed
binaries and processes running as root are unaffected, which is what makes this
so confusing to diagnose.

## Notes for anyone porting Wings elsewhere

The Docker code was not the hard part. The Docker SDK is a pure-Go HTTP client
and cross-compiles to darwin untouched. The work was in `internal/ufs`, the
sandboxed filesystem layer, whose files are tagged `//go:build unix` but use
Linux-only syscalls:

- **`/proc/self/fd/N` does not exist on macOS.** Wings uses it to confirm where a
  descriptor actually landed after opening a multi-component path, since `O_NOFOLLOW`
  only guards the final component, so an intermediate symlink could otherwise
  escape the sandbox. The macOS equivalent is `fcntl(F_GETPATH)`.
- `F_GETPATH` returns **firmlink-resolved** paths, so a base directory under
  `/var`, `/tmp` or `/etc` comes back as `/private/...` and every sandbox check
  fails closed. The base is resolved once and results are translated back.
- There is no `openat2`/`RESOLVE_BENEATH` on XNU, so darwin takes the
  pre-5.6-kernel `openat` path that validates in userspace. `NewUnixFS` clamps
  the request so a caller asking for openat2 cannot get a filesystem where every
  operation returns `ENOSYS`.
- Don't reach for `x/sys/unix.Getdirentries` on darwin. It emulates `getdents`
  by re-opening the directory and skipping N entries per call, which is O(n²).
  Use `fdopendir` (via `os.File.ReadDir`).

Process accounting for the Panel's graphs uses `proc_info(2)`
(`PROC_PIDTASKINFO` / `PROC_PIDTBSDINFO`) directly, so there is no cgo
dependency, and it sums across the process group so a shell wrapper doesn't
under-report.

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
