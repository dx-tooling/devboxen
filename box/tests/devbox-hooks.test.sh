#!/usr/bin/env bash
#
# Tests of devbox-hooks (../devbox-hooks.sh, the start hooks of the .devbox contract): run
# INSIDE a devbox, as dev, from anywhere:
#
#   bash devbox-hooks.test.sh
#
# Works in a temporary directory with its own project root, trust file and state; the box's
# real hooks and decisions are not touched. `boot` waits for the box's Docker, which runs.
set -euo pipefail

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
export DEVBOX_HOOKS_ROOT="$T/home" XDG_CONFIG_HOME="$T/config" XDG_STATE_HOME="$T/state"
H=devbox-hooks
pass=0
fail=0

check() { # check NAME EXPECTED ACTUAL
  if [ "$2" = "$3" ]; then
    pass=$((pass + 1)); echo "ok    $1"
  else
    fail=$((fail + 1)); echo "FAIL  $1: expected '$2', got '$3'"
  fi
}
contains() { # contains NAME NEEDLE HAYSTACK
  if [[ "$3" == *"$2"* ]]; then
    pass=$((pass + 1)); echo "ok    $1"
  else
    fail=$((fail + 1)); echo "FAIL  $1: '$2' not in: $3"
  fi
}

hook() { # hook DIR BODY: an executable .devbox/on-start in DIR
  mkdir -p "$1/.devbox"
  printf '#!/usr/bin/env bash\n%s\n' "$2" >"$1/.devbox/on-start"
  chmod +x "$1/.devbox/on-start"
}
last() { # last DIR: the result of DIR's last run
  "$H" status | awk -v d="$1" '$0 == d { f = 1; next } f && /last:/ { sub(/^ *last: */, ""); sub(/,.*/, ""); print; exit }'
}
trust_of() {
  "$H" status | awk -v d="$1" '$0 == d { f = 1; next } f && /trust:/ { print $2; exit }'
}

A="$T/home/git/app"
B="$T/home/git/mono/service"
mkdir -p "$T/home"

check "status without hooks" "no project under $T/home has a .devbox/on-start" "$("$H" status)"

# A hook that says where it ran, how, and what its stdin holds
hook "$A" 'echo "cwd=$PWD hook=$DEVBOX_HOOK stdin=$(cat)" >"$PWD/../app-ran"; echo hello from app'
hook "$B" 'touch "$PWD/ran"; exit 3'
mkdir -p "$T/home/git/app/node_modules/dep"
hook "$T/home/git/app/node_modules/dep" 'touch "$PWD/ran"'

contains "never run, untrusted: notice already" "not trusted: devbox-hooks trust $A" "$("$H" notice 2>&1)"
"$H" boot
check "untrusted: not run" no "$([ -e "$T/home/git/app-ran" ] && echo yes || echo no)"
check "untrusted: status" skipped-untrusted "$(last "$A")"
contains "untrusted: notice says how to trust" "not trusted: devbox-hooks trust $A" "$("$H" notice 2>&1)"
check "hooks under node_modules are not found" no "$([ -e "$T/home/git/app/node_modules/dep/ran" ] && echo yes || echo no)"

(cd "$A/.devbox" && "$H" trust >/dev/null) # from a subdirectory of the project
check "trust from a subdirectory" trusted "$(trust_of "$A")"
"$H" boot
check "trusted: ran from the project dir, DEVBOX_HOOK set, empty stdin" "cwd=$A hook=on-start stdin=" "$(cat "$T/home/git/app-ran")"
check "trusted: status" ok "$(last "$A")"
contains "trusted: output in the log" "hello from app" "$("$H" log "$A")"
notice="$("$H" notice 2>&1)"
check "trusted and ok: no notice about it" "" "$(grep -F "$A" <<<"$notice" || true)"

printf '\necho changed\n' >>"$A/.devbox/on-start"
contains "changed after a good run: notice before the next start" "changed since it was trusted" "$("$H" notice 2>&1)"
rm -f "$T/home/git/app-ran"
"$H" boot
check "changed: not run" no "$([ -e "$T/home/git/app-ran" ] && echo yes || echo no)"
check "changed: trust state" changed "$(trust_of "$A")"
contains "changed: notice" "changed since it was trusted" "$("$H" notice 2>&1)"
"$H" trust "$A" >/dev/null
"$H" boot
check "trusted again: runs" yes "$([ -e "$T/home/git/app-ran" ] && echo yes || echo no)"

"$H" trust "$B" >/dev/null
"$H" boot
check "failing hook: status" "failed(3)" "$(last "$B")"
contains "failing hook: notice" "start hook of $B: failed(3)" "$("$H" notice 2>&1)"

"$H" deny "$B" >/dev/null
rm -f "$B/ran"
"$H" boot
check "denied: not run" no "$([ -e "$B/ran" ] && echo yes || echo no)"
check "denied: status" skipped-denied "$(last "$B")"
check "denied: no notice" "" "$("$H" notice 2>&1 | grep -F "$B" || true)"
"$H" untrust "$B" >/dev/null
check "untrust: undecided again" untrusted "$(trust_of "$B")"

out="$("$H" run "$B" 2>&1 || true)"
check "run DIR: runs an untrusted hook (asked for)" yes "$([ -e "$B/ran" ] && echo yes || echo no)"
rm -f "$B/ran" "$T/home/git/app-ran"
out="$("$H" run 2>&1 || true)"
contains "run: every trusted hook, output on the terminal" "hello from app" "$out"
check "run: untrusted ones not" no "$([ -e "$B/ran" ] && echo yes || echo no)"

chmod -x "$A/.devbox/on-start"
"$H" boot
check "not executable: status" not-executable "$(last "$A")"
chmod +x "$A/.devbox/on-start"

C="$T/home/slow"
hook "$C" 'sleep 5'
"$H" trust "$C" >/dev/null
DEVBOX_HOOK_TIMEOUT=1 "$H" run "$C" >/dev/null 2>&1 || true
check "time limit: status" timeout "$(last "$C")"

hook "$C" 'sleep 3'
"$H" trust "$C" >/dev/null
"$H" run "$C" >/dev/null 2>&1 &
sleep 1
contains "running: notice" "start hook of $C is running" "$("$H" notice 2>&1)"
contains "running: a second run is refused" "running already" "$("$H" run "$C" 2>&1 || true)"
wait
check "running: the first one finished" ok "$(last "$C")"

echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
