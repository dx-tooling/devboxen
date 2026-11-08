#!/bin/bash
#
# devbox-web-attach SESSION: the browser terminal's command (ttyd runs it for every browser
# tab; devboxen-web, ../cmd/devboxen-web/). Like the host's `devbox` command: attach the box's tmux
# session of that name, or create it. A new session runs Claude Code if it is named claude
# or claude-<name> (as `devboxen claude` names them), else a login shell.
#
# Unlike `devbox`, it detaches no other client: a browser tab and a laptop terminal can share a session
# (the window takes the size of the client used last). `devbox` from a laptop does detach the
# browser; its tab then offers to reconnect.
#
# The session name comes from the URL (ttyd --url-arg), so it is checked strictly.
set -euo pipefail

session="${1:-shell}"
if ! [[ "$session" =~ ^[A-Za-z0-9_-]{1,32}$ ]]; then
  echo "devbox-web: session names may only use letters, digits, _ and - (at most 32)" >&2
  sleep 5
  exit 2
fi

cmd=(bash -l)
case "$session" in claude|claude-*) cmd=(claude) ;; esac

host_name="$(cat /etc/devbox-host 2>/dev/null || echo devbox)"
box="$(hostname)"
cd /home/dev
exec tmux new-session -A -s "$session" "${cmd[@]}" \
  \; set-option -g status-left "[$host_name:$box:#S] " \
  \; set-option -g status-left-length 50
