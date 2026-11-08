# Boxes

A box is a long-lived container to develop in: for projects that are built as a Docker stack
even in development, and for Claude Code working on them. Each box runs **its own Docker
daemon**, so `docker compose up` inside it works as on any workstation, at native speed (it
is a container, not a VM), while staying isolated from the host, from whatever else runs on
it, and from the other boxes ([security.md](security.md)).

A host can have **several boxes** (e.g. one per client or project), created, listed,
entered and destroyed from your laptop. A box is the compose project `devbox-<name>` from
[`box/docker-compose.yaml`](../box/docker-compose.yaml): boxes are runtime state, not
configuration.

## Hosts

devboxen runs boxes on one or more hosts, each an Ubuntu 24.04 machine (x86_64) you reach
over SSH as root:

- **A VPS** that is always on: boxes are there whenever you are.
- **An on-demand cloud host** ([`tofu/aws-host`](../tofu/aws-host/)): an EC2 instance that
  runs only while you use it. A stopped instance costs only its disks and IP; the CLI starts
  it when you connect, and a schedule stops it every night in any case. Everything the boxes
  keep lives on a separate EBS data volume (snapshotted daily, see
  [`ansible/data-volume.yml`](../ansible/data-volume.yml)). Stopping it ends every session
  there, like a reboot.

All hosts get the same plays and run the same image; boxes, sessions and the Claude token
are per host.

## Using it

On your laptop, the `devboxen` CLI (needs `ssh`):

```bash
devboxen shell                       # into the default box (main), login shell, session "shell"
devboxen shell client2               # into the box "client2"
devboxen shell client2 work          # box "client2", another independent session "work"
devboxen claude                      # Claude Code in the default box (session "claude")
devboxen claude client2 api          # a second Claude in "client2" (session "claude-api")
devboxen list                        # boxes of all hosts: status, memory, CPUs, sessions (* = attached)

devboxen create client2              # a new box (options: --mem 4g --cpus 4)
devboxen stop client2                # stop it (frees its memory; everything stays)
devboxen start client2
devboxen destroy client2             # type the name to confirm; deletes its data

devboxen login                       # one Claude Code login for all boxes of a host
devboxen github push                 # the GitHub tokens to all boxes

# Another host: append @<host> to a box
devboxen shell @cloud                # into its default box; starts an on-demand host if it is stopped
devboxen claude main@cloud api       # box main there, session claude-api
devboxen create client2@cloud --mem 12g --cpus 4
devboxen host status                 # running / stopped, instance type, IP
devboxen host start | stop           # stop: lists the boxes and sessions, asks
devboxen costs                       # what an on-demand host costs

devboxen tui                         # all of the above as a menu
```

Box names: 1–11 characters, lowercase letters, digits and `-` (the box's bridge `dbx-<name>`
must fit Linux's 15-character interface names). The **default box** is `main` if it exists,
else the only box.

`devboxen shell` and `devboxen claude` run `ssh -o ForwardAgent=no -t root@<host> devbox -b
<box> -s <session>`: no agent goes along, whatever `~/.ssh/config` says.

**GitHub, for all boxes: fine-grained tokens, one per owner.** A box holds no SSH key and
needs no connection from your laptop: `git` and `gh` go over HTTPS with GitHub fine-grained
personal access tokens. Such a token belongs to one resource owner (a user or an org), so
there is one per owner, kept encrypted (age) in your instance repository (plaintext:
`owner=token` lines, the first the default). `devboxen github push` decrypts them on your
laptop and sends them to `devbox-admin github set` on every running host, which stores them
as `/var/lib/devbox/shared/github-tokens` (mode 0400, UID 1000), mounted read-only into every
box; read at every use, so a new set needs no restart. In a box:

- **git** asks [`git-credential-github`](../box/git-credential-github.sh) (in `/etc/gitconfig`, with
  `useHttpPath`): the token of the repository's owner, `x-access-token` as the user name. A
  repository of an owner without a token gets no credentials (public ones still clone).
- **SSH remotes** (`git@github.com:…`, `ssh://git@github.com/…`, in existing checkouts too)
  are rewritten to HTTPS (`url.https://github.com/.insteadOf`), so nothing needs changing.
- **gh** is a wrapper ([`gh-wrapper.sh`](../box/gh-wrapper.sh)) that sets `GH_TOKEN` for each call:
  the owner from `-R`/`--repo` or `GH_REPO`, else from an argument naming a repository
  (`OWNER/REPO`, a github.com URL, `repos/OWNER/…` in `gh api`), else from the current
  directory's repository, else the default token. With `GH_TOKEN` already set, gh runs as it
  is. Claude Code uses the same `git` and `gh`.

A new token, or one for another owner: create it on GitHub (Settings → Developer settings →
Fine-grained tokens; resource owner = that user or org; repositories and permissions as
narrow as the work allows, e.g. Contents, Pull requests, Issues, Workflows: read and write),
then add it to the encrypted token file (`devboxen github edit`) and `devboxen github push`.
Commits need an identity in the box: `git config --global user.name/user.email`.

**Claude Code login, once for all boxes:** `devboxen login` runs `claude setup-token`
(on your laptop if Claude Code is installed there, else in a box), which prints a long-lived
token after you log in in the browser; paste it and it is stored root-only on the host
(`/var/lib/devbox/shared/claude-oauth-token`, mode 0400, UID 1000), mounted read-only into
every box. Each box's `claude` is a small wrapper ([`claude-wrapper.sh`](../box/claude-wrapper.sh))
that exports it as `CLAUDE_CODE_OAUTH_TOKEN` and starts the real Claude Code; it is read at
every start, so a new token (or `devbox-admin token clear`) needs no restart. Only the login
is shared: settings, MCP servers, history and project memory stay **per box** (Claude keys
project data by path, so the same checkout path in two boxes must not share it). Without a
token, each box logs in on its own (`claude` prints a URL). Login shells would find the real
`claude` first (Ubuntu's `~/.profile` puts `~/.local/bin` in front of PATH), so the box's
entrypoint appends a line to `~/.profile` that puts the wrapper back in front. A box's first
`claude` start still asks for a theme and to trust the folder, but not for a login.

**Sessions survive sleep and lost connections.** Every interactive session runs in **tmux
inside the box**, named after its command (`shell`, `claude`, …) or as you name it. When the
laptop sleeps or the network goes, the session (and Claude, mid-task) keeps running on the
host. The CLI notices the dead connection within ~45 s (SSH keepalives) and
**reconnects by itself** until you press Ctrl-C; running the same task again later also
reattaches. A second terminal connection to the **same** session takes it over (the old terminal is
detached; that is what lets a reconnect reclaim a session from a dead connection). For
**another** session side by side, give it a name. The task ends when you leave: `exit`, or
detach with **Ctrl-b d** (the session keeps running). tmux's status line names host, box and
session, e.g. `[cloud:main:shell]`, so sessions on different hosts don't get mixed up. The
browser UI attaches the same sessions, without detaching anyone.

| | |
|---|---|
| Reattach | the same command again: `devboxen shell [box] [name]`, `devboxen claude [box] [name]` |
| More windows in a session | Ctrl-b c (new), Ctrl-b n / p (next / previous) |
| List boxes and sessions | `devboxen list` |
| Scroll back | mouse wheel, or Ctrl-b [ (q to leave) |

Sessions do **not** survive a rebuild or restart of the box itself.

**On a host**, [`devbox`](../host/bin/devbox) enters a box and
[`devbox-admin`](../host/bin/devbox-admin) manages them (both installed to `/usr/local/bin`
by [`ansible/host.yml`](../ansible/host.yml)); the CLI calls them over SSH:

```bash
devbox [-b BOX] [-s SESSION] [command]       # e.g. devbox -b client2 -s work, devbox claude
devbox-admin create NAME [--mem 8g] [--cpus 6]
devbox-admin list | stop NAME | start NAME | rebuild [NAME…]
devbox-admin destroy NAME --confirm NAME
devbox-admin token set < token | token status | token clear
devbox-admin github set < owner=token lines | github status | github clear
```

`devbox` runs the command in the box's tmux session of that name on a terminal (attach or
create), without one (scripts) directly. It takes no SSH agent along. The files a box reads
from the host are owned by UID 1000 (`dev` inside, through Sysbox's ID-mapped mount; on the
host, UID 1000 is the shell-less `sysbox` account the Sysbox installer added).

`devbox-admin` keeps each box's settings in `/var/lib/devbox/boxes/<name>.env`
(`DEVBOX_NAME`, `DEVBOX_MEM`, `DEVBOX_CPUS`) and runs [`box/docker-compose.yaml`](../box/docker-compose.yaml)
from the install in `/opt/devboxen` with it. The image is `DEVBOXEN_BOX_IMAGE` from
`/etc/devboxen/host.env` (a published one, pulled), or `devboxen-box:local`, built on the
host from `box/`.

**Dev servers** inside a box (e.g. a compose stack publishing `8000`): the box has an
address on its network (`docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' devbox-<name>`);
tunnel to it from your laptop: `ssh -L 8000:<that address>:8000 root@<host>`. Or open it in
the box's own browser (below), where `localhost` is the box.

## In the browser

Every box can also be used from a browser, anywhere (password-protected): the boxes of all
hosts; a box opens as VS Code (code-server, on `/home/dev`), the box's own Chrome or Claude's
screen (tabs, below), beside a terminal that attaches the box's tmux sessions, the same ones
`devboxen shell` attaches. How, and its security: [web.md](web.md).

In the box: code-server, ttyd, a small session API and the screens' bridges (see "A browser
of your own" below) run as `dev`, started and restarted by the entrypoint, on Unix sockets in
`/run/devbox-web` (the host directory
`/var/lib/devbox/web/boxes/<box>`, created by devbox-admin); logs in
`/var/log/devbox-web-*.log`. No network port. VS Code costs a few hundred MB of the box's
memory while a window is open.

## A browser for Claude and for e2e tests

Every box has **Google Chrome** (stable, from Google's apt repo; Ubuntu's Chromium is a
snap, which does not run in containers) and a **virtual screen** `:99` (1920×1080; TigerVNC's
Xvnc, an X server that can also be watched), started and kept running by the entrypoint;
`DISPLAY=:99` is set for every process. You can watch it, and take it over, in devboxen-web
(tab "Claude's screen"; below). Browsers can run headed or headless; nothing needs a GPU or a monitor. Fonts
include Noto (CJK, color emoji) so pages and screenshots render properly; `/dev/shm` is 2 GB
(Chrome needs more than Docker's 64 MB).

- **Claude's browser tools**: the [Playwright MCP server](https://github.com/microsoft/playwright-mcp)
  (`@playwright/mcp`, pinned in the `Dockerfile`, installed globally) is registered for `dev`
  as the user-scope MCP server `playwright`, driving the installed Chrome on `:99`. Claude can
  open pages, click, type, read the console and network, and take screenshots. The
  entrypoint registers it once (marker `~/.config/devbox/playwright-mcp-registered`); remove
  it with `claude mcp remove playwright` and it stays removed.
- **e2e tests run on the devbox itself**: Playwright (`npx playwright install chromium` puts
  browsers in `~/.cache`, which persists), Cypress, Puppeteer etc. find the display and
  Google Chrome (Playwright's `channel: "chrome"`).
- **e2e tests in a project's own containers** (e.g. the `mcr.microsoft.com/playwright`
  image) bring their own browsers and run headless, as anywhere.
- **Node.js** LTS (NodeSource 26.x) is installed system-wide, for the MCP server and tooling.

To see the screen yourself: devboxen-web's tab "Claude's screen", or a screenshot inside:
`import -window root /tmp/screen.png`.

**A browser of your own in the box** (devboxen-web's tab "Browser"): Google Chrome on a second
screen `:98`, with its own profile (`~/.config/devbox-browser`, persistent). It sees the box
as a browser on your laptop sees your laptop: `http://localhost:<port>` is a dev server in the
box, an app's `https://localhost` with a mkcert certificate made in the box is trusted
(mkcert puts its authority into dev's NSS store, which this Chrome reads). Closed, it starts
again. Its screen takes the size of the pane. Keys: it is a Linux Chrome (Ctrl-…); on a Mac
the page sends Cmd-C, Cmd-V, Cmd-A, Cmd-F, Cmd-L… as Ctrl-…, but Cmd-T, Cmd-W, Cmd-N stay
with your laptop's browser: use Ctrl-T, Ctrl-W. Copy (Ctrl-C/Cmd-C) lands in your computer's
clipboard; Cmd-V/Ctrl-V pastes yours. It is a picture stream (VNC): fine for an app's UI,
not for video. Chrome costs a few hundred MB of the box's memory.

How: Xvnc listens on Unix sockets only (`/run/devbox-vnc/{98,99}.sock`, dev only; no TCP
port), matchbox keeps `:98`'s windows full-screen, and [`web-vnc.js`](../box/web-vnc.js) serves
noVNC's files and bridges the browser's WebSocket to a display, on `screen.sock` and
`watch.sock` in `/run/devbox-web` (Ubuntu's websockify 0.10 does not listen on a Unix
socket). Logs: `/var/log/devbox-xvnc-{98,99}.log`, `/var/log/devbox-web-screen*.log`.


## Projects: the `.devbox` folder

A contract between the devbox and the projects in it: a project carries what it needs from
a devbox in a `.devbox/` folder **next to its compose file** (the Compose project directory;
in a monorepo, that of each compose project). Nothing outside a devbox reads it, so it
changes nothing on a laptop or in CI.

| File | When the devbox acts on it | What it does |
|---|---|---|
| `.devbox/compose.yaml` (or `.yml`) | every `docker compose` in the project | merged on top of the project's own compose files, as the last one |
| `.devbox/on-start` (executable) | every start of the box, **once trusted** | run as `dev` from the project directory: e.g. bring back data that lived in a tmpfs |

More files (a manifest, stop hooks, ports to tunnel) come when a project needs them.

### The compose overlay

**How:** `docker compose` in a box is a small wrapper,
[`compose-overlay.sh`](../box/compose-overlay.sh), installed as the Docker CLI plugin
`/usr/local/lib/docker/cli-plugins/docker-compose`, which the docker CLI finds before the
real one (`/usr/libexec/docker/cli-plugins/docker-compose`). It works out the project as
Compose does (`-f` files and `--project-directory`, else `COMPOSE_FILE` from the
environment or the project's `.env`, else `compose.yaml`/`docker-compose.yml` from the
current directory upwards plus its override file), and appends the overlay with one more
`-f`. So it applies to every caller alike: a shell, a mise task, a script, Claude, and
stacks a project starts with its own `-f` files (an e2e stack gets the overlay too). Relative
paths in the overlay are relative to the project directory, as in any extra compose file;
the overlay should only change services the project's files define. On a terminal, `up`,
`run`, `create`, `build` and `config` say `devbox: with .devbox/compose.yaml`; nothing is
printed into pipes. `DEVBOX_COMPOSE_OVERLAY=0 docker compose …` runs without it, and
`docker compose config` shows the merged result.

**Example: MariaDB in memory** (a faster test database):

```yaml
# .devbox/compose.yaml
services:
    mariadb:
        volumes: !reset []
        tmpfs:
            - /var/lib/mysql:size=8g
```

The data lives as long as the container: a box restart, a host stop (on-demand hosts: often)
or a recreated container starts MariaDB empty. Memory in a tmpfs counts against the box's
memory limit and cannot be reclaimed while the files exist; the `size` cap makes a full
tmpfs an error for MariaDB instead of the box running out of memory.

Give a tmpfs with `tmpfs:`, never as a `type: tmpfs` entry under `volumes:`: when Compose
recreates a container that had a volume at a path the image declares a `VOLUME` (as
MariaDB's `/var/lib/mysql`), it mounts that volume again and the `volumes:` tmpfs is gone,
silently (`docker compose config` still shows it). Docker never lets a volume cover a
`tmpfs:` path. Compose refuses a volume and a tmpfs at the same path, hence the `!reset`.
A stack that already ran with the volume keeps it attached under the tmpfs (unused, but
`docker volume rm` refuses it) until the container is made anew: `docker compose rm -sf
mariadb && docker compose up -d mariadb`, then remove the volume.

Measured with a PHP project's test suite (8 processes) on an EC2 `m8a.2xlarge` with a gp3
data volume: creating 8 test databases (132 migrations, then copies) took 15–18 s on the
volume and 0.8 s on tmpfs; the whole suite went from 366 s to 289 s. What waited on the disk
were the schema changes (every `CREATE TABLE` syncs its file, its directory and the DDL log).
Without a tmpfs, `--innodb-flush-method=nosync --sync-frm=0 --debug-no-sync
--innodb-log-file-buffering=ON --innodb-data-file-buffering=ON` come close and keep the data.

Tests: [`tests/compose-overlay.test.sh`](../box/tests/compose-overlay.test.sh), run inside a box
(`bash compose-overlay.test.sh`): throwaway projects for every way of naming the files.

### Start hooks

A project's executable `.devbox/on-start` runs at **every start of the box**: `devboxen start`,
a rebuild or redeploy, and a start of the host (an on-demand host: every time it is started). It runs as `dev`, from the project directory, with `DEVBOX_HOOK=on-start`,
once the box's Docker answers; the project's containers may still be coming back by their
restart policies, so a hook waits for what it needs. No terminal and no stdin (nobody is
connected at boot); git and gh work, with the GitHub tokens. A hook must be
idempotent: it also runs when nothing was lost. Hooks run one after another, each at most
30 minutes (`DEVBOX_HOOK_TIMEOUT`).

**Trust.** Unlike an overlay, which takes effect only when you run `docker compose` in the
project yourself, a hook runs on its own: a repo cloned into the box (by you, or by Claude
reading some code) would run code at the next start. So a hook runs only once trusted as it
is now, and stops running when it changes (a pull, a branch switch) until it is trusted
again. Like `direnv allow` or `mise trust`, the trust covers the hook file, not the code it
calls. The decisions are in `~/.config/devbox/trusted-hooks`.

[`devbox-hooks`](../box/devbox-hooks.sh), inside a box:

```bash
devbox-hooks status          # every project with a hook: trust, last run, result, log
devbox-hooks trust [DIR]     # trust the hook of DIR's project (default: the current one); prints it
devbox-hooks deny [DIR]      # never run it, and stop mentioning it at login
devbox-hooks untrust [DIR]   # forget the decision
devbox-hooks run [DIR]       # run it now (trusted or not: you asked), output on the terminal
devbox-hooks log [DIR]       # its log (~/.local/state/devbox/hooks/)
```

A login shell prints one line per hook that failed, timed out, still runs, or was skipped
as untrusted or changed. Projects are found under `/home/dev`, at most 6 levels deep
(`node_modules`, `vendor`, `.git` and tool caches skipped). The entrypoint starts the
runner (`devbox-hooks boot`, its own output in `/var/log/devbox-hooks.log`).

**Example**: if the stack runs and the dev database is missing, which on a tmpfs it is after
every start, the hook loads the fixtures; otherwise it changes nothing.

Tests: [`tests/devbox-hooks.test.sh`](../box/tests/devbox-hooks.test.sh), run inside a box: trust,
change, deny, failure, time limit, the lock, discovery and the login notice, in a temporary
directory of their own.

## What persists, what not

| Volume | Holds | Survives |
|---|---|---|
| `devbox-<name>_home` → `/home/dev` | checkouts, Claude Code settings + history, gh login, shell config | stop/start, rebuild, redeploy; **not** destroy |
| `devbox-<name>_docker` → `/var/lib/docker` | the box's Docker: images, containers, volumes, build cache | stop/start, rebuild, redeploy; **not** destroy |

Anything else (e.g. packages installed with `sudo apt`) is gone when the container is
recreated: put lasting tools into the [`Dockerfile`](../box/Dockerfile). `/home/dev` is seeded from
the image only when the volume is first created, so a newer Claude Code in the image does not
replace the installed one (which updates itself anyway).

**Disk:** each box's Docker writes to the host's disk, and a full disk stops everything. Its
build cache is garbage-collected at 20 GB per box and container logs are capped
([`daemon.json`](../box/daemon.json)); images and volumes are not. Check with
`docker system df` inside, clean with `docker system prune`.

Inner networks come from `10.200.0.0/16` (`docker0`: `10.199.0.1/24`), so they never
collide with the host's `172.16.0.0/12` Docker networks.

## Files (`box/`)

| File | |
|---|---|
| `docker-compose.yaml` | one box (project `devbox-${DEVBOX_NAME}`): Sysbox runtime, limits, volumes, seccomp, the shared tokens and devboxen-web mounts, bridge `dbx-<name>` |
| `Dockerfile` | Ubuntu 24.04, Docker (Ubuntu build, like the host) + compose + buildx, git, gh, tmux, Google Chrome + fonts, Xvnc + noVNC + matchbox, Node.js + Playwright MCP, code-server + ttyd, Claude Code (+ the token wrapper), GitHub over HTTPS (`/etc/gitconfig`), the `dev` user |
| `gh-wrapper.sh`, `git-credential-github.sh` | `gh` and git's credential helper in a box: the GitHub token of the repository's owner |
| `claude-wrapper.sh` | `claude` in a box: logs in with the shared token, then runs Claude Code |
| `web-attach.sh`, `web-api.js` | devboxen-web in the box: the browser terminal's tmux attach, the session list |
| `web-vnc.js` | devboxen-web in the box: noVNC's files and the WebSocket to a screen (`:98`, `:99`), on a Unix socket |
| `tmux.conf` | tmux for the persistent sessions (Esc without delay, mouse, OSC 52 clipboard) |
| `entrypoint.sh` | MPTCP off + firewall in the devbox's namespace, the screens `:99` and `:98` (Xvnc), code-server + ttyd + session API + the box's Chrome and the screens' bridges (devboxen-web), the one-time Playwright MCP registration, the start hooks (in the background), then `exec dockerd` |
| `daemon.json` | the inner Docker: address pools, log cap, build-cache GC |
| `compose-overlay.sh` | `docker compose` in a box: adds a project's `.devbox/compose.yaml` (the `.devbox` contract) |
| `tests/compose-overlay.test.sh` | its tests, run inside a box |
| `devbox-hooks.sh`, `profile-devbox-hooks.sh` | the start hooks of the `.devbox` contract (`/usr/local/bin/devbox-hooks`), and their notice in login shells |
| `tests/devbox-hooks.test.sh` | their tests, run inside a box |
| `seccomp.json`, `seccomp/generate.sh` | the seccomp profile and how it is made |
| `../host/bin/devbox`, `../host/bin/devbox-admin` | host side: enter a box; manage boxes and the shared tokens (`ansible/host.yml`) |
