#!/usr/bin/env bash
#
# `gh` in a devbox: the GitHub CLI, logged in with the token of the owner it works on, from
# the tokens all boxes share (/run/devbox-shared/github-tokens, lines owner=token, the first
# the default; set with `devboxen github push`). Fine-grained tokens are made
# per owner, so one login cannot serve every repository. The owner, first found wins:
#
#   1. -R/--repo OWNER/REPO, or GH_REPO
#   2. an argument naming a repository of an owner with a token: OWNER/REPO, a github.com
#      URL, or an API path (repos/OWNER/…, orgs/OWNER/…, users/OWNER/…)
#   3. the repository in the current directory: the remote gh resolved before
#      (gh repo set-default), else origin, else the first remote
#   4. the default (the first line)
#
# With GH_TOKEN or GITHUB_TOKEN set, or without tokens, gh runs as it is. The git that gh
# runs gets its token from git-credential-github, by the same owner rule. See docs/box.md.
set -uo pipefail

REAL=/usr/bin/gh
TOKENS=/run/devbox-shared/github-tokens

if [ -n "${GH_TOKEN:-}${GITHUB_TOKEN:-}" ] || [ ! -r "$TOKENS" ]; then
  exec "$REAL" "$@"
fi

token_of() { # OWNER -> its token, if there is one (owner names are case-insensitive)
  awk -F= -v o="$1" 'tolower($1) == tolower(o) { print $2; exit }' "$TOKENS"
}

owner_of() { # a repository reference -> its owner, or nothing
  local s="$1"
  s="${s#https://}"; s="${s#http://}"; s="${s#ssh://}"; s="${s#git@}"; s="${s#www.}"
  s="${s#github.com/}"; s="${s#github.com:}"; s="${s#api.github.com/}"; s="${s#/}"
  case "$s" in
    repos/*|orgs/*|users/*) s="${s#*/}" ;; # an API path: the owner may stand alone
    */*) ;;                                 # OWNER/REPO…
    *) return 0 ;;
  esac
  [[ "$s" =~ ^([A-Za-z0-9][A-Za-z0-9-]{0,38})(/|\?|$) ]] || return 0
  printf '%s\n' "${BASH_REMATCH[1]}"
}

owner=""
token=""

# 1. -R/--repo, GH_REPO
prev=""
for arg in "$@"; do
  case "$prev" in -R|--repo) owner="$(owner_of "$arg")" ;; esac
  case "$arg" in --repo=*) owner="$(owner_of "${arg#--repo=}")" ;; esac
  [ -n "$owner" ] && break
  prev="$arg"
done
[ -n "$owner" ] || owner="$(owner_of "${GH_REPO:-}")"
[ -z "$owner" ] || token="$(token_of "$owner")"

# 2. an argument naming a repository of an owner with a token
if [ -z "$token" ]; then
  for arg in "$@"; do
    [[ "$arg" == */* ]] && [[ "$arg" != *" "* ]] || continue
    candidate="$(owner_of "$arg")"
    [ -n "$candidate" ] || continue
    token="$(token_of "$candidate")"
    [ -n "$token" ] && break
  done
fi

# 3. the repository in the current directory
if [ -z "$token" ] && git rev-parse --git-dir >/dev/null 2>&1; then
  remote="$(git config --get-regexp '^remote\..*\.gh-resolved$' 2>/dev/null | head -n1 | cut -d. -f2)"
  [ -n "$remote" ] || remote="$(git remote 2>/dev/null | grep -qx origin && echo origin)"
  [ -n "$remote" ] || remote="$(git remote 2>/dev/null | head -n1)"
  if [ -n "$remote" ]; then
    candidate="$(owner_of "$(git remote get-url "$remote" 2>/dev/null)")"
    [ -z "$candidate" ] || token="$(token_of "$candidate")"
  fi
fi

# 4. the default
[ -n "$token" ] || token="$(grep -m1 '=' "$TOKENS" | cut -d= -f2-)"

if [ -n "$token" ]; then
  GH_TOKEN="$token" exec "$REAL" "$@"
fi
exec "$REAL" "$@"
