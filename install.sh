#!/usr/bin/env bash
#
# Installer for Pterodactyl on Mac, an unofficial macOS build of Pterodactyl Wings.
#
#   curl -fsSL https://raw.githubusercontent.com/coquetteluke/pterodactyl-on-mac/main/install.sh | bash
#
# This installs the binary and, optionally, a LaunchAgent to keep it running.
# It does NOT write a config.yml -- that comes from your Panel, under
# Admin -> Nodes -> your node -> Configuration.
#
# Read before running: this fork removes the container boundary that upstream
# Wings relies on for isolation. It is for single-tenant machines only. See
# https://github.com/coquetteluke/pterodactyl-on-mac#readme

set -euo pipefail

REPO="${WINGS_REPO:-coquetteluke/pterodactyl-on-mac}"
PREFIX="${WINGS_PREFIX:-$HOME/.local/bin}"
DATA_DIR="${WINGS_DATA_DIR:-$HOME/pterodactyl}"
LABEL="com.github.pterodactyl-on-mac"

info()  { printf '\033[1;34m==>\033[0m %s\n' "$1"; }
warn()  { printf '\033[1;33m warning:\033[0m %s\n' "$1" >&2; }
die()   { printf '\033[1;31m error:\033[0m %s\n' "$1" >&2; exit 1; }

[ "$(uname -s)" = "Darwin" ] || die "this build is macOS only; use upstream Wings on Linux"

case "$(uname -m)" in
  arm64) ARCH=arm64 ;;
  x86_64) ARCH=amd64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

command -v curl >/dev/null 2>&1 || die "curl is required"

info "Resolving the latest release of ${REPO}"
TAG=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | awk -F'"' '/"tag_name"/ {print $4; exit}')
[ -n "${TAG:-}" ] || die "could not determine the latest release; is the repository public and does it have a release?"

ASSET="wings_darwin_${ARCH}"
BASE="https://github.com/${REPO}/releases/download/${TAG}"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

info "Downloading ${ASSET} (${TAG})"
curl -fsSL -o "${TMP}/wings" "${BASE}/${ASSET}" || die "download failed"

# Verify against the published checksums so a corrupted or tampered download is
# caught before it is installed.
if curl -fsSL -o "${TMP}/SHA256SUMS" "${BASE}/SHA256SUMS" 2>/dev/null; then
  info "Verifying checksum"
  expected=$(awk -v a="$ASSET" '$2 == a || $2 == "*"a {print $1; exit}' "${TMP}/SHA256SUMS")
  actual=$(shasum -a 256 "${TMP}/wings" | awk '{print $1}')
  if [ -z "$expected" ]; then
    warn "no checksum listed for ${ASSET}; skipping verification"
  elif [ "$expected" != "$actual" ]; then
    die "checksum mismatch (expected ${expected}, got ${actual}) -- not installing"
  else
    info "Checksum OK"
  fi
else
  warn "no SHA256SUMS published for ${TAG}; skipping verification"
fi

chmod +x "${TMP}/wings"
"${TMP}/wings" version >/dev/null 2>&1 || die "the downloaded binary does not run on this machine"

mkdir -p "$PREFIX"
mv "${TMP}/wings" "${PREFIX}/wings"
info "Installed $("${PREFIX}/wings" version | head -1) to ${PREFIX}/wings"

mkdir -p "${DATA_DIR}"/{volumes,logs/install,archives,backups,tmp}
info "Created data directories under ${DATA_DIR}"

case ":${PATH}:" in
  *":${PREFIX}:"*) ;;
  *) warn "${PREFIX} is not on your PATH; add it to your shell profile" ;;
esac

cat <<EOF

$(info "Next steps")

  1. Put your node's config at ${DATA_DIR}/config.yml
     (Panel -> Admin -> Nodes -> your node -> Configuration tab)

  2. Tell it to run servers as host processes, and use a data directory
     your user can write:

       system:
         environment: native
         root_directory: ${DATA_DIR}
         data: ${DATA_DIR}/volumes

  3. An unprivileged process cannot bind ports below 1024. If your node's
     Daemon Port is 443, either change it, put a reverse proxy in front, or
     pin a high port yourself:

       api:
         port: 8443
       ignore_panel_config_updates: true

     (the last line stops the Panel pushing 443 back down when a node is saved)

  4. Install whatever your servers actually run -- java, node, python -- and
     make sure it is on the PATH of whatever starts wings. There is no
     container image to supply it.

  5. Run it:

       ${PREFIX}/wings --config ${DATA_DIR}/config.yml

     To keep it running across reboots, install a LaunchAgent:

       curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | bash -s -- --launchagent

EOF

if [ "${1:-}" = "--launchagent" ]; then
  PLIST="$HOME/Library/LaunchAgents/${LABEL}.plist"
  mkdir -p "$(dirname "$PLIST")"
  cat > "$PLIST" <<PLISTEOF
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
  info "Wrote ${PLIST}"
  cat <<EOF

  Load it once your config.yml is in place:

    launchctl bootstrap gui/\$(id -u) "${PLIST}"

  Note: a LaunchAgent needs a login session to run at boot, so the Mac must be
  set to log in automatically if it is headless.

  If your servers need to reach anything on your LAN (a database on another
  machine, say) you must also grant Local Network permission to wings under
  System Settings -> Privacy & Security -> Local Network. Without it those
  connections fail with "no route to host" even though the network is fine.

EOF
fi
