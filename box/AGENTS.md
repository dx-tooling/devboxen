# You are working in a devbox

This machine is a **devbox** (https://github.com/dx-tooling/devboxen): an isolated Linux
container on a server, with its own Docker daemon, used as a development environment. The
person you work for is not sitting at it: they reach it over SSH or through a web page
(VS Code, terminals, a browser), often from a phone. Keep that in mind whenever you would
tell them to "open http://localhost:…".

This file is maintained by the devbox image (`/etc/devbox/AGENTS.md`); project instructions
(the repository's own `AGENTS.md` / `CLAUDE.md`) take precedence over it.

## Showing a web app to the person

`localhost` here is not their localhost. To let them open an app that runs in this box
(a dev server, a compose stack's published port), expose its port:

```bash
devbox-expose 8080          # prints https://8080--<box>--<host>.<domain>/ ; give them that URL
devbox-expose 8443 --tls    # the app itself speaks HTTPS (e.g. a mkcert certificate)
devbox-expose               # list what is exposed, and whether something listens
devbox-expose off 8080      # stop exposing it
```

The address is password-protected (the person's devboxen login) and works from any device.
An exposed port stays exposed until `devbox-expose off`; it answers with an error page while
nothing listens. The app is reached with its public host name: frameworks that check the
`Host` header or build absolute URLs may need it allowed (e.g. Vite's
`server.allowedHosts`, Symfony's `TRUSTED_HOSTS`/`TRUSTED_PROXIES`; the proxy sends
`X-Forwarded-Proto: https` and `X-Forwarded-Host`). Expose a port when the person wants to
see or try the app; say which URL it is.

## What works here

- **Docker and Docker Compose**, as on a workstation (`docker compose up`). The daemon is
  the box's own; nothing you start is reachable from the internet except through
  `devbox-expose`.
- **A real browser**: Google Chrome on the virtual screen `:99` (`DISPLAY` is set), which the
  person can watch ("Claude's screen" in the web page). Claude Code has the Playwright MCP
  server (`playwright`) for it. Headed and headless e2e tests run here.
- **git and gh over HTTPS** with fine-grained GitHub tokens, chosen per repository owner.
  There is no SSH key in this box and none is needed: `git@github.com:` remotes are rewritten
  to HTTPS. A repository of an owner without a token cannot be pushed to; say so instead of
  trying workarounds.
- **sudo** (passwordless). Root in the box is not root on the server.
- **mkcert** certificates for `localhost`, if a project makes them, are trusted by the box's
  browsers.

## What to know

- **Persistent:** `/home/dev` (checkouts, tool caches, settings) and the box's Docker data
  (images, volumes). **Not persistent:** anything else, e.g. packages installed with
  `sudo apt-get`: they are gone when the box is rebuilt. Prefer project-local tools (mise,
  npm, composer, containers) over system packages.
- **No access to the server or other boxes**, and none to the person's computer. Do not try
  to reach them.
- **Shared resources:** the box has a memory and CPU limit (`nproc`, `free -h`); a full disk
  stops it. Clean up Docker leftovers you created (`docker system df`).
- **Projects may carry a `.devbox/` folder** next to their compose file: `.devbox/compose.yaml`
  is merged into every `docker compose` call in that project (by a wrapper; see
  `docker compose config`), and `.devbox/on-start` runs at every box start once the person
  trusted it (`devbox-hooks status`). Do not trust hooks yourself (`devbox-hooks trust`):
  that is the person's decision.
- Terminal sessions run in tmux and survive disconnects; long-running processes keep going
  when the person closes the page.
