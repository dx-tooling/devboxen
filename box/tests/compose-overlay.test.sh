#!/usr/bin/env bash
#
# Tests of the compose wrapper (../compose-overlay.sh, the .devbox contract): run INSIDE a
# devbox, as dev, from anywhere:
#
#   bash compose-overlay.test.sh
#
# Builds throwaway projects in a temporary directory. Each compose file sets the
# environment variable LAYER of the service `s`; the last file merged wins, so LAYER
# tells which files Compose read and in which order. Only `docker compose config` and one
# `run` (busybox) touch Docker.
set -euo pipefail

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
pass=0
fail=0

layer_file() { # layer_file PATH NAME: a compose file setting LAYER=NAME (and, for a base, the image)
  mkdir -p "$(dirname "$1")"
  if [ "${3:-}" = base ]; then
    printf 'services:\n  s:\n    image: busybox:1.37\n    environment:\n      LAYER: %s\n' "$2" >"$1"
  else
    printf 'services:\n  s:\n    environment:\n      LAYER: %s\n' "$2" >"$1"
  fi
}

layer() { # layer [compose args…]: the LAYER the merged configuration ends with
  docker compose "$@" config --format json 2>/dev/null | jq -r '.services.s.environment.LAYER'
}

check() { # check NAME EXPECTED ACTUAL
  if [ "$2" = "$3" ]; then
    pass=$((pass + 1)); echo "ok    $1"
  else
    fail=$((fail + 1)); echo "FAIL  $1: expected '$2', got '$3'"
  fi
}

# --- Fixtures ---------------------------------------------------------------------------------
P="$T/plain"                 # no .devbox
layer_file "$P/docker-compose.yml" base base

A="$T/app"                   # docker-compose.yml + override + .devbox, an extra file, a subdir
layer_file "$A/docker-compose.yml" base base
layer_file "$A/docker-compose.extra.yml" extra
layer_file "$A/.devbox/compose.yaml" devbox
mkdir -p "$A/src/deep"

B="$T/new-names"             # compose.yaml + compose.override.yaml + .devbox/compose.yml
layer_file "$B/compose.yaml" base base
layer_file "$B/compose.override.yaml" override
layer_file "$B/.devbox/compose.yml" devbox

C="$T/envfile"               # .env names the files
layer_file "$C/docker-compose.yml" base base
layer_file "$C/docker-compose.second.yml" second
layer_file "$C/.devbox/compose.yaml" devbox
printf 'OTHER=1\nCOMPOSE_FILE="docker-compose.yml:docker-compose.second.yml"\n' >"$C/.env"

M="$T/mono"                  # a monorepo: the compose project in a subfolder, .devbox there
layer_file "$M/service-a/docker-compose.yml" base base
layer_file "$M/service-a/.devbox/compose.yaml" devbox

# --- Cases ------------------------------------------------------------------------------------
cd "$P"; check "no .devbox: unchanged" base "$(layer)"

cd "$A"; check "project dir" devbox "$(layer)"
cd "$A/src/deep"; check "from a subdirectory" devbox "$(layer)"
cd "$T"; check "--project-directory" devbox "$(layer --project-directory "$A")"
cd "$T"; check "-f from elsewhere (relative)" devbox "$(layer -f app/docker-compose.yml)"
cd "$A"; check "-f base -f extra: overlay last" devbox "$(layer -f docker-compose.yml -f docker-compose.extra.yml)"
cd "$A"; check "-p and -f, = forms" devbox "$(layer -p other --file=docker-compose.yml)"
cd "$A"; check "DEVBOX_COMPOSE_OVERLAY=0" base "$(DEVBOX_COMPOSE_OVERLAY=0 layer)"
cd "$A"; check "-f - (stdin) passes through" base "$(docker compose -f - config --format json <"$A/docker-compose.yml" 2>/dev/null | jq -r '.services.s.environment.LAYER')"
cd "$A"; check "COMPOSE_FILE in the environment" devbox "$(COMPOSE_FILE=docker-compose.yml:docker-compose.extra.yml layer)"

layer_file "$A/docker-compose.override.yml" override
cd "$A"; check "override file read, overlay after it" devbox "$(layer)"
check "override file read (overlay off)" override "$(cd "$A" && DEVBOX_COMPOSE_OVERLAY=0 layer)"
rm "$A/docker-compose.override.yml"

cd "$B"; check "compose.yaml + compose.override.yaml + .devbox/compose.yml" devbox "$(layer)"
check "compose.override.yaml still read (overlay off)" override "$(cd "$B" && DEVBOX_COMPOSE_OVERLAY=0 layer)"

cd "$C"; check ".env COMPOSE_FILE + overlay" devbox "$(layer)"
check ".env COMPOSE_FILE still read (overlay off)" second "$(cd "$C" && DEVBOX_COMPOSE_OVERLAY=0 layer)"

cd "$M"; check "monorepo: from the repo root, no compose file there" null "$(layer 2>/dev/null || echo null)"
cd "$M/service-a"; check "monorepo: in the compose project" devbox "$(layer)"

# The project name stays what Compose makes of the directory (the overlay's -f is absolute)
cd "$A"; check "project name unchanged" app "$(docker compose config --format json | jq -r .name)"

# Arguments reach Compose as they were: spaces, quotes, an empty one
cd "$A"
out="$(docker compose run --rm -T s sh -c 'printf "[%s]" "$@"; printf "[%s]" "$LAYER"' x "a  b" "" "c'd" </dev/null 2>/dev/null)"
check "arguments unchanged, overlay applied to run" "[a  b][][c'd][devbox]" "$out"
docker compose down --remove-orphans >/dev/null 2>&1 || true

# Commands without a project
check "docker compose version" 0 "$(docker compose version >/dev/null 2>&1; echo $?)"
check "docker compose ls" 0 "$(cd "$A" && docker compose ls >/dev/null 2>&1; echo $?)"

echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
