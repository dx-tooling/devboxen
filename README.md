# devboxen

Development boxes on your own servers: long-lived, isolated containers with their own
Docker, built for working with coding agents like Claude Code — from a terminal on your
laptop or from any browser, your phone included.

> **Status: work in progress.** The pieces below run in daily use; the `devboxen` CLI that
> sets up and drives them for anyone is being built. Until then this is a reference, not a
> product.

## What you get

- **Boxes with their own Docker.** Each box is a [Sysbox](https://github.com/nestybox/sysbox)
  system container: `docker compose up` inside works as on a workstation, at native speed,
  without `--privileged` and without the host's Docker socket. Several boxes per host, one
  per project or client.
- **Sessions that keep running.** Every shell and every Claude Code runs in tmux inside the
  box: close the laptop, lose the network, come back from another device — it is still there.
- **Claude Code, ready.** One login for all boxes of a host, Playwright MCP with a headed
  Chrome on a virtual screen you can watch from the browser ("Claude's screen").
- **In the browser.** VS Code (code-server), the same tmux sessions as in your terminal, and a
  Chrome running *in* the box, so `https://localhost` of your dev stack just works. Usable on
  a phone: a key row for Esc/Tab/Ctrl/arrows, finger scrolling, automatic reconnects.
- **GitHub without keys in the box.** Fine-grained tokens per owner, picked by `git` and `gh`
  per repository; no SSH key, no forwarded agent.
- **Your hosts.** Any Ubuntu 24.04 VPS, and optionally an on-demand EC2 host (OpenTofu
  module) that runs only while you use it and stops itself every night.
- **Projects can adapt.** A `.devbox/` folder in a project adds a compose overlay (e.g. a
  database on tmpfs) and trusted start hooks, without changing anything outside a box.

## How it compares

devboxen is small: a Go binary, a few shell scripts and Ansible plays; no Kubernetes, no
database, no multi-tenant machinery. It is meant for one person or a small team who own
their servers. If you need users, roles, templates and an admin UI for a whole
organization, look at [Coder](https://github.com/coder/coder). If you want devcontainers on
any backend without a server component, look at [DevPod](https://github.com/loft-sh/devpod).

## Layout

| | |
|---|---|
| [`box/`](box/) | the box image and its compose file: Ubuntu, Docker, Chrome, code-server, ttyd, Claude Code, the GitHub helpers ([docs/box.md](docs/box.md)) |
| [`host/`](host/) | on each host: `devbox` (enter a box), `devbox-admin` (manage boxes), the host firewall, the tunnel unit, optional hardening |
| [`cmd/devboxen-web/`](cmd/devboxen-web/), [`web/`](web/) | the browser UI: a Go server and its page; front, backend, Caddy ([docs/web.md](docs/web.md)) |
| [`ansible/`](ansible/) | provisioning and deploy: `site.yml` runs everything ([inventory example](ansible/inventory.example.ini)) |
| [`tofu/aws-host/`](tofu/aws-host/) | an OpenTofu module for an on-demand EC2 host with a separate data volume |
| [`docs/`](docs/) | [boxes](docs/box.md), [the browser UI](docs/web.md), [security](docs/security.md) |

## Running it today (without the CLI)

You need an Ubuntu 24.04 host (x86_64) you reach as root with an SSH key, a domain pointing
at it, and Ansible on your laptop.

```bash
cd ansible
ansible-galaxy collection install -r requirements.yml
cp inventory.example.ini my.ini                          # your host(s), name, domain
htpasswd -B -C 12 -c ~/devboxen.htpasswd you             # the browser UI's password
ansible-playbook -i my.ini site.yml -e devboxen_web_htpasswd_file=~/devboxen.htpasswd
ssh root@<host> devbox-admin create main                 # the first box
ssh -t root@<host> devbox                                # in it
```

Then open `https://<your domain>`.

## License

[MIT](LICENSE)
