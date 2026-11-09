# devboxen-web — the boxes in the browser

Every box of every host, from any browser (password-protected). Pick a box, and the page
splits in two: on the left **VS Code** (code-server, opened on `/home/dev`), the box's own
**browser** or **Claude's screen** (tabs), and a **terminal** on the right that is the same
thing as `devboxen shell`: the box's tmux sessions, as `dev`, with `claude`, Docker and
everything else. The boxes themselves: [box.md](box.md).

## Using it

- **Overview** (`#/`): one card per host, its boxes (memory, CPUs) and their tmux sessions
  (● = a terminal is attached somewhere). A stopped on-demand host shows as not reachable:
  start it (`devboxen host start`); the page finds it within a minute.
- **A box** (`#/<host>/<box>/<session>`, e.g. `#/cloud/main/claude`): the bar has the box
  picker, the sessions as chips (click to switch the terminal), a field for a new session
  (`+ shell`, or `+ claude`: named `claude` or `claude-<name>`, it runs Claude Code, like
  `devboxen claude`) and the layout (left | split | term; drag the divider). On a phone, one
  pane at a time, and the bar in two rows; the new-session field and buttons open from a `+`
  next to the sessions.
- **On a touch screen**, a row of keys under the terminal, as in Termux or Blink Shell: Esc,
  Tab, Shift-Tab, Ctrl and Alt (for the next key, from the row or the keyboard: Ctrl, then
  `r`), Paste, the arrows, Ctrl-C, tmux's prefix Ctrl-B and `|`, `~`, `/`, `-`, `_`, `\` and a
  backtick. Tapping it keeps the keyboard open; the page follows the part of the screen the
  keyboard leaves (iOS lays it over the page), so the prompt and the row stay visible.
  Dragging a finger up or down scrolls: the page turns it into mouse wheel steps, so tmux
  scrolls its history (copy mode; scroll back to the bottom or `q` to leave it). xterm.js
  alone ignores touch while tmux has the mouse. **Paste:** hold a finger on the terminal for
  half a second and lift it, or tap Paste in the row: the page reads the clipboard (iOS
  shows its small Paste button for that; tap it) and sends the text as one paste. A long
  press alone opens no menu there: the terminal is a canvas, and tmux has the touches. If
  the browser refuses the clipboard, a text field opens instead: paste into it the usual
  way, then Send.
- **The sessions are the same ones** as from your laptop: start Claude in the browser,
  continue in the terminal at home, or the other way round. Attaching from your laptop
  (`devboxen shell`) takes the session over and the browser tab offers to reconnect (one tap
  on the terminal); the browser detaches nobody. A terminal dropped while the tab was in the
  background (phones do that) reconnects by itself on the return to the tab; ttyd would wait
  for Enter. tmux works as usual (Ctrl-b …). **Copy:** drag with the mouse; on release tmux
  copies the selection (and clears it) and the page puts it in the browser's clipboard
  ("copied" in the corner). ttyd's terminal ignores tmux's OSC 52, so the page handles it
  (`app.js`). Shift-drag (Option in Safari) selects in the browser instead, past tmux.
  Paste: Cmd/Ctrl-V.
- **Browser** (tab): a Google Chrome running in the box, on its screen `:98`: `localhost` is
  the box's, so a dev server or an app's `https://localhost:<port>` (mkcert, made in the box)
  opens as on a laptop. Copy and paste go between it and your computer (Cmd-C/Cmd-V on a
  Mac, sent as Ctrl-C/Ctrl-V; Cmd-T/Cmd-W stay with your browser: use Ctrl-T/Ctrl-W). It is a
  picture stream (VNC through noVNC): fine for an app, not for video.
- **Claude's screen** (tab): the box's screen `:99`, where Claude's browser (Playwright MCP)
  and headed tests run, scaled to fit; "watching" by default, "controlling" passes mouse and
  keyboard (e.g. to log in somewhere for Claude).
- **VS Code** keeps its state per box in `~/.local/share/code-server` (extensions from Open
  VSX, settings). Its first look at `/home/dev` is in Restricted Mode: trust the folder once.
  Its own terminal (Ctrl-`) is a plain shell, not tmux.

**git, gh and Claude Code work from the browser alone**: GitHub goes over HTTPS with the
tokens all boxes share, Claude Code with its shared login; nothing needs your laptop.

## How it fits together

```
browser ──https──> Caddy (TLS "auto") or your reverse proxy ("proxy")
                     └─> devboxen-web front (container, on the front host)
                           │ password (htpasswd, bcrypt) + session cookie, same-origin check
                           ├─ its own boxes: /var/lib/devbox/web/boxes/<box>/{code,term,api,screen,watch}.sock
                           └─ another host: /var/lib/devbox/web/hosts/<host>.sock
                                  ══ SSH tunnel (devbox-web-tunnel@<host>) ══>
                                  devboxen-web backend there, 127.0.0.1:7700
                                    └─ its boxes: /var/lib/devbox/web/boxes/<box>/*.sock
```

| Piece | Where | What |
|---|---|---|
| code-server, ttyd, session API, screens | in each box, as `dev` ([`entrypoint.sh`](../box/entrypoint.sh)) | VS Code on `code.sock`, the terminal on `term.sock` (runs [`devbox-web-attach`](../box/web-attach.sh) `<session>`: tmux attach or create), the tmux session list on `api.sock` ([`web-api.js`](../box/web-api.js)), the box's Chrome on `screen.sock` and Claude's screen on `watch.sock` (noVNC's files and a WebSocket to the Xvnc display, [`web-vnc.js`](../box/web-vnc.js)). The box's `/run/devbox-web` is the host directory `/var/lib/devbox/web/boxes/<box>` (devbox-admin creates it). No network port, no password: only the host can reach the sockets. |
| front | the host in `devboxen_front`; [`web/compose.yaml`](../web/compose.yaml), profile `front` (+ `auto`: Caddy) | the UI ([`ui/`](../cmd/devboxen-web/ui/)), `/api/hosts`, and the proxy `/h/<host>/b/<box>/{code,term,screen,watch}/…` |
| backend | every other host, profile `backend` | the same for its own boxes, on `127.0.0.1:7700` (host network), no password |
| tunnel | front: `devbox-web-tunnel@<host>.service`; the other host: the user `devbox-web` | [`ansible/web.yml`](../ansible/web.yml) |

**What the front knows about the other hosts** is only what their backends answer through
the tunnels. It has no cloud credentials and calls no AWS API. For the overview it asks each
backend for its boxes; no answer within 4 seconds shows that host as "not reachable
(stopped?)". The front cannot tell a stopped EC2 instance from a crashed host or a network
problem, and it cannot start a host: that needs your AWS login, on your computer
(`devboxen host start`, or connecting to one of its boxes). Once the host runs again, the
tunnel reconnects by itself (it retries with growing pauses, at most a minute apart), and
the page shows its boxes again.

## TLS

- **`auto`** (default): Caddy, in front of the front, gets a Let's Encrypt certificate for
  `devboxen_web_domain`. The domain must point at the front host, and its ports 80 and 443
  must be free and reachable.
- **`proxy`**: you already run a reverse proxy (Traefik, nginx, Caddy…) on the front host.
  devboxen-web then publishes nothing; your instance supplies a compose override
  (`devboxen_web_compose_override`) that puts the front on your proxy's network, with its
  labels. The front listens on port 8080. Pass the client's address as `X-Real-Ip` (used to
  throttle failed logins). Example for Traefik:

  ```yaml
  services:
    front:
      networks: [proxy]
      labels:
        - traefik.enable=true
        - traefik.http.routers.devboxen.rule=Host(`devboxen.example.com`)
        - traefik.http.routers.devboxen.tls.certresolver=letsencrypt
        - traefik.http.services.devboxen.loadbalancer.server.port=8080
  networks:
    proxy:
      external: true
  ```

## Security

A login here is a shell in every box (and, with the box's sudo, root there), so:

- The password is checked by devboxen-web itself, not only at a proxy: other containers on a
  shared proxy network could reach the front directly. bcrypt entries only (cost 12 from
  `devboxen web passwd`); a checked password is remembered for 15 minutes as a hash of its
  header; 10 failures in 15 minutes from one address and that address gets nothing more
  until the window passes.
- Basic auth, once: a good password also gets a session cookie (`devboxen_session`: its
  expiry, HMAC-signed; 90 days, renewed after 45; `Secure`, `HttpOnly`, `SameSite=Lax`) and
  the cookie alone lets in. (Safari on iOS does not send the remembered password with every
  request — iframes, WebSockets, VS Code's service worker, parallel requests — and asked for
  it again and again.) The signing key comes from `web/session-key` (32 random bytes,
  root-only, made once by the deploy) and the password file: restarts and deploys keep
  everyone logged in, a new password logs every browser out, and so does deleting
  `session-key` (then deploy again).
- The password (`Authorization`) and the session cookie never reach a box: the proxy takes
  them out (`stripCredentials`), since what runs in a box could listen on its sockets.
- WebSockets and non-GET requests from another origin are refused (the browser would send
  the stored credentials along); ttyd and code-server check the origin too.
- Nothing of a box is on a network: the front and the backend reach the box sockets through
  a read-only mount, refuse a symlink in place of a socket, and have no Docker socket; root
  in their container keeps only `CAP_DAC_OVERRIDE` (to connect to `dev`'s sockets).
- The tunnel key is on the front only (`/etc/devbox-web/`, root-only). On the other hosts its
  user can only forward to `127.0.0.1:7700`: `restrict,port-forwarding,permitopen=…` on the
  key, and a `Match User devbox-web` block (no shell, pty, socket or remote forwarding). The
  backend listens on the loopback, which the boxes cannot reach.

## Files

| File | |
|---|---|
| `cmd/devboxen-web/main.go` | configuration, the origin check, socket dialing |
| `cmd/devboxen-web/server.go` | routes, the proxy, the host and box list |
| `cmd/devboxen-web/auth.go` | the password check (htpasswd, bcrypt), the session cookie |
| `cmd/devboxen-web/ui/` | the page (no build step, no dependencies) |
| `web/Dockerfile`, `web/compose.yaml`, `web/Caddyfile` | the image (a static binary), the profiles, HTTPS for `auto` |
| `host/systemd/devbox-web-tunnel@.service` | the tunnel unit (front) |
| `ansible/web.yml` | tunnel key, user, sshd restrictions, unit |
| `ansible/deploy.yml` | the install, the password file, the session key, starting it |

Tests: `go test ./...` (routing, prefix stripping, symlinks, remote passthrough, origin
check, password file and session cookie, host list).
