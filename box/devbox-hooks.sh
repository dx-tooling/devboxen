#!/usr/bin/env bash
#
# devbox-hooks — the start hooks of the .devbox contract (see docs/box.md, "Projects: the
# .devbox folder"). A project's executable `.devbox/on-start` runs, as dev and from the
# project directory, every time the box starts (box start, rebuild, host start), once the
# box's Docker answers; e.g. to bring back data that lived in a tmpfs. Only hooks trusted
# as they are now run on their own.
#
#   devbox-hooks status          every project's hook: trust, last run, result, log
#   devbox-hooks trust [DIR]     trust the hook of DIR's project (default: the current one)
#                                as it is now; prints it. A changed hook runs no more
#                                until it is trusted again.
#   devbox-hooks deny [DIR]      never run it, and stop mentioning it at login
#   devbox-hooks untrust [DIR]   forget the decision
#   devbox-hooks run [DIR]       run DIR's hook now, trusted or not (you asked for it), with
#                                its output on the terminal; without DIR: every trusted one
#   devbox-hooks log [DIR]       the hook's log
#
# Called by the box itself: `boot` (entrypoint.sh, in the background) and `notice` (login
# shells, /etc/profile.d/devbox-hooks.sh: one line per hook that failed, was skipped as
# untrusted or changed, or still runs).
#
# Projects are found under $HOME (DEVBOX_HOOKS_ROOT), at most 6 levels deep, skipping
# node_modules, vendor, .git and dot-directories of tools. The trust file holds "<sha256 of
# the hook>  <project dir>" (or "deny  <dir>"); a hook's hash covers that file only, not
# what it calls, as with `direnv allow` or `mise trust`.
set -euo pipefail

ROOT="${DEVBOX_HOOKS_ROOT:-$HOME}"
TRUST="${XDG_CONFIG_HOME:-$HOME/.config}/devbox/trusted-hooks"
STATE="${XDG_STATE_HOME:-$HOME/.local/state}/devbox/hooks"
TIMEOUT="${DEVBOX_HOOK_TIMEOUT:-30m}"
HOOK=.devbox/on-start

die() { echo "devbox-hooks: $*" >&2; exit 1; }

# --- Projects, trust, state --------------------------------------------------------------------

all_projects() { # the project dir of every hook under ROOT, sorted
  find "$ROOT" -maxdepth 6 \
    \( -name node_modules -o -name vendor -o -name .git -o -name .cache -o -name .local \
       -o -name .npm -o -name .cargo -o -name .rustup \) -prune \
    -o -path "*/$HOOK" -type f -print 2>/dev/null |
    sed "s|/$HOOK\$||" | sort
}

project_of() { # project_of [DIR]: the project dir with a hook, from DIR upwards
  local d
  d="$(realpath -- "${1:-$PWD}")" || die "no such directory: ${1:-$PWD}"
  while :; do
    [ -f "$d/$HOOK" ] && { echo "$d"; return 0; }
    [ "$d" = / ] && die "no $HOOK in ${1:-$PWD} or above"
    d="$(dirname "$d")"
  done
}

hook_hash() { sha256sum <"$1/$HOOK" | cut -d' ' -f1; }

decision() { # decision DIR: the trust file's word for DIR (a hash, deny) or nothing
  [ -f "$TRUST" ] || return 0
  awk -v d="$1" 'substr($0, index($0, "  ") + 2) == d { w = $1 } END { if (w != "") print w }' "$TRUST"
}

trust_state() { # trust_state DIR: trusted, changed, denied or untrusted
  local w
  w="$(decision "$1")"
  if [ -z "$w" ]; then echo untrusted
  elif [ "$w" = deny ]; then echo denied
  elif [ "$w" = "$(hook_hash "$1")" ]; then echo trusted
  else echo changed
  fi
}

set_decision() { # set_decision DIR WORD (WORD empty: forget)
  mkdir -p "$(dirname "$TRUST")"
  touch "$TRUST"
  local tmp
  tmp="$(mktemp "$TRUST.XXXXXX")"
  awk -v d="$1" 'substr($0, index($0, "  ") + 2) != d' "$TRUST" >"$tmp"
  [ -z "$2" ] || printf '%s  %s\n' "$2" "$1" >>"$tmp"
  mv -f "$tmp" "$TRUST"
}

short() { # short DIR: DIR with ~ for the home
  case "$1" in
    "$HOME") printf '%s' '~' ;;
    "$HOME"/*) printf '%s/%s' '~' "${1#"$HOME"/}" ;;
    *) printf '%s' "$1" ;;
  esac
}

key() { # key DIR: the file name stem of DIR's log and status
  printf '%s-%s' "$(basename "$1")" "$(printf '%s' "$1" | sha256sum | cut -c1-10)"
}

write_status() { # write_status DIR RESULT [STARTED SECONDS]
  mkdir -p "$STATE"
  printf '%s\t%s\t%s\t%s\n' "$2" "${3:-$(date +%s)}" "${4:-}" "$1" >"$STATE/$(key "$1").status"
}

read_status() { # read_status DIR: "RESULT<TAB>STARTED<TAB>SECONDS" or nothing
  local f
  f="$STATE/$(key "$1").status"
  [ -f "$f" ] && cut -f1-3 "$f" || true
}

# --- Running a hook -----------------------------------------------------------------------------

run_hook() { # run_hook DIR MODE (boot: log only; manual: also on the terminal)
  local dir="$1" mode="$2" log lock start rc result secs
  mkdir -p "$STATE"
  log="$STATE/$(key "$dir").log"
  lock="$STATE/$(key "$dir").lock"
  [ -x "$dir/$HOOK" ] || { write_status "$dir" not-executable; echo "devbox-hooks: $dir/$HOOK is not executable (chmod +x)" >&2; return 1; }

  exec 9>"$lock"
  if ! flock -n 9; then
    echo "devbox-hooks: the hook of $dir is running already (devbox-hooks log $dir)" >&2
    exec 9>&-
    return 1
  fi
  # A log keeps its last 200 kB
  if [ -f "$log" ] && [ "$(stat -c %s "$log")" -gt 1000000 ]; then
    tail -c 200000 "$log" >"$log.tmp" && mv -f "$log.tmp" "$log"
  fi
  start="$(date +%s)"
  write_status "$dir" running "$start"
  echo "=== $(date -Is) on-start ($mode), $(trust_state "$dir")" >>"$log"

  set +e
  if [ "$mode" = manual ]; then
    (cd "$dir" && DEVBOX_HOOK=on-start timeout "$TIMEOUT" "./$HOOK") </dev/null 2>&1 | tee -a "$log"
    rc="${PIPESTATUS[0]}"
  else
    (cd "$dir" && DEVBOX_HOOK=on-start timeout "$TIMEOUT" "./$HOOK") </dev/null >>"$log" 2>&1
    rc=$?
  fi
  set -e

  secs=$(($(date +%s) - start))
  case "$rc" in
    0) result=ok ;;
    124) result=timeout ;;
    *) result="failed($rc)" ;;
  esac
  write_status "$dir" "$result" "$start" "$secs"
  echo "=== $(date -Is) $result after ${secs}s" >>"$log"
  exec 9>&-
  [ "$rc" -eq 0 ]
}

# --- Commands -------------------------------------------------------------------------------------

cmd_boot() {
  # The hooks call docker compose: wait for the box's Docker (dockerd starts right after
  # this is launched), at most two minutes.
  local i dir
  for i in $(seq 120); do
    docker info >/dev/null 2>&1 && break
    sleep 1
  done
  [ "$i" -lt 120 ] || { echo "devbox-hooks: Docker did not answer within two minutes; no hooks run" >&2; return 1; }
  while IFS= read -r dir; do
    [ -n "$dir" ] || continue
    case "$(trust_state "$dir")" in
      trusted) run_hook "$dir" boot || true ;;
      changed) write_status "$dir" skipped-changed ;;
      denied) write_status "$dir" skipped-denied ;;
      untrusted) write_status "$dir" skipped-untrusted ;;
    esac
  done < <(all_projects)
}

cmd_status() {
  local dir st started secs line=0
  while IFS= read -r dir; do
    [ -n "$dir" ] || continue
    line=1
    IFS=$'\t' read -r st started secs <<<"$(read_status "$dir")" || true
    printf '%s\n' "$(short "$dir")"
    printf '    trust:    %s\n' "$(trust_state "$dir")"
    if [ -n "${st:-}" ]; then
      printf '    last:     %s, %s%s\n' "$st" "$(date -d "@$started" '+%Y-%m-%d %H:%M')" "${secs:+, ${secs}s}"
      printf '    log:      %s\n' "$STATE/$(key "$dir").log"
    else
      printf '    last:     never\n'
    fi
  done < <(all_projects)
  [ "$line" -eq 1 ] || echo "no project under $(short "$ROOT") has a $HOOK"
}

cmd_notice() {
  local dir st started secs sh
  while IFS= read -r dir; do
    [ -n "$dir" ] || continue
    IFS=$'\t' read -r st started secs <<<"$(read_status "$dir")" || true
    sh="$(short "$dir")"
    # Never run, or fine last time: what the next start would do (a repo cloned since, a
    # hook changed by a pull)
    if [ "${st:-ok}" = ok ]; then
      case "$(trust_state "$dir")" in
        untrusted) st=skipped-untrusted ;;
        changed) st=skipped-changed ;;
      esac
    fi
    case "${st:-}" in
      ok | skipped-denied | "") ;;
      running) echo "devbox: the start hook of $sh is running (devbox-hooks log $sh)" ;;
      skipped-untrusted) echo "devbox: $sh has a start hook, not trusted: devbox-hooks trust $sh (or deny)" ;;
      skipped-changed) echo "devbox: the start hook of $sh changed since it was trusted, so it does not run: devbox-hooks trust $sh" ;;
      *) echo "devbox: the start hook of $sh: $st (devbox-hooks log $sh; again: devbox-hooks run $sh)" ;;
    esac
  done < <(all_projects) >&2
}

cmd="${1:-}"; shift || true
case "$cmd" in
  boot) cmd_boot ;;
  status | "") cmd_status ;;
  notice) cmd_notice ;;
  trust)
    dir="$(project_of "${1:-}")"
    echo "--- $dir/$HOOK"
    cat "$dir/$HOOK"
    echo "---"
    [ -x "$dir/$HOOK" ] || echo "devbox-hooks: note: it is not executable (chmod +x), so it cannot run" >&2
    set_decision "$dir" "$(hook_hash "$dir")"
    echo "trusted: it runs at every start of this box while it stays as it is"
    ;;
  deny) dir="$(project_of "${1:-}")"; set_decision "$dir" deny; echo "denied: $dir" ;;
  untrust) dir="$(project_of "${1:-}")"; set_decision "$dir" ""; echo "forgotten: $dir" ;;
  run)
    if [ -n "${1:-}" ]; then
      run_hook "$(project_of "$1")" manual
    else
      rc=0
      while IFS= read -r dir; do
        [ -n "$dir" ] && [ "$(trust_state "$dir")" = trusted ] || continue
        echo "==> $(short "$dir")"
        run_hook "$dir" manual || rc=1
      done < <(all_projects)
      exit "$rc"
    fi
    ;;
  log) dir="$(project_of "${1:-}")"; f="$STATE/$(key "$dir").log"; [ -f "$f" ] && cat "$f" || echo "no log yet: $f" ;;
  *) sed -n '3,22p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2 ;;
esac
