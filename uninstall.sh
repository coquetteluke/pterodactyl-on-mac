#!/usr/bin/env bash
#
# Uninstaller for Pterodactyl on Mac.
#
#   curl -fsSL https://raw.githubusercontent.com/coquetteluke/pterodactyl-on-mac/main/uninstall.sh | bash
#
# By default this removes the software and leaves every byte of your data
# alone: server files, worlds, and the Panel database all stay. Use --purge to
# delete those too, which it will make you confirm.
#
#   --full     also remove the Panel, its services and its nginx config
#   --purge    ALSO DELETE DATA: server files, Panel directory, database
#   --yes      skip the confirmation prompt for --purge
#
# Set WINGS_DATA_DIR / PANEL_DIR if you installed somewhere other than the
# defaults.

set -euo pipefail

PREFIX="${WINGS_PREFIX:-$HOME/.local/bin}"
DATA_DIR="${WINGS_DATA_DIR:-$HOME/pterodactyl}"
PANEL_DIR="${PANEL_DIR:-$HOME/pterodactyl-panel}"
LABEL="com.github.pterodactyl-on-mac"

info() { printf '\033[1;34m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[1;33m warning:\033[0m %s\n' "$1" >&2; }
die()  { printf '\033[1;31m error:\033[0m %s\n' "$1" >&2; exit 1; }

main() {
FULL=0
PURGE=0
ASSUME_YES=0
for arg in "$@"; do
  case "$arg" in
    --full) FULL=1 ;;
    --purge) PURGE=1 ;;
    --yes|-y) ASSUME_YES=1 ;;
    -h|--help)
      cat <<'USAGE'
Uninstaller for Pterodactyl on Mac.

  --full     also remove the Panel, its services and its nginx config
  --purge    ALSO DELETE DATA: server files, Panel directory, database
  --yes      skip the confirmation prompt for --purge

Without --purge your data is left untouched and the paths are printed so you
can remove them yourself if you want to.
USAGE
      exit 0
      ;;
    *) die "unknown option: $arg (try --help)" ;;
  esac
done

[ "$(uname -s)" = "Darwin" ] || die "this script is for macOS"

stop_servers
remove_wings
if [ "$FULL" = 1 ]; then
  remove_panel
fi

if [ "$PURGE" = 1 ]; then
  confirm_purge
  purge_data
fi

report
}

# Servers are deliberately detached from wings -- their own session, stdin on a
# FIFO -- so that they survive a wings restart. That means removing wings would
# otherwise leave a game server running with nothing supervising it, which is
# the one thing an uninstall must not do.
stop_servers() {
  local runtime="${DATA_DIR}/native" pidfile pid stopped=0
  [ -d "$runtime" ] || return 0

  for pidfile in "$runtime"/*/pid; do
    [ -f "$pidfile" ] || continue
    pid=$(awk '{print $1; exit}' "$pidfile" 2>/dev/null || true)
    [ -n "${pid:-}" ] && [ "$pid" -gt 0 ] 2>/dev/null || continue
    kill -0 "$pid" 2>/dev/null || continue

    info "Stopping server process $pid"
    # Negative pid signals the whole process group, so a shell wrapper does not
    # leave the real server behind.
    kill -TERM "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
    local i
    for i in $(seq 1 60); do
      kill -0 "$pid" 2>/dev/null || break
      sleep 1
    done
    if kill -0 "$pid" 2>/dev/null; then
      warn "server $pid did not stop gracefully; killing it"
      kill -KILL "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
    fi
    stopped=$((stopped + 1))
  done
  if [ "$stopped" -gt 0 ]; then
    info "Stopped ${stopped} server(s)"
  fi
  return 0
}

remove_wings() {
  local uid; uid=$(id -u)
  if launchctl print "gui/${uid}/${LABEL}" >/dev/null 2>&1; then
    info "Unloading the wings LaunchAgent"
    launchctl bootout "gui/${uid}/${LABEL}" 2>/dev/null || true
  fi
  rm -f "$HOME/Library/LaunchAgents/${LABEL}.plist"

  # Anything still running from a previous session.
  pkill -f "${PREFIX}/wings" 2>/dev/null || true

  if [ -f "${PREFIX}/wings" ]; then
    rm -f "${PREFIX}/wings"
    info "Removed ${PREFIX}/wings"
  fi
}

remove_panel() {
  local uid bp label
  uid=$(id -u)
  for label in com.pterodactyl.panel.queue com.pterodactyl.panel.schedule; do
    launchctl bootout "gui/${uid}/${label}" 2>/dev/null || true
    rm -f "$HOME/Library/LaunchAgents/${label}.plist"
  done
  info "Removed the Panel queue worker and scheduler"

  if command -v brew >/dev/null 2>&1; then
    bp=$(brew --prefix 2>/dev/null || true)
    if [ -n "${bp:-}" ] && [ -f "${bp}/etc/nginx/servers/pterodactyl.conf" ]; then
      rm -f "${bp}/etc/nginx/servers/pterodactyl.conf"
      brew services restart nginx >/dev/null 2>&1 || true
      info "Removed the nginx site"
    fi
    # The services are stopped but the formulae are left installed: they are
    # ordinary Homebrew packages and may well be in use by something else.
    for svc in nginx php@8.3; do
      brew services stop "$svc" >/dev/null 2>&1 || true
    done
    info "Stopped nginx and PHP (formulae left installed)"
  fi
}

confirm_purge() {
  [ "$ASSUME_YES" = 1 ] && return 0
  cat <<EOF

  --purge will permanently delete:

    ${DATA_DIR}       (server files and worlds)
    ${PANEL_DIR}      (the Panel)
    the 'panel' MariaDB database and its user

  This cannot be undone. Back up anything you care about first.

EOF
  # Read from the terminal, not stdin: stdin is the script itself when this is
  # piped into bash, and would otherwise answer the prompt for you. Testing
  # -r /dev/tty is not enough -- it passes in contexts where the open still
  # fails -- so attempt the read and treat a failure as "no confirmation".
  local answer=""
  printf '  Type DELETE to continue: '
  if ! read -r answer < /dev/tty 2>/dev/null; then
    echo
    die "no terminal to confirm on; re-run with --yes if you are certain"
  fi
  [ "$answer" = "DELETE" ] || die "aborted; nothing was deleted"
}

purge_data() {
  local bp
  if [ -d "$DATA_DIR" ]; then
    rm -rf "$DATA_DIR"
    info "Deleted ${DATA_DIR}"
  fi
  if [ -d "$PANEL_DIR" ]; then
    rm -rf "$PANEL_DIR"
    info "Deleted ${PANEL_DIR}"
  fi
  if command -v brew >/dev/null 2>&1; then
    bp=$(brew --prefix 2>/dev/null || true)
    if [ -n "${bp:-}" ] && [ -x "${bp}/bin/mysql" ]; then
      "${bp}/bin/mysql" -e "DROP DATABASE IF EXISTS panel; DROP USER IF EXISTS 'pterodactyl'@'127.0.0.1';" 2>/dev/null \
        && info "Dropped the panel database" \
        || warn "could not drop the panel database; do it by hand if you care"
    fi
  fi

  # Per-server accounts, if the isolation feature was ever enabled. Removing
  # them needs root, so only attempt it when we have it.
  if [ "$(id -u)" = "0" ]; then
    local name
    for name in $(dscl . -list /Users 2>/dev/null | grep '^ptero-' || true); do
      dscl . -delete "/Users/${name}" 2>/dev/null || true
      dscl . -delete "/Groups/${name}" 2>/dev/null || true
      info "Removed account ${name}"
    done
  elif dscl . -list /Users 2>/dev/null | grep -q '^ptero-'; then
    warn "per-server accounts (ptero-*) still exist; re-run with sudo to remove them"
  fi
}

report() {
  echo
  if [ "$PURGE" = 1 ]; then
    info "Uninstalled, and data deleted"
  else
    info "Uninstalled. Your data was left alone:"
    echo
    if [ -d "$DATA_DIR" ]; then
      echo "    ${DATA_DIR}       (server files and worlds)"
    fi
    if [ -d "$PANEL_DIR" ]; then
      echo "    ${PANEL_DIR}      (the Panel)"
    fi
    echo
    echo "  Delete those yourself, or re-run with --purge."
  fi
  if command -v brew >/dev/null 2>&1 && brew list --versions mariadb >/dev/null 2>&1; then
    echo
    echo "  MariaDB, PHP and nginx are still installed. If nothing else uses them:"
    echo "    brew services stop mariadb && brew uninstall mariadb php@8.3 nginx composer"
  fi
  echo
}

main "$@"
