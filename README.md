# devboxen

devboxen runs your development environments on your own servers instead of on your laptop.
Each environment is an isolated container with its own Docker, so a project's complete
Docker Compose stack and a coding agent like Claude Code can work there around the clock,
without access to your laptop, your SSH keys or your other projects. You use them from a
terminal or from any browser, also on your phone, and everything keeps running when you
disconnect.

**Status:** early. Everything described here is in daily use. The `devboxen` command line
tool sets it all up; a guided first-time setup is still to come.

## Is it for you?

It fits if:

- you develop projects that run as a Docker Compose stack, even in development;
- you let coding agents work on them and want that away from your own machine;
- you have, or can rent, a Linux server (any Ubuntu 24.04 VPS), and optionally an AWS
  account for a bigger machine that only runs when you need it;
- you are one person or a small team.

It does not fit if you need user management, roles and an admin UI for a larger
organization. Use [Coder](https://github.com/coder/coder) for that.

## What you get

- **Isolated boxes.** Each box is a container with its own Docker daemon (via
  [Sysbox](https://github.com/nestybox/sysbox)). `docker compose up` works as on a
  workstation and at the same speed. A box cannot reach the host, other boxes or the
  server's SSH. You can have several boxes per server, for example one per project.
- **Sessions that keep running.** Every terminal and every Claude Code session runs in tmux
  inside the box. Close the laptop, lose the connection, continue from another device.
- **Claude Code included.** One login for all boxes on a server. Claude can use a real
  Chrome browser in the box, and you can watch what it does.
- **Browser access.** VS Code, the terminal sessions and a Chrome that runs inside the box,
  so your app's `localhost` address works. Usable on a phone.
- **GitHub without SSH keys.** git and gh use fine-grained tokens, one per account or
  organization. No key and no SSH agent ever enters a box.
- **On-demand cloud server.** An optional OpenTofu module for an AWS EC2 instance that you
  start when you need it and that stops itself every night.

## Repository layout

| Directory | Contents |
|---|---|
| [`box/`](box/) | The box image: Ubuntu, Docker, Chrome, VS Code server, terminal, Claude Code ([docs](docs/box.md)) |
| [`host/`](host/) | Commands and settings for each server: `devbox`, `devbox-admin`, firewall |
| [`cmd/devboxen-web/`](cmd/devboxen-web/), [`web/`](web/) | The browser interface ([docs](docs/web.md)) |
| [`ansible/`](ansible/) | Server setup and deployment |
| [`tofu/aws-host/`](tofu/aws-host/) | OpenTofu module for an on-demand AWS server |
| [`cmd/devboxen/`](cmd/devboxen/) | The `devboxen` command line tool |
| [`docs/`](docs/) | [Settings](docs/config.md), [boxes](docs/box.md), [browser interface](docs/web.md), [security](docs/security.md) |

## Getting started

You need:

- a server with Ubuntu 24.04 (x86_64) that you can reach as root with your SSH key;
- a domain name that points to it, for the browser interface;
- on your computer: Go, `ssh` and Ansible.

```bash
go install github.com/dx-tooling/devboxen/cmd/devboxen@latest

mkdir my-devboxen && cd my-devboxen && git init      # a private repository for your setup
curl -fsSLo devboxen.toml https://raw.githubusercontent.com/dx-tooling/devboxen/main/devboxen.example.toml
$EDITOR devboxen.toml                                 # your server and domain
printf 'secrets/identity.txt\n' > .gitignore

devboxen secrets keygen        # the key pair for your encrypted secrets
devboxen web passwd            # the password for the browser interface
devboxen github edit           # optional: GitHub tokens for the boxes
devboxen deploy                # sets up the server (takes a few minutes)
devboxen host setup            # creates the first box, "main"
devboxen login                 # optional: one Claude Code login for all boxes
devboxen shell                 # a shell in it (or: devboxen claude)
```

Then open `https://<your domain>`. Commit everything except `secrets/identity.txt`, and keep
that file safe: without it, the secrets cannot be decrypted.

`devboxen help` lists all commands. The settings are described in
[docs/config.md](docs/config.md).

## License

[MIT](LICENSE)
