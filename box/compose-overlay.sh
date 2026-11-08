#!/usr/bin/env bash
#
# `docker compose` in a devbox, with the project's devbox overlay: the .devbox contract
# (see docs/box.md, "Projects: the .devbox folder").
#
# A Compose project that has `.devbox/compose.yaml` (or .yml) next to its compose file gets
# that file merged on top of its own, as the last one, for every `docker compose` call in
# the box: from a shell, a mise task, a script or Claude. Projects carry what they need
# only in the devbox there (e.g. MariaDB without fsync) without anything changing on a laptop
# or in CI, where nothing reads .devbox/.
#
# Installed as the Docker CLI plugin /usr/local/lib/docker/cli-plugins/docker-compose,
# which the docker CLI finds before the real one (Ubuntu's docker-compose-v2 package, in
# /usr/libexec/docker/cli-plugins). Passes everything through unchanged when there is no
# overlay, when DEVBOX_COMPOSE_OVERLAY=0, or when it cannot tell the project (`-f -`).
#
# Which project: like Compose itself. The files given with -f (the project directory is
# --project-directory, else the first file's directory); else COMPOSE_FILE (environment,
# then the project's .env); else compose.yaml / docker-compose.yml found from the current
# directory upwards, plus its override file. The overlay goes after all of them, so it
# wins; relative paths in it are relative to the project directory, as in any extra
# compose file.
set -euo pipefail

REAL=/usr/libexec/docker/cli-plugins/docker-compose

# Called by the docker CLI as `docker-compose compose …`; anything else (the CLI's plugin
# metadata query, a direct call) is not ours to change.
if [ "${1:-}" != compose ] || [ "${DEVBOX_COMPOSE_OVERLAY:-1}" = 0 ]; then
  exec "$REAL" "$@"
fi
shift
args=("$@")
real() { exec "$REAL" compose "$@"; } # as the docker CLI calls it

# --- Compose's own flags, up to the subcommand ------------------------------------------------
files=()
projdir=""
sub=-1
i=0
while [ "$i" -lt "${#args[@]}" ]; do
  a="${args[$i]}"
  case "$a" in
    -f | --file) files+=("${args[$((i + 1))]:-}"); i=$((i + 2)) ;;
    --file=*) files+=("${a#--file=}"); i=$((i + 1)) ;;
    -f?*) files+=("${a#-f}"); i=$((i + 1)) ;;
    --project-directory) projdir="${args[$((i + 1))]:-}"; i=$((i + 2)) ;;
    --project-directory=*) projdir="${a#--project-directory=}"; i=$((i + 1)) ;;
    # Flags that take a value as the next argument
    -p | --project-name | --env-file | --profile | --parallel | --ansi | --progress) i=$((i + 2)) ;;
    -*) i=$((i + 1)) ;; # --flag=value, -pNAME, and the boolean ones (--dry-run, -h, -v, …)
    *) sub="$i"; break ;;
  esac
done

# No subcommand (`docker compose --help`), or one that reads no project
if [ "$sub" -lt 0 ]; then real "${args[@]}"; fi
case "${args[$sub]}" in
  version | help) real "${args[@]}" ;;
esac

abs() { # abs PATH [BASE]: absolute path, without needing it to exist
  case "$1" in
    /*) realpath -m -- "$1" ;;
    *) realpath -m -- "${2:-$PWD}/$1" ;;
  esac
}

overlay_in() { # overlay_in DIR: prints the overlay file of the project in DIR, if any
  local f
  for f in "$1/.devbox/compose.yaml" "$1/.devbox/compose.yml"; do
    [ -f "$f" ] && { echo "$f"; return 0; }
  done
  return 1
}

sep="${COMPOSE_PATH_SEPARATOR:-:}"
mode="" # how the overlay is added: insert (-f before the subcommand) or env (COMPOSE_FILE)
insert=()

if [ ${#files[@]} -gt 0 ]; then
  for f in "${files[@]}"; do [ "$f" = - ] && real "${args[@]}"; done
  if [ -n "$projdir" ]; then dir="$(abs "$projdir")"; else dir="$(dirname "$(abs "${files[0]}")")"; fi
  overlay="$(overlay_in "$dir")" || real "${args[@]}"
  mode="insert"
  insert=(-f "$overlay")
elif [ -n "${COMPOSE_FILE:-}" ]; then
  base="${projdir:+$(abs "$projdir")}"
  first="${COMPOSE_FILE%%"$sep"*}"
  if [ -n "$base" ]; then dir="$base"; else dir="$(dirname "$(abs "$first")")"; fi
  overlay="$(overlay_in "$dir")" || real "${args[@]}"
  mode="env"
else
  # Compose's own lookup: the directory with a compose file, from the project directory or
  # the current one upwards
  if [ -n "$projdir" ]; then start="$(abs "$projdir")"; else start="$PWD"; fi
  dir="$start"
  found=""
  while :; do
    for name in compose.yaml compose.yml docker-compose.yaml docker-compose.yml; do
      if [ -f "$dir/$name" ]; then found="$dir/$name"; break 2; fi
    done
    [ "$dir" = / ] && break
    dir="$(dirname "$dir")"
  done
  [ -n "$found" ] || real "${args[@]}"
  overlay="$(overlay_in "$dir")" || real "${args[@]}"

  # The project's .env may name the files (COMPOSE_FILE=a.yml:b.yml); then those
  envfile_value=""
  if [ -f "$dir/.env" ]; then
    envfile_value="$(sed -n 's/^[[:space:]]*\(export[[:space:]]\{1,\}\)\{0,1\}COMPOSE_FILE=//p' "$dir/.env" | tail -n 1)"
    envfile_value="${envfile_value%\"}"; envfile_value="${envfile_value#\"}"
    envfile_value="${envfile_value%\'}"; envfile_value="${envfile_value#\'}"
  fi
  if [ -n "$envfile_value" ]; then
    IFS="$sep" read -r -a listed <<<"$envfile_value"
    for f in "${listed[@]}"; do [ -n "$f" ] && insert+=(-f "$(abs "$f" "$dir")"); done
  else
    insert+=(-f "$found")
    stem="${found%.*}"
    for o in "$stem.override.yaml" "$stem.override.yml"; do
      if [ -f "$o" ]; then insert+=(-f "$o"); break; fi
    done
  fi
  insert+=(-f "$overlay")
  mode="insert"
fi

# Say so where a person reads it (never into a pipe or a log someone parses), and only
# where the configuration makes containers.
case "${args[$sub]}" in
  up | run | create | config | build)
    [ -t 2 ] && echo "devbox: with ${overlay#"$dir"/} (DEVBOX_COMPOSE_OVERLAY=0 without)" >&2
    ;;
esac

if [ "$mode" = env ]; then
  export COMPOSE_FILE="${COMPOSE_FILE}${sep}${overlay}"
  real "${args[@]}"
fi
real "${args[@]:0:$sub}" "${insert[@]}" "${args[@]:$sub}"
