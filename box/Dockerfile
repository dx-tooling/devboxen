# The devbox: an isolated development environment with its own Docker daemon, run with
# the Sysbox runtime (see docs/box.md). Ubuntu 24.04, like the host; Docker is the Ubuntu
# build, like the host's.
FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl gnupg \
        git openssh-client rsync jq make unzip zip less vim-tiny nano \
        procps iproute2 iputils-ping bind9-dnsutils nftables kmod sudo locales tzdata \
        tmux ncurses-term \
        docker.io docker-compose-v2 docker-buildx containerd \
    && mkdir -p -m 755 /etc/apt/keyrings \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        -o /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh \
    && locale-gen en_US.UTF-8 \
    && rm -rf /var/lib/apt/lists/*

ENV LANG=en_US.UTF-8

# GitHub's SSH host keys, as GitHub publishes them (over HTTPS): no trust-on-first-use
# prompt should anything still speak SSH to GitHub (git itself goes over HTTPS, below).
RUN curl -fsSL https://api.github.com/meta \
        | jq -r '.ssh_keys[] | "github.com " + .' > /etc/ssh/ssh_known_hosts \
    && test -s /etc/ssh/ssh_known_hosts

# A real browser for Claude and for e2e tests, on a virtual screen (no GPU, no monitor):
# Google Chrome stable (Ubuntu's Chromium is a snap, which does not run in containers),
# fonts so pages render properly, and Node.js LTS for the Playwright MCP server (Claude's
# browser tools, see docs/box.md). The virtual screens are further down (xvfb stays for
# xvfb-run).
RUN curl -fsSL https://dl.google.com/linux/linux_signing_key.pub \
        | gpg --dearmor -o /etc/apt/keyrings/google-chrome.gpg \
    && echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/google-chrome.gpg] https://dl.google.com/linux/chrome/deb/ stable main" \
        > /etc/apt/sources.list.d/google-chrome.list \
    && curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key \
        | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg \
    && echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_26.x nodistro main" \
        > /etc/apt/sources.list.d/nodesource.list \
    && apt-get update && apt-get install -y --no-install-recommends \
        google-chrome-stable nodejs \
        xvfb xauth x11-utils imagemagick \
        fonts-liberation fonts-noto-core fonts-noto-cjk fonts-noto-color-emoji \
    && rm -rf /var/lib/apt/lists/*

# Pinned, released >= 2 weeks before it was chosen (2026-11-04).
ARG PLAYWRIGHT_MCP_VERSION=0.0.83
RUN npm install -g --no-fund --no-audit "@playwright/mcp@${PLAYWRIGHT_MCP_VERSION}" \
    && npm cache clean --force

# Every process sees the virtual screen; browsers may run headed.
ENV DISPLAY=:99

# The box in the browser (devboxen-web, docs/web.md): VS Code (code-server) and a
# terminal (ttyd) on Unix sockets in /run/devbox-web, started by the entrypoint. Pinned with
# checksums; both released >= 2 weeks before they were chosen (2026-11-07). ttyd is the
# upstream static build (Ubuntu's is 1.7.4).
ARG CODE_SERVER_VERSION=4.141.1
ARG CODE_SERVER_SHA256=7a15eae0c4fc102a9099a37f728688f0219fe8d5752943df44ccbe152563adef
ARG TTYD_VERSION=1.7.7
ARG TTYD_SHA256=8a217c968aba172e0dbf3f34447218dc015bc4d5e59bf51db2f2cd12b7be4f55
RUN curl -fsSL -o /tmp/code-server.deb \
        "https://github.com/coder/code-server/releases/download/v${CODE_SERVER_VERSION}/code-server_${CODE_SERVER_VERSION}_amd64.deb" \
    && echo "${CODE_SERVER_SHA256}  /tmp/code-server.deb" | sha256sum -c - \
    && apt-get update && apt-get install -y --no-install-recommends /tmp/code-server.deb \
    && rm -rf /tmp/code-server.deb /var/lib/apt/lists/* \
    && curl -fsSL -o /usr/local/bin/ttyd \
        "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.x86_64" \
    && echo "${TTYD_SHA256}  /usr/local/bin/ttyd" | sha256sum -c - \
    && chmod 0755 /usr/local/bin/ttyd

# The virtual screens: TigerVNC's Xvnc, an X server that can be watched over VNC (display
# :99 for Claude and the tests, :98 for the browser in devbox-web; both started by the
# entrypoint), noVNC (the viewer, served to devbox-web by web-vnc.js), and matchbox, which
# keeps :98's windows full-screen.
RUN apt-get update && apt-get install -y --no-install-recommends \
        tigervnc-standalone-server novnc matchbox-window-manager \
    && rm -rf /var/lib/apt/lists/*

# The one login user. It may use Docker and sudo: root inside the devbox is an
# unprivileged user on the host (Sysbox user namespace), so this grants nothing there.
RUN userdel -r ubuntu 2>/dev/null || true \
    && useradd -m -u 1000 -s /bin/bash -G docker,sudo dev \
    && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev && chmod 0440 /etc/sudoers.d/dev

# Claude Code (native installer, into ~/.local; it updates itself from there). /home/dev
# is a named volume: Docker seeds it from this image on first creation only.
USER dev
RUN curl -fsSL https://claude.ai/install.sh | bash \
    && git config --global init.defaultBranch main
USER root
# PATH for every process. First the `claude` wrapper (it logs in with the token all boxes
# share, see claude-wrapper.sh), then Claude Code's own install dir. Login shells reorder
# PATH in ~/.profile; entrypoint.sh puts the wrapper back in front there.
COPY claude-wrapper.sh /usr/local/lib/devbox/bin/claude
RUN chmod 0755 /usr/local/lib/devbox/bin/claude
# GitHub (docs/box.md, "GitHub"): over HTTPS with the token of the repository's owner, from
# the tokens all boxes share. `gh` is a wrapper that picks it; git asks the credential
# helper. SSH remotes (git@github.com:…, also in existing checkouts) are rewritten to HTTPS,
# as there is no SSH key in a box.
COPY gh-wrapper.sh /usr/local/lib/devbox/bin/gh
COPY git-credential-github.sh /usr/local/lib/devbox/git-credential-github
RUN chmod 0755 /usr/local/lib/devbox/bin/gh /usr/local/lib/devbox/git-credential-github \
    && git config --system credential.https://github.com.helper '' \
    && git config --system --add credential.https://github.com.helper /usr/local/lib/devbox/git-credential-github \
    && git config --system credential.https://github.com.useHttpPath true \
    && git config --system url.https://github.com/.insteadOf git@github.com: \
    && git config --system --add url.https://github.com/.insteadOf ssh://git@github.com/
ENV PATH=/usr/local/lib/devbox/bin:/home/dev/.local/bin:$PATH

# The inner Docker daemon's config, and the host's CVE-2026-98432 firewall (same file,
# applied to the devbox's own network namespace; see docs/security.md).
COPY daemon.json /etc/docker/daemon.json
# `docker compose` with a project's .devbox/compose.yaml merged on top (the .devbox
# contract, see docs/box.md). This plugin directory comes before the real plugin's
# (/usr/libexec/docker/cli-plugins), which the wrapper then runs.
COPY compose-overlay.sh /usr/local/lib/docker/cli-plugins/docker-compose
RUN chmod 0755 /usr/local/lib/docker/cli-plugins/docker-compose \
    && test -x /usr/libexec/docker/cli-plugins/docker-compose
# The projects' start hooks (.devbox/on-start; started by the entrypoint), and their
# one-line notice in login shells.
COPY devbox-hooks.sh /usr/local/bin/devbox-hooks
COPY profile-devbox-hooks.sh /etc/profile.d/devbox-hooks.sh
RUN chmod 0755 /usr/local/bin/devbox-hooks && chmod 0644 /etc/profile.d/devbox-hooks.sh
COPY tmux.conf /etc/tmux.conf
# devbox-web's pieces in the box: the terminal's attach command, the session API and the
# screens' bridge (noVNC's files and the VNC displays, on Unix sockets).
COPY web-attach.sh /usr/local/bin/devbox-web-attach
COPY web-api.js /usr/local/lib/devbox/web-api.js
COPY web-vnc.js /usr/local/lib/devbox/web-vnc.js
RUN chmod 0755 /usr/local/bin/devbox-web-attach \
    && chmod 0644 /usr/local/lib/devbox/web-api.js /usr/local/lib/devbox/web-vnc.js
COPY --from=host firewall/host-firewall.nft /etc/devbox-firewall.nft
COPY entrypoint.sh /usr/local/sbin/devbox-entrypoint
RUN chmod 0755 /usr/local/sbin/devbox-entrypoint

WORKDIR /home/dev
ENTRYPOINT ["/usr/local/sbin/devbox-entrypoint"]
