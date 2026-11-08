# devboxen.toml

The `devboxen` command reads its settings from `devboxen.toml`. It looks for the file in
the current directory and the directories above it, then in `~/.config/devboxen/`. The
environment variable `DEVBOXEN_CONFIG` names another file. Paths in the file are relative
to the file's directory.

`devboxen init` writes it, and `devboxen host add` adds hosts to it; you can also write it
yourself. Keep the file in a private Git repository of your own, together with the
encrypted secrets. A complete example: [`devboxen.example.toml`](../devboxen.example.toml).

## Top level

| Setting | Meaning |
|---|---|
| `default_host` | The host used when a command names none. Default: the first host. |
| `source` | A devboxen checkout to deploy from, instead of the files built into the CLI. For working on devboxen itself. `DEVBOXEN_SOURCE` does the same. |

## `[hosts.<name>]`

One section per server. The name is how you address the host (`main@<name>`): 1 to 11
characters, lowercase letters, digits and `-`.

| Setting | Meaning |
|---|---|
| `ssh` | `root@<address>`. devboxen uses your normal SSH setup (keys, agent, `~/.ssh/config`), but never forwards the agent. |
| `public_ipv4` | The host's public IPv4 address, if `ssh` uses a DNS name. The firewall needs the address. |
| `hardening` | `false` turns off the optional hardening (key-only SSH, journal size cap, kernel mitigations). Default: on. |
| `aws` | Makes this an on-demand AWS host: `{ profile, region, instance, sso_session, data_volume }`. `profile` is an AWS CLI profile; `sso_session` is used to log in again when the login has expired; `data_volume` is the EBS volume for the boxes' data. All of these come from the outputs of [`tofu/aws-host`](../tofu/aws-host/); `devboxen host add` fills them in ([aws.md](aws.md)). |

## `[web]`

| Setting | Meaning |
|---|---|
| `front` | The host that serves the browser interface for all hosts. Default: the default host. |
| `domain` | The interface's domain name. Required for `tls = "auto"`. |
| `tls` | `auto`: devboxen runs Caddy, which gets a certificate from Let's Encrypt (ports 80 and 443 must be free). `proxy`: you run your own reverse proxy; see [web.md](web.md#tls). |
| `compose_override` | For `proxy`: a Docker Compose file that connects the interface to your proxy. |
| `image` | A published devboxen-web image. Default: built on the host. |

## `[box]`

| Setting | Meaning |
|---|---|
| `image` | A published box image. Default: built on each host. |

## `[secrets]`

Secrets are encrypted with [age](https://age-encryption.org). The private key stays on your
computer; everything else can be committed.

| Setting | Meaning |
|---|---|
| `identity` | The private key. Never commit it. `devboxen secrets keygen` creates it. `DEVBOXEN_AGE_IDENTITY` names another file. |
| `recipients` | The public key, used to encrypt. Commit it. |
| `github_tokens` | The GitHub tokens, encrypted. Edit with `devboxen github edit`. |
| `web_htpasswd` | The browser interface's password, encrypted. Set with `devboxen web passwd`. |
