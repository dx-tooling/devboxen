# Security

What a box can and cannot do, and why it is built that way.

## Why a Sysbox container, not a VM

Many VPS are KVM guests **without nested virtualization** (no `svm`/`vmx`, no `/dev/kvm`),
so a VM would only run emulated (QEMU TCG, 5–20× slower). Docker-in-Docker with
`--privileged` or the host's Docker socket would be fast, but is root on the host.

[Sysbox](https://github.com/nestybox/sysbox) (installed by
[`ansible/sysbox.yml`](../ansible/sysbox.yml)) is a container runtime that runs such "system
containers" unprivileged:

| | |
|---|---|
| **User namespace** | root inside is UID 100000 on the host (`/proc/self/uid_map`: `0 100000 65536`); `dev` (UID 1000) is 101000 |
| **No `--privileged`, no host Docker socket** | a box's `/var/run/docker.sock` is its own daemon; it cannot see or touch the host's containers |
| **Virtualized `/proc`, `/sys`** | partly emulated by `sysbox-fs`; no host devices |
| **Limits** | per box, default `cpus: 6`, `mem_limit: 8g` (`--mem`, `--cpus` at creation); `pids_limit: 16384`, `shm_size: 2g`. CPU limits may add up past the host's cores; memory limits that add up past the host's free memory risk the OOM killer picking something else when the boxes are busy. Stop boxes you are not using. |
| **Not public, separate** | no published ports, on no network but its own (bridge `dbx-<name>`): boxes cannot reach each other; no box reaches the sshd of any devboxen host |
| **seccomp** | Docker's default profile minus MPTCP sockets, see below |

It is **kernel-shared isolation**: a kernel exploit escapes it, as from any container. Only
a VM closes that, which needs nested virtualization from the hosting provider.

## What a box holds

Everything running in a box can use what the box has: Claude, a project's dependencies, any
image pulled. So:

- **No SSH key and no forwarded agent**: nothing in a box logs in to a server.
- **GitHub tokens** (fine-grained, one per owner) reach only their owner's repositories,
  with the permissions given to them on GitHub, and expire. Every box of a host can read all
  of them; a token that leaked is revoked on GitHub and replaced.
- **The Claude Code token** is shared by the boxes of a host, the same way.
- **The host firewall** ([`host-firewall.nft`](../host/firewall/host-firewall.nft)) rejects
  connections from every box network (bridges `dbx-*`) to the host's sshd on any of its
  addresses (chain `devbox_no_host_ssh`), nested containers included; and to the public
  address of every devboxen host (chain `devbox_no_devbox_host_ssh`, set `devboxen_hosts`,
  filled from the inventory): on EC2 the Elastic IP is not on the host's interfaces, so that
  connection is forwarded and comes back in from outside, past the first chain.

`dev` may use Docker and `sudo` in a box; both only reach the box.

## The browser UI

See [`web.md`](web.md#security): its own password check (bcrypt) with a signed session
cookie, same-origin checks for WebSockets and state changes, the password and cookie never
passed on to a box, and nothing of a box on a network (Unix sockets only, read-only mounts,
no Docker socket in the web containers).

## CVE-2026-98432

A Linux IPv4 TCP use-after-free in `ip_options_build()` (remote root), reached through a SYN
with IPv4 options; the published exploit needs an MPTCP listener. Until the host runs a
fixed kernel, devboxen mitigates it on the host (with `devboxen_hardening`, the default) and
in every box.

**On the host** ([`ansible/host.yml`](../ansible/host.yml)):

- the host firewall drops IPv4 TCP with options, and TCP with the MPTCP option from Docker
  networks (bridged traffic too, table `bridge host_firewall`);
- `net.mptcp.enabled=0`;
- `init_on_free=1` on the kernel command line (from the next boot).

**In a box**, those do not reach: a box's inner Docker networks live in its own network
namespace, which the host firewall never sees, and `net.mptcp.enabled` is per network
namespace (each new one starts with MPTCP on). Worse, root inside a box could create an
MPTCP listener itself and attack the shared kernel locally. So each box has three layers:

1. **seccomp** ([`seccomp.json`](../box/seccomp.json), built by
   [`seccomp/generate.sh`](../box/seccomp/generate.sh)): `socket(…, IPPROTO_MPTCP)` fails
   with `EPERM` for every process in the box, nested containers included. Nothing inside
   can lift a seccomp filter, so this holds whatever root inside does. **This is the layer
   that counts.** (`socketcall` is dropped; io_uring is already denied by Docker's default.)
2. `net.mptcp.enabled=0` in the box's namespace, set by
   [`entrypoint.sh`](../box/entrypoint.sh).
3. The host's ruleset, loaded into the box's namespace by the entrypoint: it drops TCP with
   IPv4 options and MPTCP options on the inner `docker0`/`br-*` bridges.

Verified: with `net.mptcp.enabled=1` re-set by root inside, an MPTCP listener still fails in
a box and in a nested container; ordinary networking works. The seccomp change can go once
the hosts run a fixed kernel.
