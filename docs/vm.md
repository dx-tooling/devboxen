# VM boxes

On a host with hardware virtualization, each box can run in its own virtual machine. A
container box shares the host's kernel with every other box: a kernel exploit in one box
reaches the host and all boxes. A VM box has its own kernel, so the same exploit stays in
that one VM.

Everything you do with a box works the same for both kinds: `devboxen shell` and `claude`,
the browser interface, ports and tunnels, file transfer, the GitHub and Claude Code tokens,
the `.devbox` folder of your projects.

## When a host has VM boxes

A host gets VM boxes when it has `/dev/kvm`. Many VPS do not: they are virtual machines
themselves, and need nested virtualization from the provider. Check with
`ls /dev/kvm` and `grep -cw -e svm -e vmx /proc/cpuinfo` on the host.

| Setting in `[hosts.<name>]` | |
|---|---|
| `vms` unset | VM boxes if the host has `/dev/kvm` (the default) |
| `vms = true` | VM boxes; the deploy fails if the host has no `/dev/kvm` |
| `vms = false` | no VM boxes |
| `containers = false` | only VM boxes: no Docker and no Sysbox on the host itself; devboxen-web's backend runs as a systemd service there. Not for the host with the browser interface's front. |

On a host with VM boxes, a new box is a VM unless you ask for a container:

```bash
devboxen create client2@kvmhost --mem 16g --cpus 8 --disk 200g   # a VM box
devboxen create tools@kvmhost --container                         # a container box
```

`devboxen list` shows the kind of each box.

## What it costs

A VM reserves its memory while it runs (a container uses only what it needs), and work in it
runs slower: by about 15 to 20% for builds, test suites and container starts, measured on a
VPS that offers nested virtualization (a Linux kernel build: 122 s on the host, 142 s in a
VM). Pure computation loses little; what costs most is starting processes and switching
between them, since each switch out of the VM goes through the hypervisor (on a VPS, through
two). On a physical server the cost is lower.

| | Container box | VM box |
|---|---|---|
| Isolation | Sysbox: user namespace, seccomp; shares the host's kernel | its own kernel, and inside it the same container |
| Memory | uses what it needs, up to `--mem` | reserves `--mem` while running; about 1 GB of it goes to the VM itself |
| Disk | volumes on the host's disk | a data disk of its own (`--disk`, default 100g), sparse |
| Speed | as on the host | 15–20% slower on a VPS |
| Start | seconds | about half a minute |

## How it works

A VM box is a small virtual machine (libvirt and QEMU with KVM) that is a devboxen host with
exactly one box inside: Ubuntu 24.04, Docker, Sysbox and the normal box container, made from
the same plays and the same box image. So the box itself is the same in both kinds.

```
host
├── libvirt network "devboxen" (bridge dbv0, NAT, ports isolated)
├── VM devbox-client2 ── vsock ── host
│     ├── system disk  (a copy of the base image)
│     ├── data disk    (/var/lib/docker/volumes: the box's home and inner Docker)
│     └── container devbox-client2 (Sysbox) ── the box
└── /var/lib/devbox/web/boxes/client2/*.sock  (forwarded from the VM, for devboxen-web)
```

- **The base image.** `devboxen deploy` builds one per host: Ubuntu 24.04's cloud image, set
  up by devboxen's plays (`base.yml`, `sysbox.yml`, `host.yml`, then `vm-guest.yml`) in a
  builder VM, with the box image inside. It is rebuilt only when the files it comes from
  change. A new VM box starts as a copy of it, which takes about a minute.
- **Two disks per box** in `/var/lib/devbox/vms/<box>/`: the system disk and the data disk
  (ext4, label `devbox-data`), mounted at `/var/lib/docker/volumes` in the VM, where the
  box's volumes live. Both are sparse files on the host.
- **Rebuild.** `devboxen deploy --rebuild-boxes` (or `devbox-admin rebuild <box>`) gives a VM
  box a fresh system disk from the current base image, which brings a new box image, Docker
  and Ubuntu at once. The data disk stays, and with it everything in `/home/dev` and the box's
  Docker data. Sessions end, as with a container box.
- **The host reaches a VM only over vsock**, a channel between a host and its VMs that does
  not use the network: SSH to the VM's sshd on vsock port 22 with the key
  `/etc/devboxen/vm_ed25519` (`/usr/local/lib/devboxen/vm ssh <box>`). `devbox` (to enter a
  box), `devbox-admin` and the tunnels go this way. In the VM, sshd listens on the network
  only on `127.0.0.1`.
- **The browser interface** finds a box's sockets in `/var/lib/devbox/web/boxes/<box>/` on
  the host. For a VM box, the unit `devbox-vm-web@<box>` forwards them from the VM while it
  runs; devboxen-web sees no difference.
- **The tokens** (Claude Code, GitHub) are copied into each running VM when they are set,
  and at every start of the VM. There is no shared filesystem between host and VM.
- **Ubuntu's updates** in a VM: unattended upgrades, as on any Ubuntu, and a newer base image
  at each rebuild.

## Disk cache and clock

- **Disks: `cache=unsafe,io=threads`.** The host ignores the guest's flush requests, so writes
  go through the host's page cache: much faster IO for builds, `npm install` and Docker. The
  price: if the *host* crashes or loses power, a VM's disks can lose recent writes and, in the
  worst case, be inconsistent. A clean `virsh shutdown`/`devbox-admin stop` and a guest crash
  are safe. (`io=native` is not possible with it: that needs `cache=none`.)
- **Clock: `tsc=reliable clocksource=tsc`** on the guest kernel (the base image sets it, so a
  rebuild or a new box has it). Nested, the kernel's boot-time TSC check fails and kvm-clock
  has no vDSO fast path, so every `time()` is a syscall (about 680 ns instead of 90 ns). The
  kernel then no longer checks that the vCPUs' TSCs agree.

## Security

Inside the VM, the box keeps every protection of a container box (Sysbox, the seccomp
profile, the CVE-2026-98432 measures in [security.md](security.md)); the VM's own kernel gets
the host hardening of `host.yml`, except `init_on_free=1`: zeroing all of the VM's memory at
every boot takes minutes when the VM is nested, and it touches every page, so the VM could
hand none back to the host. A kernel exploit in a VM box reaches only that VM.

- **Network.** All VM boxes of a host are on the libvirt network `devboxen` (bridge `dbv0`,
  `10.198.0.0/24`), with NAT to the internet. Each VM's port is isolated: VMs cannot reach
  each other. The host firewall lets them reach only DHCP and DNS of the host, nothing else of
  it, and no devboxen host's sshd.
- **vsock** goes from the host to a VM. A VM can also connect to the host over vsock, so
  nothing on the host may listen on vsock: check with `ss -l --vsock` (empty). The box's
  seccomp profile denies vsock sockets (`AF_VSOCK`), so nothing in a box opens one at all.
- **Login to a VM:** the host's VM key, and the keys that may log in to the host as root
  (`devboxen deploy` reaches the builder through the host). No key and no agent ever goes
  into a VM or a box.
- **What a VM holds:** its one box, and the tokens every box of the host has anyway.

## Files

| File | |
|---|---|
| [`host/lib/vm.sh`](../host/lib/vm.sh) | the VMs on a host (`/usr/local/lib/devboxen/vm`): create, start, stop, reimage, destroy, SSH over vsock, the base image's builder |
| [`host/bin/devbox-admin`](../host/bin/devbox-admin), [`host/bin/devbox`](../host/bin/devbox) | both kinds of box |
| [`host/systemd/devbox-vm-web@.service`](../host/systemd/devbox-vm-web@.service) | a VM box's browser sockets, forwarded to the host |
| [`ansible/vm.yml`](../ansible/vm.yml) | libvirt, the network, vsock, the VM key, the cloud image; starts the builder |
| [`ansible/vm-guest.yml`](../ansible/vm-guest.yml) | finishes the builder into the base image |
| [`web/devboxen-web.service`](../web/devboxen-web.service) | the backend on a host without Docker |
