# devboxen

devboxen runs your development environments on your own servers instead of on your laptop.
Each environment is an isolated container with its own Docker, so a project's complete
Docker Compose stack and a coding agent like Claude Code can work there around the clock,
without access to your laptop, your SSH keys or your other projects. You use them from a
terminal or from any browser, also on your phone, and everything keeps running when you
disconnect.

**Status:** work in progress. Everything described here is in daily use, but setting it up
still means running Ansible by hand. A `devboxen` command line tool that does the setup for
you is being built.

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
| [`docs/`](docs/) | [Boxes](docs/box.md), [browser interface](docs/web.md), [security](docs/security.md) |

## Trying it today

You need an Ubuntu 24.04 server (x86_64) that you can reach as root with an SSH key, a domain
name that points to it, and Ansible on your computer.

```bash
cd ansible
ansible-galaxy collection install -r requirements.yml
cp inventory.example.ini my.ini                          # enter your server and domain
htpasswd -B -C 12 -c ~/devboxen.htpasswd you             # password for the browser interface
ansible-playbook -i my.ini site.yml -e devboxen_web_htpasswd_file=~/devboxen.htpasswd
ssh root@<server> devbox-admin create main               # create the first box
ssh -t root@<server> devbox                              # open a shell in it
```

Then open `https://<your domain>` in a browser.

## License

[MIT](LICENSE)
