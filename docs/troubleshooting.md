# Troubleshooting

Part of [Pterodactyl on Mac](../README.md).

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
[the note above](../README.md#watch-out-for-maxrampercentage-in-egg-startup-commands). The
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

Running as root *by accident* also means your game servers run as root, which is
a worse position than upstream Pterodactyl puts you in, where the container
contains a compromised plugin.

Note that this is different from [turning on isolation](../README.md#isolation),
which runs wings as root deliberately. There, root is what lets wings create the
per-server accounts and load the firewall rules, and each server then drops to
an account of its own, so the servers end up **less** privileged, not more. The
problem described here is wings running as root while servers inherit that.

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

Two different causes, and they look identical from the server's side.

If you have **network isolation** on, this is working as intended: a database on
your LAN sits in a private range, and those are blocked. Add its address to
`allow_out` and restart wings:

```yaml
system:
  network_isolation:
    allow_out:
      - 192.168.1.50
```

`sudo pfctl -a wings -s rules` shows what is actually loaded, which is the
quickest way to tell whether a rule is the cause.

If isolation is **off**, it is macOS Local Network permission rather than
networking. See below.

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

## macOS gotcha: Local Network permission

If your servers cannot reach anything on your LAN, such as a database on another
machine, and you get `EHOSTUNREACH` / `NoRouteToHostException` while the
same connection works fine from your shell, this is **not** a networking problem.

macOS 15+ requires Local Network permission, and a process started by `launchd`
has no UI, so it can never trigger the permission prompt. It just fails. Grant
it under **System Settings → Privacy & Security → Local Network**. Apple-signed
binaries and processes running as root are unaffected, which is what makes this
so confusing to diagnose.
