#!/bin/sh
#
# `claude` in a devbox: Claude Code, logged in with the long-lived token that all boxes share
# (set on the host with `devbox-admin token set`, mounted read-only at /run/devbox-shared).
# Without a token, or with CLAUDE_CODE_OAUTH_TOKEN already set, Claude Code behaves as usual
# (its own per-box login). Read at every start, so a new token takes effect without
# recreating the box. Claude Code updates itself in ~/.local/bin; this wrapper stays.
token_file=/run/devbox-shared/claude-oauth-token
if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && [ -s "$token_file" ] && [ -r "$token_file" ]; then
  CLAUDE_CODE_OAUTH_TOKEN="$(cat "$token_file")"
  export CLAUDE_CODE_OAUTH_TOKEN
fi
exec /home/dev/.local/bin/claude "$@"
