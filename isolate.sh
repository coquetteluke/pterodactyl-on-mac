#!/usr/bin/env bash
#
# Turns the isolation stack on, or back off.
#
# Containers give upstream Wings three things at once: servers cannot read each
# other's files, cannot reach each other over the network, and cannot touch the
# host. macOS has no containers, so this rebuilds each piece from what the
# system does have:
#
#   * an account per server, so unix permissions keep them apart
#   * the kernel sandbox, so that holds even when the permissions do not
#   * pf rules keyed on each server's uid, so one cannot reach your Panel,
#     your LAN, or anything else on the machine
#
# The first and third need root, so this moves wings from a LaunchAgent to a
# LaunchDaemon. The servers themselves end up less privileged than before, since
# each drops to an account of its own.
#
# Usage:
#   sudo ./isolate.sh              turn it on
#   sudo ./isolate.sh --revert     turn it off and hand the files back
#
set -euo pipefail

PREFIX="${WINGS_PREFIX:-}"
DATA_DIR="${WINGS_DATA_DIR:-}"
LABEL="${WINGS_LABEL:-com.github.pterodactyl-on-mac}"
ANCHOR_FILE=/etc/pf.anchors/com.pterodactyl.wings
PF_CONF=/etc/pf.conf
PF_BACKUP=/etc/pf.conf.wings-backup
DAEMON_DIR=/Library/LaunchDaemons

info() { printf '\033[1;34m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[1;33m warning:\033[0m %s\n' "$1" >&2; }
die()  { printf '\033[1;31m error:\033[0m %s\n' "$1" >&2; exit 1; }

main() {
# Help comes before the privilege check, so that someone can find out what this
# does without first being told to run an unknown script as root.
case "${1:-}" in
  -h|--help) usage; exit 0 ;;
esac

[ "$(id -u)" = 0 ] || die "run this with sudo"

# sudo keeps the invoking user in SUDO_USER. Everything installed by the
# non-root installer lives in that user's home, so without it there is no way
# to find the LaunchAgent or the data directory.
OWNER="${SUDO_USER:-}"
[ -n "$OWNER" ] || die "run this through sudo, not as a root login, so the original user can be identified"
OWNER_UID=$(id -u "$OWNER") || die "cannot resolve the user $OWNER"
OWNER_GID=$(id -g "$OWNER")
OWNER_HOME=$(dscl . -read "/Users/${OWNER}" NFSHomeDirectory 2>/dev/null | awk '{print $2}')
[ -n "$OWNER_HOME" ] || die "cannot resolve the home directory of $OWNER"

[ -n "$PREFIX" ]   || PREFIX="${OWNER_HOME}/.local/bin"
[ -n "$DATA_DIR" ] || DATA_DIR="${OWNER_HOME}/pterodactyl"
AGENT_PLIST="${OWNER_HOME}/Library/LaunchAgents/${LABEL}.plist"
DAEMON_PLIST="${DAEMON_DIR}/${LABEL}.plist"
CONFIG="${DATA_DIR}/config.yml"

FORCE=0
ACTION=enable
for arg in "$@"; do
  case "$arg" in
    --revert) ACTION=revert ;;
    --force)  FORCE=1 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $arg (try --help)" ;;
  esac
done
[ "$ACTION" = revert ] && revert

enable_isolation
}

usage() {
  cat <<'USAGE'
Turn the isolation stack on, or back off.

  sudo ./isolate.sh              an account, a sandbox and firewall rules per server
  sudo ./isolate.sh --revert     undo it, and hand the server files back

Wings moves to a LaunchDaemon running as root, which it needs to create the
accounts and load the firewall rules. Servers themselves end up less privileged
than before.
USAGE
}

# ---------------------------------------------------------------------------

# pf ignores an anchor nothing references, and macOS's stock pf.conf references
# only Apple's own. Without this the rules load and enforce nothing, which looks
# exactly like success.
add_pf_anchor() {
  # The anchor file has to exist before pf.conf is validated: a `load anchor
  # ... from` directive naming a missing file is a parse error, so creating it
  # afterwards would make the check below fail every time.
  mkdir -p "$(dirname "$ANCHOR_FILE")"
  [ -f "$ANCHOR_FILE" ] || echo "# Populated by wings on startup." > "$ANCHOR_FILE"

  if grep -q "^anchor \"wings\"" "$PF_CONF" 2>/dev/null; then
    info "pf already references the wings anchor"
  else
    info "Adding the wings anchor to ${PF_CONF}"
    cp "$PF_CONF" "$PF_BACKUP"
    # Appended, not inserted: both directives have to come after Apple's rules.
    cat >> "$PF_CONF" <<'PFEOF'

# Added by pterodactyl-on-mac. Without these the rules wings generates are
# loaded but never evaluated.
anchor "wings"
load anchor "wings" from "/etc/pf.anchors/com.pterodactyl.wings"
PFEOF
    if ! pfctl -n -f "$PF_CONF" >/dev/null 2>&1; then
      cp "$PF_BACKUP" "$PF_CONF"
      die "the edited ${PF_CONF} does not parse; it has been restored from the backup"
    fi
  fi
}

# set_config_flag edits one key inside a block of config.yml.
#
# A YAML parser would be better, but nothing ships with macOS that can rewrite
# YAML in place while preserving comments, and pulling in a dependency for three
# booleans is worse. The edits are anchored to their block so a key of the same
# name elsewhere in the file cannot be hit by accident.
set_config_flag() {  # block, key, value
  local block=$1 key=$2 value=$3
  /usr/bin/sed -i '' -e "/^  ${block}:/,/^  [a-z_]*:/ s/^\( *${key}:\).*/\1 ${value}/" "$CONFIG"
}

enable_isolation() {
  [ -f "$AGENT_PLIST" ] || [ -f "$DAEMON_PLIST" ] || \
    die "no wings service found; run install.sh first"

  if [ ! -f "$CONFIG" ]; then
    die "no ${CONFIG} yet.

Create the node in your Panel first, paste its configuration to that path, then
run this again. Isolation has to be written into that file, and it does not
exist until the node does."
  fi

  add_pf_anchor

  info "Stopping wings (servers keep running; they are detached from it)"
  launchctl bootout "gui/${OWNER_UID}/${LABEL}" 2>/dev/null || true
  launchctl bootout "system/${LABEL}" 2>/dev/null || true

  if [ -f "$AGENT_PLIST" ]; then
    info "Moving wings to a LaunchDaemon so it can run as root"
    cp "$AGENT_PLIST" "$DAEMON_PLIST"
    # Keep the agent around: --revert puts it straight back.
    chown root:wheel "$DAEMON_PLIST"
    chmod 644 "$DAEMON_PLIST"
  fi
  plutil -lint "$DAEMON_PLIST" >/dev/null || die "the generated ${DAEMON_PLIST} is malformed"

  info "Switching on per-server accounts, the sandbox and network isolation"
  set_config_flag "user"              "per_server" "true"
  set_config_flag "sandbox"           "enabled"    "true"
  set_config_flag "network_isolation" "enabled"    "true"

  grep -q "per_server: true" "$CONFIG" || die "could not set per_server in ${CONFIG}"

  info "Starting wings as root"
  launchctl bootstrap system "$DAEMON_PLIST"

  cat <<DONE

$(info "Isolation is on")

  Restart each server from the Panel to pick it up. On its next boot a server
  gets its own account, its files are chowned to it, and its firewall rules are
  loaded.

  Check it worked:

    sudo pfctl -a wings -s rules     the generated policy, one block per server
    dscl . -list /Users | grep ptero the per-server accounts

  Two things change:

    * Server files stop being readable from your own shell. Use SFTP or the
      Panel's file manager rather than editing them over ssh.
    * A server can only be reached on the ports allocated to it in the Panel.
      Anything else it happens to listen on stops being reachable from your
      network. Add it as an allocation if you want it back.

  If a plugin needs something on your LAN, a database for instance, add its
  address to allow_out in ${CONFIG} and restart wings:

      system:
        network_isolation:
          allow_out:
            - 192.168.1.50

  To undo all of this:  sudo ./isolate.sh --revert

DONE
}

# ---------------------------------------------------------------------------

# wait_for_wings waits for the service to actually come up.
#
# launchctl bootstrap only registers the job; launchd spawns it a moment later.
# Checking immediately reports a healthy service as dead, so wait for the
# process rather than for the command that asked for it.
wait_for_wings() {
  local waited=0
  while [ "$waited" -lt 20 ]; do
    pgrep -f "${PREFIX}/wings" >/dev/null 2>&1 && return 0
    sleep 1
    waited=$((waited + 1))
  done
  return 1
}

# running_servers lists the pids of servers still running.
#
# Wings detaches servers into their own session so they survive it restarting,
# which means stopping wings does not stop them. Each server's pid is recorded
# in its runtime directory.
running_servers() {
  local f pid
  for f in "${DATA_DIR}"/native/*/pid; do
    [ -f "$f" ] || continue
    pid=$(awk '{print $1; exit}' "$f" 2>/dev/null) || continue
    [ -n "$pid" ] || continue
    kill -0 "$pid" 2>/dev/null && echo "$pid"
  done
  return 0
}

revert() {
  # Reverting hands the server files back from the per-server accounts and then
  # deletes those accounts. Doing that underneath a running server leaves it
  # owned by nobody, unable to write its own world, and still running -- which
  # looks fine until the next save fails. Refuse rather than corrupt.
  local running
  running=$(running_servers)
  if [ -n "$running" ] && [ "${FORCE:-0}" != 1 ]; then
    die "servers are still running (pids: $(echo "$running" | tr '\n' ' '))

Reverting changes who owns their files and deletes the accounts they run as, so
a server left running would lose access to its own data mid-flight.

Stop them from the Panel first, then run this again.
Or pass --force to stop them abruptly and revert anyway."
  fi

  if [ -n "$running" ]; then
    warn "stopping running servers because --force was given"
    echo "$running" | while read -r pid; do
      [ -n "$pid" ] || continue
      kill -TERM "$pid" 2>/dev/null || true
    done
    local waited=0
    while [ -n "$(running_servers)" ] && [ "$waited" -lt 30 ]; do
      sleep 1; waited=$((waited + 1))
    done
    echo "$(running_servers)" | while read -r pid; do
      [ -n "$pid" ] || continue
      kill -9 "$pid" 2>/dev/null || true
    done
  fi

  info "Turning isolation off"

  launchctl bootout "system/${LABEL}" 2>/dev/null || true

  # Drop the rules. pf itself is left running: Internet Sharing and some VPN
  # clients enable it too, and disabling it outright would break them.
  pfctl -a wings -F rules >/dev/null 2>&1 || true

  if [ -f "$CONFIG" ]; then
    set_config_flag "user"              "per_server" "false"
    set_config_flag "sandbox"           "enabled"    "false"
    set_config_flag "network_isolation" "enabled"    "false"
  fi

  # The server files belong to the per-server accounts, so they have to be
  # handed back before those accounts are deleted. Otherwise every file ends up
  # owned by a uid with no account behind it, and a later reinstall would hand
  # those uids to different servers.
  local accounts
  accounts=$(dscl . -list /Users 2>/dev/null | grep '^ptero-' || true)
  if [ -n "$accounts" ]; then
    info "Returning server files to ${OWNER} (this can take a moment)"
    [ -d "$DATA_DIR" ] && chown -R "${OWNER_UID}:${OWNER_GID}" "$DATA_DIR"
    while IFS= read -r acct; do
      [ -n "$acct" ] || continue
      dscl . -delete "/Users/${acct}" 2>/dev/null || true
      dscl . -delete "/Groups/${acct}" 2>/dev/null || true
    done <<< "$accounts"
  fi

  rm -f "$DAEMON_PLIST"

  if [ -f "$PF_BACKUP" ]; then
    info "Restoring ${PF_CONF}"
    cp "$PF_BACKUP" "$PF_CONF"
    rm -f "$PF_BACKUP"
  fi
  rm -f "$ANCHOR_FILE"

  if [ -f "$AGENT_PLIST" ]; then
    info "Starting wings again as ${OWNER}"
    launchctl bootstrap "gui/${OWNER_UID}" "$AGENT_PLIST" 2>/dev/null || true
    wait_for_wings || warn "wings did not come back up; log in as ${OWNER} and run:
    launchctl bootstrap gui/${OWNER_UID} ${AGENT_PLIST}"
  else
    warn "no LaunchAgent to fall back to; start wings yourself"
  fi

  info "Isolation is off. Restart your servers so they run as ${OWNER} again."
  exit 0
}

main "$@"
