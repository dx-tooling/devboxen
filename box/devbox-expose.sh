#!/usr/bin/env bash
#
# devbox-expose: show a port of this box in the browser, password-protected, at
# https://<port>--<box>--<host>.<devboxen web domain> (docs/box.md, "Showing an app in the
# browser"). For a dev server, a compose stack's published port, anything speaking HTTP.
#
#   devbox-expose PORT [--tls]   expose PORT (--tls: the app itself speaks HTTPS); prints the URL
#   devbox-expose off PORT       stop exposing it
#   devbox-expose url PORT       print the URL only
#   devbox-expose [list]         the exposed ports, their URLs, and whether something listens
#
# It only writes the registry (~/.config/devbox/exposed); web-expose.js, started by the
# box's entrypoint, keeps a socket for each port that the host's devboxen-web reaches.
# Exposed ports stay exposed (across box restarts) until `devbox-expose off`.
set -euo pipefail

registry=/home/dev/.config/devbox/exposed
domain_file=/run/devbox-shared/web-domain

die() { echo "devbox-expose: $*" >&2; exit 1; }

usage() {
  sed -n '7,10s/^# \{0,1\}//p' "$0"
  exit "${1:-0}"
}

valid_port() {
  [[ "$1" =~ ^[1-9][0-9]{0,4}$ ]] && (( $1 <= 65535 ))
}

url() { # PORT
  local domain host box
  domain="$(cat "$domain_file" 2>/dev/null || true)"
  host="$(cat /etc/devbox-host 2>/dev/null || true)"
  box="$(cat /proc/sys/kernel/hostname)"
  if [[ -z "$domain" ]]; then
    echo "(no devboxen web domain on this host yet: deploy devboxen again)"
  elif [[ "$host" == *--* || "$box" == *--* || -z "$host" ]]; then
    echo "(box or host name contains '--': cannot be shown at its own address)"
  else
    echo "https://$1--$box--$host.$domain/"
  fi
}

listening() { # PORT
  ss -Hltn "sport = :$1" 2>/dev/null | grep -q .
}

entries() { # prints "PORT FLAGS" per exposed port
  [[ -f "$registry" ]] || return 0
  sed -e 's/#.*//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' "$registry" | grep -E '^[0-9]+' || true
}

write_without() { # PORT [NEW_LINE]: the registry without PORT (plus NEW_LINE)
  local tmp
  mkdir -p "${registry%/*}"
  tmp="$(mktemp "${registry}.XXXXXX")"
  { entries | awk -v p="$1" '$1 != p'; [[ -z "${2:-}" ]] || echo "$2"; } >"$tmp"
  mv "$tmp" "$registry"
}

list() {
  local found=0 port flags state
  while read -r port flags; do
    found=1
    if listening "$port"; then state="listening"; else state="nothing listens yet"; fi
    printf '%-6s %-4s %-19s %s\n' "$port" "${flags:--}" "$state" "$(url "$port")"
  done < <(entries)
  if (( !found )); then
    echo "No ports exposed. Expose one: devbox-expose PORT"
  fi
}

if [[ "$(id -u)" -eq 0 && -z "${DEVBOX_EXPOSE_AS_ROOT:-}" ]]; then
  # The registry belongs to dev (web-expose.js runs as dev).
  exec setpriv --reuid=dev --regid=dev --init-groups env HOME=/home/dev "$0" "$@"
fi

case "${1:-list}" in
  -h|--help|help) usage ;;
  list|ls) list ;;
  off|rm|remove)
    [[ $# -eq 2 ]] && valid_port "$2" || usage 2
    write_without "$2"
    echo "Port $2 is no longer exposed."
    ;;
  url)
    [[ $# -eq 2 ]] && valid_port "$2" || usage 2
    url "$2"
    ;;
  *)
    valid_port "$1" || usage 2
    port="$1" flags=""
    shift
    for a in "$@"; do
      case "$a" in
        --tls) flags=" tls" ;;
        *) usage 2 ;;
      esac
    done
    write_without "$port" "$port$flags"
    echo "Port $port${flags:+ (HTTPS inside)} is exposed (password-protected, like devboxen-web):"
    echo "  $(url "$port")"
    listening "$port" || echo "Nothing listens on port $port yet; the address answers once something does."
    ;;
esac
