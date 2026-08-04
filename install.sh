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
LABEL="com.github.pterodactyl-on-mac"

# Panel 1.15 accepts PHP 8.2 or 8.3 only. Homebrew's unversioned "php" is 8.4
# or newer, which composer refuses outright, so pin the version rather than
# letting people discover that the hard way.
PHP_FORMULA="php@8.3"

info()  { printf '\033[1;34m==>\033[0m %s\n' "$1"; }
warn()  { printf '\033[1;33m warning:\033[0m %s\n' "$1" >&2; }
die()   { printf '\033[1;31m error:\033[0m %s\n' "$1" >&2; exit 1; }

MODE=node
LAUNCHAGENT=0
for arg in "$@"; do
  case "$arg" in
    --full|--panel) MODE=full ;;
    --node) MODE=node ;;
    --launchagent) LAUNCHAGENT=1 ;;
    -h|--help)
      sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'
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
  trap 'rm -rf "$tmp"' RETURN

  info "Downloading ${asset} (${tag})"
  curl -fsSL -o "${tmp}/wings" "${base}/${asset}" || die "download failed"

  # Verify against the published checksums so a corrupted or tampered download
  # is caught before it is installed.
  if curl -fsSL -o "${tmp}/SHA256SUMS" "${base}/SHA256SUMS" 2>/dev/null; then
    info "Verifying checksum"
    expected=$(awk -v a="$asset" '$2 == a || $2 == "*"a {print $1; exit}' "${tmp}/SHA256SUMS")
    actual=$(shasum -a 256 "${tmp}/wings" | awk '{print $1}')
    if [ -z "$expected" ]; then
      warn "no checksum listed for ${asset}; skipping verification"
    elif [ "$expected" != "$actual" ]; then
      die "checksum mismatch (expected ${expected}, got ${actual}) -- not installing"
    else
      info "Checksum OK"
    fi
  else
    warn "no SHA256SUMS published for ${tag}; skipping verification"
  fi

  chmod +x "${tmp}/wings"
  "${tmp}/wings" version >/dev/null 2>&1 || die "the downloaded binary does not run on this machine"

  mkdir -p "$PREFIX"
  mv "${tmp}/wings" "${PREFIX}/wings"
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
  for i in $(seq 1 30); do
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
    >/dev/null || die "composer install failed"

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
  ) || die "panel setup failed"

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

  cat > "${PANEL_DIR}/CREDENTIALS.txt" <<CREDS
Pterodactyl Panel — generated $(date)

  URL:            ${url}
  Admin login:    admin@$(hostname -s).local
  Admin password: ${adminpass}

  Database:       panel
  DB user:        pterodactyl@127.0.0.1
  DB password:    ${dbpass}

Keep this file, or change the admin password after your first login.
CREDS
  chmod 600 "${PANEL_DIR}/CREDENTIALS.txt"
}

# ---------------------------------------------------------------------------

if [ "$MODE" = full ]; then
  install_panel
fi

install_wings
[ "$LAUNCHAGENT" = 1 ] && write_wings_launchagent

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
