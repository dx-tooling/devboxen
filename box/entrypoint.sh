#!/usr/bin/env bash
#
# devbox PID 1 (under docker-init): harden the devbox's network namespace, then run the
# inner Docker daemon in the foreground. If dockerd dies, the container stops and Docker's
# restart policy brings it back.
set -euo pipefail

# CVE-2026-98432 (see docs/box.md, "Security"). The host's firewall
# and its net.mptcp.enabled=0 do not reach the devbox's network namespace, so both are
# applied here as well. Best effort: a failure is logged, not fatal.
sysctl -qw net.mptcp.enabled=0 || echo "devbox: could not disable MPTCP" >&2
nft -f /etc/devbox-firewall.nft || echo "devbox: could not load the CVE-2026-98432 firewall" >&2

# Supervised processes as dev: SUPERVISE NAME SOCKET COMMAND… runs the command as dev and
# starts it again 2 s after it ends; SOCKET (or "") is removed before each start. Logs in
# /var/log/devbox-<NAME>.log.
supervise() {
  local name="$1" socket="$2"; shift 2
  (
    while :; do
      [ -z "$socket" ] || rm -f "$socket"
      (cd /home/dev && exec setpriv --reuid=dev --regid=dev --init-groups \
        env HOME=/home/dev USER=dev LOGNAME=dev SHELL=/bin/bash \
        PATH="/usr/local/lib/devbox/bin:/home/dev/.local/bin:$PATH" COLORTERM=truecolor \
        "$@") >"/var/log/devbox-$name.log" 2>&1 || true
      sleep 2
    done
  ) &
}

# The virtual screens: TigerVNC's Xvnc, an X server that can also be watched over VNC, on a
# Unix socket in /run/devbox-vnc (dev only; no TCP port). :99 is everyone's (DISPLAY is set
# image-wide): Claude's browser, e2e tests; 1920x1080 is the largest a browser window gets,
# pages see their own viewport size, and a viewer cannot change it. :98 is the browser in
# devbox-web (below); its size follows the viewer's pane. A viewer gets the clipboard when
# something is copied, not every selection (SendPrimary=0).
vnc=/run/devbox-vnc
install -d -m 0700 -o dev -g dev "$vnc"
xvnc() { # DISPLAY_NUMBER GEOMETRY [Xvnc options…]
  local n="$1" geometry="$2"; shift 2
  supervise "xvnc-$n" "$vnc/$n.sock" sh -c 'rm -f "/tmp/.X$1-lock" "/tmp/.X11-unix/X$1"; n="$1"; shift; exec Xvnc ":$n" "$@"' \
    xvnc "$n" -geometry "$geometry" -depth 24 -SecurityTypes None -rfbport -1 \
    -rfbunixpath "$vnc/$n.sock" -rfbunixmode 0600 -nolisten tcp -ac -SendPrimary=0 "$@"
}
xvnc 99 1920x1080 -AcceptSetDesktopSize=0

# The box in the browser (devboxen-web, docs/web.md), each as dev on a Unix socket
# in /run/devbox-web, the host directory that only the host's devbox-web reads. No passwords
# here: nothing else can reach the sockets. A box created without that mount gets none.
#   code    VS Code (code-server)
#   term    the terminal (ttyd, running devbox-web-attach: the box's tmux sessions)
#   api     the session API
#   screen  the browser: Google Chrome on :98 (its own profile in ~/.config/devbox-browser;
#           closed, it starts again), shown through noVNC (web-vnc.js)
#   watch   :99, Claude's screen, the same way
#   port-N  the ports devbox-expose shows at their own address (web-expose.js)
web=/run/devbox-web
if mountpoint -q "$web"; then
  supervise web-code "$web/code.sock" code-server --auth none \
    --socket "$web/code.sock" --socket-mode 600 \
    --disable-telemetry --disable-update-check --disable-getting-started-override /home/dev
  # -W: the terminal is writable; -a: the URL names the session (?arg=NAME, checked by
  # devbox-web-attach); -O: WebSockets only from the page's own origin.
  supervise web-term "$web/term.sock" ttyd -i "$web/term.sock" -W -a -O \
    -t fontSize=14 -t disableLeaveAlert=true -t disableResizeOverlay=true \
    -t 'theme={"background":"#181818"}' \
    devbox-web-attach
  supervise web-api "$web/api.sock" node /usr/local/lib/devbox/web-api.js "$web/api.sock"
  supervise web-expose "" node /usr/local/lib/devbox/web-expose.js "$web" /home/dev/.config/devbox/exposed

  xvnc 98 1600x1000
  supervise web-screen-wm "" env DISPLAY=:98 matchbox-window-manager -use_titlebar no
  supervise web-screen-chrome "" env DISPLAY=:98 google-chrome \
    --user-data-dir=/home/dev/.config/devbox-browser --no-first-run --no-default-browser-check \
    --password-store=basic --start-maximized
  supervise web-screen "$web/screen.sock" node /usr/local/lib/devbox/web-vnc.js "$web/screen.sock" "$vnc/98.sock"
  supervise web-watch "$web/watch.sock" node /usr/local/lib/devbox/web-vnc.js "$web/watch.sock" "$vnc/99.sock"
else
  echo "devbox: no $web mount; no browser access (devbox-web) for this box" >&2
fi

# Claude Code's browser tools: register the Playwright MCP server for dev once (a marker in
# the persistent home keeps a later removal by the user from being undone). Uses the
# installed Google Chrome, headed on :99.
marker=/home/dev/.config/devbox/playwright-mcp-registered
if [ ! -e "$marker" ]; then
  if su - dev -c '~/.local/bin/claude mcp add --scope user playwright -- playwright-mcp --browser chrome' \
       >/var/log/devbox-mcp.log 2>&1; then
    su dev -s /bin/sh -c "mkdir -p '${marker%/*}' && touch '$marker'"
  else
    echo "devbox: could not register the Playwright MCP server (see /var/log/devbox-mcp.log)" >&2
  fi
fi

# Ubuntu's ~/.profile puts ~/.local/bin (Claude Code's install dir) in front of PATH in every
# login shell, which would bypass the `claude` wrapper that logs in with the shared token
# (claude-wrapper.sh). Put the wrapper's dir back in front, at the end of ~/.profile. Done
# here rather than in the image so homes that already exist get it too; once per home.
profile=/home/dev/.profile
if [ -f "$profile" ] && ! grep -qF '/usr/local/lib/devbox/bin' "$profile"; then
  printf '\n# devbox: the claude wrapper (shared login token) before ~/.local/bin\nPATH="/usr/local/lib/devbox/bin:$PATH"\n' \
    >>"$profile"
fi

# What a coding agent should know about the box (/etc/devbox/AGENTS.md, from the image, so it
# stays current). ~/AGENTS.md: read by agents working anywhere under /home/dev, and by Claude
# Code in projects without a CLAUDE.md of their own. ~/.claude/CLAUDE.md (Claude's user
# file, read in every project) imports it. Either is only created when missing; an existing
# ~/.claude/CLAUDE.md gets the import once (a marker keeps a removal by the user removed).
if [ ! -e /home/dev/AGENTS.md ] && [ ! -L /home/dev/AGENTS.md ]; then
  su dev -s /bin/sh -c 'ln -s /etc/devbox/AGENTS.md /home/dev/AGENTS.md'
fi
marker=/home/dev/.config/devbox/agents-md-imported
if [ ! -e "$marker" ]; then
  su dev -s /bin/sh -c '
    set -e
    f=/home/dev/.claude/CLAUDE.md
    mkdir -p /home/dev/.claude /home/dev/.config/devbox
    if [ ! -e "$f" ] && [ ! -L "$f" ]; then
      printf "%s\n" "@/etc/devbox/AGENTS.md" >"$f"
    elif ! grep -qF "@/etc/devbox/AGENTS.md" "$f"; then
      printf "\n%s\n" "@/etc/devbox/AGENTS.md" >>"$f"
    fi
    touch /home/dev/.config/devbox/agents-md-imported
  ' || echo "devbox: could not add the devbox notes to ~/.claude/CLAUDE.md" >&2
fi

# The projects' start hooks (.devbox/on-start, see docs/box.md): as dev, in the background,
# once the dockerd started below answers. Every start of the box runs them: box start,
# rebuild, host start. PATH as a login shell of dev has it, plus mise's shims.
su dev -s /bin/bash -c \
  'PATH="$HOME/.local/share/mise/shims:/usr/local/lib/devbox/bin:$HOME/.local/bin:$PATH" exec devbox-hooks boot' \
  >/var/log/devbox-hooks.log 2>&1 &

exec dockerd
