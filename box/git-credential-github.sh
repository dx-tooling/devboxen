#!/usr/bin/env bash
#
# git's credential helper for github.com in a devbox (set in /etc/gitconfig, with
# useHttpPath so git passes the repository's path): answers with the GitHub token of the
# repository's owner, from the tokens all boxes share (/run/devbox-shared/github-tokens,
# lines owner=token; set with `devboxen github push`). An owner without a token
# gets no answer, and git fails as without a login. See docs/box.md, "GitHub".
set -euo pipefail

TOKENS=/run/devbox-shared/github-tokens

[ "${1:-}" = get ] || exit 0 # store, erase: the tokens are managed on the host

host="" path=""
while IFS= read -r line && [ -n "$line" ]; do
  case "$line" in
    host=*) host="${line#host=}" ;;
    path=*) path="${line#path=}" ;;
  esac
done
[ "$host" = github.com ] || exit 0
owner="${path%%/*}"
[ -n "$owner" ] && [ -r "$TOKENS" ] || exit 0

# GitHub owner names are case-insensitive.
token="$(awk -F= -v o="$owner" 'tolower($1) == tolower(o) { print $2; exit }' "$TOKENS")"
[ -n "$token" ] || exit 0
printf 'username=x-access-token\npassword=%s\n' "$token"
