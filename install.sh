#!/usr/bin/env bash
#
# Installer for Pterodactyl on Mac.
#
#   Node only (default) -- this Mac runs game servers, your Panel lives
#   somewhere else, such as a Raspberry Pi on the same network:
#
#     curl -fsSL https://raw.githubusercontent.com/coquetteluke/pterodactyl-on-mac/main/install.sh | bash
#
#   Everything -- Panel and node on this one Mac, nothing else needed:
#
#     curl -fsSL https://raw.githubusercontent.com/coquetteluke/pterodactyl-on-mac/main/install.sh | bash -s -- --full
#
# Read before running: this fork removes the container boundary that upstream
# Wings relies on for isolation. It is for single-tenant machines only. See
# https://github.com/coquetteluke/pterodactyl-on-mac#readme

set -euo pipefail

REPO="${WINGS_REPO:-coquetteluke/pterodactyl-on-mac}"
PREFIX="${WINGS_PREFIX:-$HOME/.local/bin}"
DATA_DIR="${WINGS_DATA_DIR:-$HOME/pterodactyl}"
PANEL_DIR="${PANEL_DIR:-$HOME/pterodactyl-panel}"
# Homebrew's stock nginx.conf already serves its welcome page on 8080, and that
# block is parsed first, so it would win as the default server for the port and
# the Panel would silently never be reached. Pick a port it does not claim.
PANEL_PORT="${PANEL_PORT:-8088}"
LABEL="${WINGS_LABEL:-com.github.pterodactyl-on-mac}"

# Panel 1.15 accepts PHP 8.2 or 8.3 only. Homebrew's unversioned "php" is 8.4
# or newer, which composer refuses outright, so pin the version rather than
# letting people discover that the hard way.
PHP_FORMULA="php@8.3"

info()  { printf '\033[1;34m==>\033[0m %s\n' "$1"; }
warn()  { printf '\033[1;33m warning:\033[0m %s\n' "$1" >&2; }
die()   { printf '\033[1;31m error:\033[0m %s\n' "$1" >&2; exit 1; }

# have_tty reports whether there is a human to ask.
#
# This script is normally run as `curl ... | bash`, which makes stdin the script
# itself. A plain `read` would consume the rest of the script rather than wait
# for an answer, so questions are asked on /dev/tty instead. When there is no
# terminal at all, as in CI, every question falls back to its default.
#
# The test has to be an actual open. /dev/tty exists and passes -r and -w even
# when the process has no controlling terminal; it is only opening it that fails,
# with "Device not configured". Testing the permissions instead means a piped,
# terminal-less run tries to prompt and dies.
have_tty() {
  [ "${ASSUME_YES:-0}" = 0 ] || return 1
  { : < /dev/tty; } 2>/dev/null || return 1
  { : > /dev/tty; } 2>/dev/null
}

# ask_yn asks a yes/no question. $2 is the answer used when nobody is there.
ask_yn() {  # question, default (y|n)
  local q=$1 default=$2 reply hint
  if ! have_tty; then [ "$default" = y ]; return; fi
  [ "$default" = y ] && hint="[Y/n]" || hint="[y/N]"
  while true; do
    printf '\033[1;36m ?\033[0m %s %s ' "$q" "$hint" > /dev/tty
    read -r reply < /dev/tty || reply=""
    reply=${reply:-$default}
    case "$reply" in
      [Yy]|[Yy][Ee][Ss]) return 0 ;;
      [Nn]|[Nn][Oo])     return 1 ;;
      *) printf '    please answer y or n\n' > /dev/tty ;;
    esac
  done
}

# ask_choice presents a numbered menu and echoes the chosen value.
ask_choice() {  # question, default_value, then value:label pairs
  local q=$1 default=$2; shift 2
  local n=1 reply
  if ! have_tty; then echo "$default"; return; fi
  printf '\033[1;36m ?\033[0m %s\n' "$q" > /dev/tty
  local pair
  for pair in "$@"; do
    printf '     %d) %s\n' "$n" "${pair#*:}" > /dev/tty
    n=$((n + 1))
  done
  while true; do
    printf '    choose [1-%d, default %s] ' "$#" "1" > /dev/tty
    read -r reply < /dev/tty || reply=""
    reply=${reply:-1}
    case "$reply" in
      ''|*[!0-9]*) ;;
      *) if [ "$reply" -ge 1 ] && [ "$reply" -le "$#" ]; then
           eval "pair=\${$reply}"
           echo "${pair%%:*}"
           return
         fi ;;
    esac
    printf '    please pick a number between 1 and %d\n' "$#" > /dev/tty
  done
}

main() {
# Unset rather than 0/1, so that asking questions can be skipped for anything
# the command line already answered.
MODE=""
LAUNCHAGENT=""
ISOLATE=""
ACTION=""
PURGE=0
ASSUME_YES=0
NO_VERIFY=0
for arg in "$@"; do
  case "$arg" in
    --full|--panel) MODE=full ;;
    --node) MODE=node ;;
    --launchagent) LAUNCHAGENT=1 ;;
    --isolate) ISOLATE=1 ;;
    --no-isolate) ISOLATE=0 ;;
    --turn-isolation-on) ACTION=isolate ;;
    --turn-isolation-off) ACTION=revert ;;
    --update) ACTION=update ;;
    --uninstall) ACTION=uninstall ;;
    --purge) ACTION=uninstall; PURGE=1 ;;
    --no-verify) NO_VERIFY=1 ;;
    -y|--yes) ASSUME_YES=1 ;;
    -h|--help)
      # Printed inline rather than read back out of $0: when this script is
      # piped into bash there is no file to read.
      cat <<'USAGE'
Installer for Pterodactyl on Mac.

  Node only (default) -- this Mac runs game servers, your Panel lives
  somewhere else, such as a Raspberry Pi on the same network:

    curl -fsSL https://raw.githubusercontent.com/coquetteluke/pterodactyl-on-mac/main/install.sh | bash

  Everything -- Panel and node on this one Mac, nothing else needed:

    curl -fsSL https://raw.githubusercontent.com/coquetteluke/pterodactyl-on-mac/main/install.sh | bash -s -- --full

Run it with no options and it asks you what you want. Answer with the flags
below instead if you are scripting it.

  Options:
    --node          install only the wings daemon
    --full          also install the Panel, MariaDB, PHP and nginx
    --launchagent   start wings automatically
    --isolate       an account, a sandbox and firewall rules per server (default)
    --no-isolate    skip that, and run every server as one unprivileged user
    -y, --yes       take the defaults, ask nothing
    --no-verify     install even if the checksum cannot be verified

  On a machine that already has a node, this same command also manages it:

    --turn-isolation-on    turn isolation on for an existing install
    --turn-isolation-off   turn it back off, handing the server files back
    --update               replace wings with the latest release and restart it
    --uninstall            remove wings, keeping your server data
    --purge                remove wings and the server data too

This fork removes the container boundary that upstream Wings relies on for
isolation. It is for single-tenant machines only. See
https://github.com/coquetteluke/pterodactyl-on-mac#readme
USAGE
      exit 0
      ;;
    *) die "unknown option: $arg (try --help)" ;;
  esac
done

[ "$(uname -s)" = "Darwin" ] || die "this build is macOS only; use upstream Wings on Linux"
command -v curl >/dev/null 2>&1 || die "curl is required"

case "$(uname -m)" in
  arm64) ARCH=arm64 ;;
  x86_64) ARCH=amd64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

adopt_existing_install || true
interview
run
}

# interview fills in anything the command line did not answer.
#
# Every question has a default that is applied when there is no terminal, so the
# same script works piped from curl, run by hand, and from a script.
interview() {
  # On a machine that already has a node, "install this" is rarely what is
  # wanted; managing what is there is. Asking that first saves someone hunting
  # for a second script to run.
  if [ -z "$ACTION" ]; then
    if wings_installed && have_tty; then
      printf '\n'
      ACTION=$(ask_choice "This Mac already has a node on it. What do you want to do?" update \
        "update:Update wings to the latest release, without interrupting your servers" \
        "install:Reinstall it from scratch" \
        "isolate:Turn isolation on" \
        "revert:Turn isolation off" \
        "uninstall:Remove it")
    else
      ACTION=install
    fi
  fi

  case "$ACTION" in
    update)
      update_wings
      exit 0
      ;;
    isolate)
      exec sudo "$(fetch_helper isolate.sh pterodactyl-isolate)"
      ;;
    revert)
      exec sudo "$(fetch_helper isolate.sh pterodactyl-isolate)" --revert
      ;;
    uninstall)
      local u; u=$(fetch_helper uninstall.sh pterodactyl-uninstall)
      if [ "$PURGE" = 1 ]; then exec "$u" --purge; else exec "$u"; fi
      ;;
  esac

  if [ -z "$MODE" ]; then
    if have_tty; then printf '\n'; fi
    MODE=$(ask_choice "What should this Mac do?" node \
      "node:Run game servers for a Panel on another machine, a Pi for instance" \
      "full:Everything on this Mac: the Panel and the game servers")
  fi

  if [ -z "$ISOLATE" ]; then
    if have_tty; then
      cat > /dev/tty <<'EOF'

    Isolation gives every server its own account, its own view of the disk,
    and its own firewall rules, so one server cannot read another's files,
    read your Panel token, or reach the rest of your network.

    It needs to run wings as root in order to create those accounts. The game
    servers themselves end up with fewer privileges than without it, not more.

EOF
    fi
    if ask_yn "Turn on isolation?" y; then ISOLATE=1; else ISOLATE=0; fi
  fi

  if [ -z "$LAUNCHAGENT" ]; then
    if ask_yn "Start wings automatically when this Mac boots?" y; then
      LAUNCHAGENT=1
    else
      LAUNCHAGENT=0
    fi
  fi

  if have_tty; then
    printf '\n'
    info "Installing: $([ "$MODE" = full ] && echo 'Panel and node' || echo 'node only')$([ "$ISOLATE" = 1 ] && echo ', isolated')$([ "$LAUNCHAGENT" = 1 ] && echo ', starts at boot')"
    printf '\n'
  fi
}

# ---------------------------------------------------------------------------
# Wings
# ---------------------------------------------------------------------------

install_wings() {
  info "Resolving the latest release of ${REPO}"
  local tag asset base tmp expected actual
  tag=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | awk -F'"' '/"tag_name"/ {print $4; exit}')
  [ -n "${tag:-}" ] || die "could not determine the latest release; is the repository public and does it have a release?"

  asset="wings_darwin_${ARCH}"
  base="https://github.com/${REPO}/releases/download/${tag}"
  tmp=$(mktemp -d)

  info "Downloading ${asset} (${tag})"
  curl -fsSL -o "${tmp}/wings" "${base}/${asset}" || die "download failed"

  # Verify against the published checksums, and refuse to install if that cannot
  # be done.
  #
  # This fails closed on purpose. The checksums come from the same origin as the
  # binary, so this only ever proved the download was not corrupted, but a
  # missing SHA256SUMS also means something is wrong with the release. For
  # something people run by piping it into a shell, installing anyway on the
  # strength of a warning nobody reads is the wrong default. --no-verify is
  # there for anyone who genuinely needs it.
  if [ "$NO_VERIFY" = 1 ]; then
    warn "skipping checksum verification because --no-verify was given"
  else
    curl -fsSL -o "${tmp}/SHA256SUMS" "${base}/SHA256SUMS" 2>/dev/null \
      || die "could not download SHA256SUMS for ${tag}, so the binary cannot be verified. Re-run with --no-verify to install it unchecked."
    info "Verifying checksum"
    expected=$(awk -v a="$asset" '$2 == a || $2 == "*"a {print $1; exit}' "${tmp}/SHA256SUMS")
    actual=$(shasum -a 256 "${tmp}/wings" | awk '{print $1}')
    [ -n "$expected" ] \
      || die "SHA256SUMS lists no checksum for ${asset}. Re-run with --no-verify to install it unchecked."
    [ "$expected" = "$actual" ] \
      || die "checksum mismatch (expected ${expected}, got ${actual}) -- not installing"
    info "Checksum OK"
  fi

  chmod +x "${tmp}/wings"
  "${tmp}/wings" version >/dev/null 2>&1 || die "the downloaded binary does not run on this machine"

  mkdir -p "$PREFIX"
  mv "${tmp}/wings" "${PREFIX}/wings"
  rm -rf "$tmp"
  info "Installed $("${PREFIX}/wings" version | head -1) to ${PREFIX}/wings"

  mkdir -p "${DATA_DIR}"/{volumes,logs/install,archives,backups,tmp}

  case ":${PATH}:" in
    *":${PREFIX}:"*) ;;
    *) warn "${PREFIX} is not on your PATH; add it to your shell profile" ;;
  esac
}

write_wings_launchagent() {
  local plist="$HOME/Library/LaunchAgents/${LABEL}.plist"
  mkdir -p "$(dirname "$plist")"
  cat > "$plist" <<PLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>${LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>${PREFIX}/wings</string>
        <string>--config</string>
        <string>${DATA_DIR}/config.yml</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <!-- Servers inherit this PATH; whatever your startup command invokes
             must be findable here. -->
        <key>PATH</key>
        <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>
    <key>WorkingDirectory</key>
    <string>${DATA_DIR}</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ThrottleInterval</key>
    <integer>15</integer>
    <key>StandardOutPath</key>
    <string>${DATA_DIR}/logs/wings.stdout.log</string>
    <key>StandardErrorPath</key>
    <string>${DATA_DIR}/logs/wings.stderr.log</string>
    <!-- A game server plus wings opens a lot of files; macOS defaults to 256. -->
    <key>SoftResourceLimits</key>
    <dict>
        <key>NumberOfFiles</key>
        <integer>65536</integer>
    </dict>
</dict>
</plist>
PLISTEOF
  info "Wrote ${plist}"
}

# ensure_egg_tools installs the command-line tools that egg install scripts
# assume are present.
#
# Those scripts are written for the Alpine and Debian installer images, where a
# working jq and wget are a given. macOS ships neither. The failure is
# particularly unhelpful: the Paper script pipes curl through jq to build its
# download URL, so without jq the URL comes out empty, curl writes nothing, the
# install "succeeds", and the server then exits 1 on boot with no output at all
# because there is no jar to run.
ensure_egg_tools() {
  local missing=()
  command -v jq   >/dev/null 2>&1 || missing+=(jq)
  command -v wget >/dev/null 2>&1 || missing+=(wget)
  [ ${#missing[@]} -eq 0 ] && return 0

  if command -v brew >/dev/null 2>&1; then
    info "Installing tools that egg install scripts need: ${missing[*]}"
    brew install "${missing[@]}" >/dev/null 2>&1 || warn "could not install: ${missing[*]}"
  else
    warn "missing ${missing[*]}; most egg install scripts need them. Install Homebrew, then: brew install ${missing[*]}"
  fi
  return 0
}

# ---------------------------------------------------------------------------
# Panel
# ---------------------------------------------------------------------------

brew_prefix() { brew --prefix 2>/dev/null; }

# rand_string prints n random alphanumerics.
#
# The obvious `tr -dc ... </dev/urandom | head -c n` cannot be used here: head
# closes the pipe as soon as it has enough, tr dies of SIGPIPE, and with
# pipefail that surfaces as a fatal exit 141. Bounding the input instead lets
# tr reach EOF on its own.
rand_string() {
  local n=$1 s
  s=$(LC_ALL=C tr -dc 'A-Za-z0-9' < <(head -c $((n * 12)) /dev/urandom))
  printf '%s' "${s:0:n}"
}

install_panel() {
  command -v brew >/dev/null 2>&1 || die "Homebrew is required for the Panel: https://brew.sh"

  local bp php composer_bin
  bp=$(brew_prefix)

  info "Installing Panel dependencies (this takes a few minutes)"
  brew install "$PHP_FORMULA" composer mariadb nginx >/dev/null || die "brew install failed"

  php="${bp}/opt/${PHP_FORMULA}/bin/php"
  [ -x "$php" ] || die "expected PHP at ${php} after installing ${PHP_FORMULA}"
  composer_bin=$(command -v composer) || die "composer not found after install"
  info "Using $("$php" -r 'echo PHP_VERSION;')"

  info "Starting MariaDB"
  brew services start mariadb >/dev/null 2>&1 || true
  # Give MariaDB a moment to accept connections on first start.
  local i
  for _ in $(seq 1 30); do
    "${bp}/bin/mysqladmin" ping >/dev/null 2>&1 && break
    sleep 1
  done
  "${bp}/bin/mysqladmin" ping >/dev/null 2>&1 || die "MariaDB did not start"

  # Credentials are generated rather than prompted so the install can run
  # unattended; they are printed once at the end.
  local dbpass adminpass
  dbpass=$(rand_string 32)
  adminpass=$(rand_string 20)

  info "Creating the panel database"
  "${bp}/bin/mysql" <<SQL
CREATE DATABASE IF NOT EXISTS panel CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'pterodactyl'@'127.0.0.1' IDENTIFIED BY '${dbpass}';
ALTER USER 'pterodactyl'@'127.0.0.1' IDENTIFIED BY '${dbpass}';
GRANT ALL PRIVILEGES ON panel.* TO 'pterodactyl'@'127.0.0.1';
FLUSH PRIVILEGES;
SQL

  if [ -d "${PANEL_DIR}/.git" ] || [ -f "${PANEL_DIR}/artisan" ]; then
    die "a Panel already exists at ${PANEL_DIR}; remove it or set PANEL_DIR to install elsewhere"
  fi

  info "Downloading the Panel"
  mkdir -p "$PANEL_DIR"
  curl -fsSL "https://github.com/pterodactyl/panel/releases/latest/download/panel.tar.gz" \
    | tar -xz -C "$PANEL_DIR" || die "could not download the Panel"
  chmod -R 755 "${PANEL_DIR}/storage" "${PANEL_DIR}/bootstrap/cache" 2>/dev/null || true

  info "Installing PHP dependencies"
  ( cd "$PANEL_DIR" && "$php" "$composer_bin" install --no-dev --optimize-autoloader --no-interaction ) \
    </dev/null >/dev/null || die "composer install failed"

  info "Configuring the Panel"
  local url="http://$(hostname -s).local:${PANEL_PORT}"
  ( cd "$PANEL_DIR"
    cp -n .env.example .env
    "$php" artisan key:generate --force --no-interaction >/dev/null
    "$php" artisan p:environment:setup \
      --author="admin@$(hostname -s).local" \
      --url="$url" \
      --timezone="$(readlink /etc/localtime | sed 's|.*/zoneinfo/||')" \
      --cache=file --session=database --queue=database \
      --settings-ui=true --telemetry=false --no-interaction >/dev/null
    "$php" artisan p:environment:database \
      --host=127.0.0.1 --port=3306 --database=panel \
      --username=pterodactyl --password="$dbpass" --no-interaction >/dev/null
    "$php" artisan migrate --seed --force --no-interaction >/dev/null
    "$php" artisan p:user:make \
      --email="admin@$(hostname -s).local" --username=admin \
      --name-first=Admin --name-last=User \
      --password="$adminpass" --admin=1 --no-interaction >/dev/null
  ) </dev/null || die "panel setup failed"

  write_panel_services "$php" "$bp" "$url" "$dbpass" "$adminpass"
}

write_panel_services() {
  local php="$1" bp="$2" url="$3" dbpass="$4" adminpass="$5"
  local agents="$HOME/Library/LaunchAgents"
  mkdir -p "$agents" "${PANEL_DIR}/storage/logs"

  # nginx serves the Panel on an unprivileged port; binding 80 or 443 would
  # need root, which the rest of this install deliberately avoids.
  local conf="${bp}/etc/nginx/servers/pterodactyl.conf"
  mkdir -p "$(dirname "$conf")"

  # Refuse to fight another server block for the port; nginx would start
  # cleanly and simply serve the wrong site, which is worse than failing here.
  if grep -qE "^[^#]*listen[[:space:]]+${PANEL_PORT}\\b" "${bp}/etc/nginx/nginx.conf" 2>/dev/null; then
    die "nginx.conf already has a server on port ${PANEL_PORT}; re-run with PANEL_PORT set to a free port"
  fi
  cat > "$conf" <<NGINX
server {
    # default_server so this block answers requests for any Host on this port,
    # rather than only ones that happen to match a server_name.
    listen ${PANEL_PORT} default_server;
    server_name _;
    root ${PANEL_DIR}/public;
    index index.php;

    access_log ${PANEL_DIR}/storage/logs/nginx.access.log;
    error_log  ${PANEL_DIR}/storage/logs/nginx.error.log error;

    client_max_body_size 100m;

    location / { try_files \$uri \$uri/ /index.php?\$query_string; }

    location ~ \.php\$ {
        fastcgi_pass 127.0.0.1:9000;
        fastcgi_index index.php;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME \$document_root\$fastcgi_script_name;
        # Prevent the Httpoxy request-header attack from reaching PHP.
        fastcgi_param HTTP_PROXY "";
    }

    location ~ /\.ht { deny all; }
}
NGINX

  brew services start "$PHP_FORMULA" >/dev/null 2>&1 || true
  brew services restart nginx >/dev/null 2>&1 || brew services start nginx >/dev/null 2>&1 || true

  # Pterodactyl needs a queue worker and a once-a-minute scheduler. On Linux
  # those are a systemd unit and a crontab entry; here they are LaunchAgents.
  cat > "${agents}/com.pterodactyl.panel.queue.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.pterodactyl.panel.queue</string>
<key>ProgramArguments</key><array>
<string>${php}</string><string>${PANEL_DIR}/artisan</string><string>queue:work</string>
<string>--queue=high,standard,low</string><string>--sleep=3</string><string>--tries=3</string>
</array>
<key>WorkingDirectory</key><string>${PANEL_DIR}</string>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>15</integer>
<key>StandardOutPath</key><string>${PANEL_DIR}/storage/logs/queue.log</string>
<key>StandardErrorPath</key><string>${PANEL_DIR}/storage/logs/queue.log</string>
</dict></plist>
PLIST

  cat > "${agents}/com.pterodactyl.panel.schedule.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.pterodactyl.panel.schedule</string>
<key>ProgramArguments</key><array>
<string>${php}</string><string>${PANEL_DIR}/artisan</string><string>schedule:run</string>
</array>
<key>WorkingDirectory</key><string>${PANEL_DIR}</string>
<key>RunAtLoad</key><false/>
<key>StartInterval</key><integer>60</integer>
<key>StandardOutPath</key><string>${PANEL_DIR}/storage/logs/schedule.log</string>
<key>StandardErrorPath</key><string>${PANEL_DIR}/storage/logs/schedule.log</string>
</dict></plist>
PLIST

  local uid; uid=$(id -u)
  launchctl bootout "gui/${uid}/com.pterodactyl.panel.queue" 2>/dev/null || true
  launchctl bootout "gui/${uid}/com.pterodactyl.panel.schedule" 2>/dev/null || true
  launchctl bootstrap "gui/${uid}" "${agents}/com.pterodactyl.panel.queue.plist" 2>/dev/null || true
  launchctl bootstrap "gui/${uid}" "${agents}/com.pterodactyl.panel.schedule.plist" 2>/dev/null || true

  # The umask is set in a subshell rather than chmod'ing afterwards: between
  # creating the file and tightening it, the admin and database passwords would
  # be readable by anyone on the machine for as long as the write took.
  ( umask 077
    cat > "${PANEL_DIR}/CREDENTIALS.txt" <<CREDS
Pterodactyl Panel, generated $(date)

  URL:            ${url}
  Admin login:    admin@$(hostname -s).local
  Admin password: ${adminpass}

  Database:       panel
  DB user:        pterodactyl@127.0.0.1
  DB password:    ${dbpass}

Keep this file, or change the admin password after your first login.
CREDS
  )
  # Belt and braces: the umask covers creation, this covers a pre-existing file
  # from an earlier run, whose mode a redirect would not change.
  chmod 600 "${PANEL_DIR}/CREDENTIALS.txt"
}

# ---------------------------------------------------------------------------

# fetch_helper installs one of the companion scripts next to wings.
#
# They stay separate files rather than being folded in here because each is
# needed again after the install is over: isolation has to be written into a
# config.yml that does not exist until the node has been created in the Panel,
# and uninstalling obviously happens later. This script is the only URL anyone
# has to know; it fetches the others as they are needed.
fetch_helper() {  # source name, installed name
  local src=$1 dest="${PREFIX}/$2"
  mkdir -p "$PREFIX"
  curl -fsSL "https://raw.githubusercontent.com/${REPO}/main/${src}" -o "${dest}.tmp" \
    || die "could not download ${src}"
  chmod 755 "${dest}.tmp"
  mv "${dest}.tmp" "$dest"
  echo "$dest"
}

install_isolate_script() {
  local dest
  dest=$(fetch_helper isolate.sh pterodactyl-isolate)
  info "Installing ${dest}"
}

# adopt_existing_install learns where wings actually is, rather than assuming.
#
# The defaults here describe a fresh install done by this script. A node set up
# by hand, or by an older version, can have the binary somewhere else and be
# registered under a different service label, and every management action would
# then look at the wrong paths and report that nothing is installed.
#
# A launchd plist records both the binary and the config path it was given, so
# an existing service is the most reliable description of the layout available.
# Anything set explicitly through the environment still wins.
adopt_existing_install() {
  local f label prog cfg
  for f in /Library/LaunchDaemons/*.plist "$HOME/Library/LaunchAgents/"*.plist; do
    [ -f "$f" ] || continue
    grep -q '<string>[^<]*/wings</string>' "$f" 2>/dev/null || continue

    label=$(basename "$f" .plist)
    prog=$(awk -F'[<>]' '/<string>[^<]*\/wings<\/string>/{print $3; exit}' "$f")
    cfg=$(awk -F'[<>]' '/<string>[^<]*config\.yml<\/string>/{print $3; exit}' "$f")

    [ -n "${WINGS_LABEL:-}" ]    || LABEL="$label"
    if [ -z "${WINGS_PREFIX:-}" ] && [ -n "$prog" ]; then PREFIX=$(dirname "$prog"); fi
    if [ -z "${WINGS_DATA_DIR:-}" ] && [ -n "$cfg" ]; then DATA_DIR=$(dirname "$cfg"); fi
    return 0
  done
  return 1
}

# find_wings returns the path to the installed binary, or nothing.
#
# Deliberately not a bare command substitution at a call site: under `set -e` a
# failing one aborts the script at the assignment, before any error message can
# run, which is how this used to fail completely silently.
find_wings() {
  local c
  for c in "${PREFIX}/wings" "$HOME/.local/bin/wings" "$HOME/bin/wings" \
           /usr/local/bin/wings /opt/homebrew/bin/wings; do
    if [ -x "$c" ]; then echo "$c"; return 0; fi
  done
  if c=$(command -v wings 2>/dev/null) && [ -n "$c" ]; then echo "$c"; return 0; fi
  return 1
}

# wings_installed reports whether this machine already has a node on it, which
# decides whether the first question is "what do you want" or "what now".
wings_installed() {
  find_wings >/dev/null 2>&1 || [ -f "${DATA_DIR}/config.yml" ]
}

# restart_wings restarts the daemon wherever it happens to be registered.
#
# It lives in the system domain once isolation is on and in the user's domain
# otherwise, and picking the wrong one silently does nothing.
restart_wings() {
  if [ -f "/Library/LaunchDaemons/${LABEL}.plist" ]; then
    sudo launchctl kickstart -k "system/${LABEL}" 2>/dev/null && return 0
  fi
  if launchctl print "gui/$(id -u)/${LABEL}" >/dev/null 2>&1; then
    launchctl kickstart -k "gui/$(id -u)/${LABEL}" 2>/dev/null && return 0
  fi
  return 1
}

# update_wings replaces the binary and restarts, leaving everything else alone.
#
# Your game servers keep running throughout. They are detached into their own
# sessions precisely so they survive wings restarting, and the new wings adopts
# them by pid on the way back up, so updating costs no downtime for players.
update_wings() {
  local before after bin
  # Guarded rather than assigned directly: `set -e` would otherwise abort here
  # with no output at all when the binary is missing.
  if ! bin=$(find_wings); then
    die "no wings binary found. Looked in ${PREFIX}, ~/.local/bin, ~/bin, /usr/local/bin and on your PATH. Install it first."
  fi
  PREFIX=$(dirname "$bin")

  before=$("$bin" version 2>/dev/null | head -1) || true
  [ -n "$before" ] || die "found ${bin} but it would not run; is it the right build for this Mac?"
  info "Installed: ${before}  (${bin})"

  local latest
  latest=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | awk -F'"' '/"tag_name"/ {print $4; exit}')
  [ -n "${latest:-}" ] || die "could not reach GitHub to check for a newer release"

  # `wings version` prints "wings v1.2.3", the tag is "v1.2.3".
  if [ "$before" = "wings ${latest}" ]; then
    info "Already on the latest release (${latest}). Nothing to do."
    return 0
  fi
  info "Latest is ${latest}"

  # Downloads to a temporary directory, verifies the checksum, and only then
  # renames into place. A rename is what makes replacing a running binary safe:
  # writing to it directly fails with ETXTBSY, and the running process keeps
  # the old inode until it is restarted either way.
  install_wings

  after=$("$bin" version 2>/dev/null | head -1) || true
  if restart_wings; then
    info "Restarted wings. Your game servers were not interrupted."
  else
    # Nearly always because sudo had nowhere to ask for a password, which is the
    # case when this is piped from curl with no terminal attached. Say what to
    # run rather than leaving someone to work it out.
    warn "the new binary is installed, but wings is still running the old one"
    if [ -f "/Library/LaunchDaemons/${LABEL}.plist" ]; then
      printf '  Finish with:  sudo launchctl kickstart -k system/%s\n\n' "$LABEL"
    else
      printf '  Finish with:  launchctl kickstart -k gui/%s/%s\n\n' "$(id -u)" "$LABEL"
    fi
  fi

  printf '\n'
  info "${before}  ->  ${after}"
  printf '\n'
}

# setup_isolation turns on everything that stands in for a container.
setup_isolation() {
  info "Setting up isolation"
  install_isolate_script

  if [ ! -f "${DATA_DIR}/config.yml" ]; then
    # Node mode: the Panel has not been told about this machine yet, so there is
    # no config.yml to switch anything on in.
    cat <<EOF

$(warn "isolation not switched on yet, because ${DATA_DIR}/config.yml does not exist")

  Create the node in your Panel, paste its configuration to that path, then run:

      sudo ${PREFIX}/pterodactyl-isolate

EOF
    return 0
  fi

  # Isolation is the default, so it must not be able to fail the whole install.
  # Without a terminal there is nowhere for sudo to ask for a password, and a
  # scripted run would otherwise die here having already installed everything
  # else successfully.
  if ! sudo -n true 2>/dev/null && ! have_tty; then
    cat <<EOF

$(warn "isolation skipped: it needs sudo, and there is no terminal to ask for a password")

  Everything else installed. Turn isolation on when you are at a terminal:

      sudo ${PREFIX}/pterodactyl-isolate

EOF
    return 0
  fi

  if ! sudo "${PREFIX}/pterodactyl-isolate"; then
    warn "isolation setup did not complete; wings has been left as it was"
    printf '  Retry it on its own with: sudo %s/pterodactyl-isolate\n\n' "$PREFIX"
  fi
}

run() {
if [ "$MODE" = full ]; then
  install_panel
fi

ensure_egg_tools
install_wings

# The LaunchAgent is written first even when isolating, because the isolate
# step builds the LaunchDaemon from it, and --revert puts it back afterwards.
if [ "$LAUNCHAGENT" = 1 ] || [ "$ISOLATE" = 1 ]; then
  write_wings_launchagent
fi

if [ "$ISOLATE" = 1 ]; then
  setup_isolation
fi

if [ "$MODE" = full ]; then
  url="http://$(hostname -s).local:${PANEL_PORT}"
  cat <<EOF

$(info "Panel and node installed")

  Your Panel is at ${url}
  Credentials are in ${PANEL_DIR}/CREDENTIALS.txt (also shown below once):

$(sed 's/^/    /' "${PANEL_DIR}/CREDENTIALS.txt")

  To finish, create the node this Mac will run servers on:

    1. Log in, then Admin -> Locations -> Create, and make one.
    2. Admin -> Nodes -> Create Node.
         FQDN:        $(hostname -s).local     (or this Mac's IP)
         Daemon Port: 8443                     (not 443: an unprivileged
                                                process cannot bind it)
         Behind proxy / SSL: off, unless you have set up TLS yourself.
    3. Open the node's Configuration tab, copy the YAML, and save it to
       ${DATA_DIR}/config.yml
    4. Add these two lines to that file so servers run as host processes and
       the Panel cannot push the port back to 443:

         system:
           environment: native
         ignore_panel_config_updates: true

    5. Start wings:

         ${PREFIX}/wings --config ${DATA_DIR}/config.yml

  Install whatever your servers actually run -- java, node, python -- and make
  sure it is on the PATH of whatever starts wings. There is no container image
  to supply it.

EOF
else
  cat <<EOF

$(info "Next steps")

  This Mac is now ready to act as a node for a Panel running elsewhere -- on a
  Raspberry Pi, another machine on your network, or anywhere it can reach this
  one.

  1. In that Panel: Admin -> Nodes -> Create Node.
       FQDN:        this Mac's hostname or IP, reachable from the Panel
       Daemon Port: 8443  (not 443: an unprivileged process cannot bind it)

  2. Open the node's Configuration tab, copy the YAML, and save it to
     ${DATA_DIR}/config.yml

  3. Add these lines to that file:

       system:
         environment: native
         root_directory: ${DATA_DIR}
         data: ${DATA_DIR}/volumes
       ignore_panel_config_updates: true

  4. Install whatever your servers run -- java, node, python -- and make sure it
     is on the PATH of whatever starts wings.

  5. Run it:

       ${PREFIX}/wings --config ${DATA_DIR}/config.yml

     To keep it running across reboots:

       curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | bash -s -- --launchagent

  If your servers need to reach anything on your LAN (a database on another
  machine, say) you must also grant Local Network permission to wings under
  System Settings -> Privacy & Security -> Local Network. Without it those
  connections fail with "no route to host" even though the network is fine.

EOF
fi
}

# Everything above is definitions only. Nothing runs until this line, which is
# what makes `curl ... | bash` safe: bash has to read the entire script to parse
# these functions, so a half-downloaded script cannot execute a truncated
# install, and no child process can consume the remainder of the script as its
# own stdin.
main "$@"
