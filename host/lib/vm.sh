#!/usr/bin/env bash
#
# devboxen's virtual machines on a host: the VM of a VM box, and the builder that makes the
# base image all VM boxes are copied from. Installed to /usr/local/lib/devboxen/vm by
# ansible/vm.yml; used by devbox-admin, devbox, the units devbox-vm-web@ and the plays.
# See docs/vm.md.
#
#   vm create NAME MEM CPUS DISK CID   # disks from the base image, the domain, started
#   vm state NAME                      # running | stopped | missing
#   vm start NAME | stop NAME          # stop: a clean shutdown (90 s), then off
#   vm reimage NAME                    # a fresh system disk from the current base image (data disk stays)
#   vm destroy NAME                    # the domain and both disks
#   vm ssh [-t|-T] NAME [COMMAND]      # root in the VM, over vsock (NAME "builder": the builder)
#   vm proxy NAME                      # the vsock stream to the VM's sshd (an ssh ProxyCommand)
#   vm wait NAME [SECONDS]             # until the VM's sshd answers
#   vm web-forward NAME                # the box's browser sockets from the VM to the host (unit devbox-vm-web@)
#   vm builder-start ID CLOUD_IMAGE    # a builder VM from the Ubuntu cloud image, for base image ID
#   vm builder-finish ID               # the shut-down builder's disk becomes base image ID
#
# A VM box NAME is the libvirt domain devbox-NAME with two disks in /var/lib/devbox/vms/NAME:
# system.qcow2 (a copy of the base image: Ubuntu, Docker, Sysbox, the box image) and
# data.raw (ext4, label devbox-data, mounted at /var/lib/docker/volumes in the VM: the box's
# volumes). Its settings are devbox-admin's /var/lib/devbox/boxes/NAME.env, with
# DEVBOX_VM_CID, the VM's vsock address.
#
# The host reaches a VM only over vsock (sshd on vsock port 22 in the VM, with the key
# /etc/devboxen/vm_ed25519), never over the VM network. The VM's identity is its vsock
# address, which only that VM has, so its SSH host key is not checked.
set -euo pipefail

VMS=/var/lib/devbox/vms
BASE_DIR="$VMS/base"
BASE="$BASE_DIR/current.qcow2"
BOXES=/var/lib/devbox/boxes
WEB=/var/lib/devbox/web/boxes
KEY=/etc/devboxen/vm_ed25519
NETWORK=devboxen
BUILDER=devboxen-builder
BUILDER_CID=3
SYSTEM_SIZE=40G

die() { echo "vm: $*" >&2; exit 1; }
say() { echo "==> $*" >&2; }

valid_name() { [[ "$1" =~ ^[a-z0-9][a-z0-9-]{0,10}$ ]]; }
domain() { if [ "$1" = builder ]; then echo "$BUILDER"; else echo "devbox-$1"; fi; }

cid_of() {
  local name="$1" cid
  if [ "$name" = builder ]; then echo "$BUILDER_CID"; return; fi
  valid_name "$name" || die "no such VM box: $name"
  cid="$(sed -n 's/^DEVBOX_VM_CID=//p' "$BOXES/$name.env" 2>/dev/null)"
  [[ "$cid" =~ ^[0-9]+$ ]] || die "$name is not a VM box"
  echo "$cid"
}

# 8g, 6144m -> MiB
mib() {
  case "$1" in
    *g) echo $((${1%g} * 1024)) ;;
    *m) echo "${1%m}" ;;
    *) die "a size like 8g or 6144m, not $1" ;;
  esac
}

ssh_opts() { # CID -> SSH_OPTS
  SSH_OPTS=(-i "$KEY" -o IdentitiesOnly=yes -o IdentityAgent=none -o ForwardAgent=no
    -o "ProxyCommand=socat - VSOCK-CONNECT:$1:22"
    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR
    -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 -o ServerAliveCountMax=3)
}

state() {
  local s
  s="$(virsh domstate "$(domain "$1")" 2>/dev/null)" || { echo missing; return; }
  case "$s" in
    running | paused | "in shutdown") echo running ;;
    *) echo stopped ;;
  esac
}

# The cloud-init seed: who may log in (the host's VM key, and whoever may log in to the host
# as root, for `devboxen deploy` through the host), sshd on vsock, the hostname. A new
# instance-id makes cloud-init run again on a fresh copy of the base image: new SSH host keys,
# a new machine-id.
seed() { # DIR HOSTNAME INSTANCE_ID
  local dir="$1" hostname="$2" id="$3" keys
  keys="$( (cat "$KEY.pub"; grep -E '^(ssh-|ecdsa-|sk-)' /root/.ssh/authorized_keys 2>/dev/null || true) | sed 's/^/      /')"
  cat >"$dir/meta-data" <<EOF
instance-id: $id
local-hostname: $hostname
EOF
  cat >"$dir/user-data" <<EOF
#cloud-config
hostname: $hostname
manage_etc_hosts: true
disable_root: false
ssh_pwauth: false
users: []
write_files:
  - path: /root/.ssh/authorized_keys
    permissions: "0600"
    content: |
$keys
  - path: /etc/systemd/system/sshd-vsock.socket
    content: |
      # devboxen: sshd on vsock port 22, how the host reaches this VM (no network needed).
      [Unit]
      Description=OpenSSH on vsock (devboxen)
      [Socket]
      ListenStream=vsock::22
      Accept=yes
      [Install]
      WantedBy=sockets.target
  - path: /etc/systemd/system/sshd-vsock@.service
    content: |
      [Unit]
      Description=OpenSSH on vsock, one connection (devboxen)
      [Service]
      ExecStart=-/usr/sbin/sshd -i
      StandardInput=socket
      StandardError=journal
      RuntimeDirectory=sshd
      RuntimeDirectoryPreserve=yes
  - path: /etc/ssh/sshd_config.d/10-devboxen-vm.conf
    content: |
      # devboxen: the host comes in over vsock; on the network, sshd answers the VM itself only.
      ListenAddress 127.0.0.1
runcmd:
  - [systemctl, daemon-reload]
  - [systemctl, enable, --now, sshd-vsock.socket]
  - [systemctl, restart, ssh.socket]
EOF
  cloud-localds "$dir/seed.iso" "$dir/user-data" "$dir/meta-data"
  rm -f "$dir/user-data" "$dir/meta-data"
}

# cache=unsafe: the host ignores the guest's flushes (much faster IO; docs/vm.md says what that
# costs). It rules out io=native, which needs O_DIRECT.
define() { # DOMAIN DIR MEM_MIB CPUS CID [data disk]
  local dom="$1" dir="$2" mem="$3" cpus="$4" cid="$5" data="${6:-}"
  local disks=(--disk "path=$dir/system.qcow2,format=qcow2,bus=virtio,cache=unsafe,io=threads,discard=unmap")
  [ -z "$data" ] || disks+=(--disk "path=$data,format=raw,bus=virtio,cache=unsafe,io=threads,discard=unmap")
  disks+=(--disk "path=$dir/seed.iso,device=cdrom")
  virt-install --name "$dom" --memory "$mem" --vcpus "$cpus" --cpu host-passthrough \
    --osinfo ubuntu24.04 --import "${disks[@]}" \
    --network "network=$NETWORK,model=virtio" \
    --vsock "cid.auto=no,cid.address=$cid" \
    --memballoon "model=virtio,freePageReporting=on" \
    --graphics none --console pty,target_type=serial \
    --noautoconsole --quiet
  drop_pool "$dir"
}

# virt-install registers the directory of a VM's disks as a libvirt storage pool, named
# after the directory. devboxen does not use pools, and one whose directory is gone (the
# builder's) fails at every libvirt start; so it goes again right away.
drop_pool() { # DIR
  local dir="$1" pool
  for pool in $(virsh pool-list --all --name 2>/dev/null); do
    if [ "$(virsh pool-dumpxml "$pool" 2>/dev/null | sed -n 's|.*<path>\(.*\)</path>.*|\1|p' | head -n1)" = "$dir" ]; then
      virsh pool-destroy "$pool" >/dev/null 2>&1 || true
      virsh pool-undefine "$pool" >/dev/null 2>&1 || true
    fi
  done
}

system_disk() { # DIR: a fresh system disk from the current base image
  [ -f "$BASE" ] || die "there is no base image yet (devboxen deploy sets the host up)"
  cp --sparse=always "$(readlink -f "$BASE")" "$1/system.qcow2.new"
  mv "$1/system.qcow2.new" "$1/system.qcow2"
}

cmd_create() { # NAME MEM CPUS DISK CID
  local name="$1" mem="$2" cpus="$3" disk="$4" cid="$5" dir="$VMS/$1"
  valid_name "$name" || die "invalid name: $name"
  [ "$(state "$name")" = missing ] || die "a VM devbox-$name exists already"
  install -d -m 0711 "$dir"
  say "the VM's system disk, from the base image"
  system_disk "$dir"
  say "its data disk ($disk, sparse)"
  truncate -s "${disk^^}" "$dir/data.raw"
  mkfs.ext4 -q -F -L devbox-data -m 1 -E lazy_itable_init=1,lazy_journal_init=1 "$dir/data.raw"
  seed "$dir" "$name" "devbox-$name-$(date +%s)"
  say "the VM devbox-$name: $mem memory, $cpus CPUs"
  define "devbox-$name" "$dir" "$(mib "$mem")" "$cpus" "$cid" "$dir/data.raw"
  virsh autostart "devbox-$name" >/dev/null
}

cmd_start() {
  local dom; dom="$(domain "$1")"
  virsh autostart "$dom" >/dev/null
  [ "$(state "$1")" = running ] || virsh start "$dom" >/dev/null
}

cmd_stop() {
  local dom; dom="$(domain "$1")"
  [ "$1" = builder ] || virsh autostart --disable "$dom" >/dev/null 2>&1 || true
  [ "$(state "$1")" = running ] || return 0
  virsh shutdown "$dom" >/dev/null 2>&1 || true
  for _ in $(seq 90); do
    [ "$(state "$1")" = running ] || return 0
    sleep 1
  done
  say "devbox $1 did not shut down within 90 s; turning it off"
  virsh destroy "$dom" >/dev/null 2>&1 || true
}

cmd_reimage() {
  local name="$1" dir="$VMS/$1"
  [ -f "$dir/data.raw" ] || die "no VM disks for $name"
  cmd_stop "$name"
  say "a fresh system disk for devbox $name (its data disk stays)"
  system_disk "$dir"
  seed "$dir" "$name" "devbox-$name-$(date +%s)"
  cmd_start "$name"
}

cmd_destroy() {
  local name="$1" dom="devbox-$1"
  valid_name "$name" || die "invalid name: $name"
  virsh destroy "$dom" >/dev/null 2>&1 || true
  virsh undefine "$dom" --nvram >/dev/null 2>&1 || virsh undefine "$dom" >/dev/null 2>&1 || true
  rm -rf "${VMS:?}/$name"
}

cmd_ssh() {
  local tty=-T
  case "${1:-}" in -t | -T) tty="$1"; shift ;; esac
  local name="${1:?vm ssh NAME [COMMAND]}"; shift
  ssh_opts "$(cid_of "$name")"
  exec ssh "${SSH_OPTS[@]}" "$tty" "root@$(domain "$name")" "$@"
}

cmd_proxy() { exec socat - "VSOCK-CONNECT:$(cid_of "$1"):22"; }

cmd_wait() {
  local name="$1" timeout="${2:-300}" i
  ssh_opts "$(cid_of "$name")"
  for ((i = 0; i < timeout; i += 3)); do
    if ssh "${SSH_OPTS[@]}" -o ConnectTimeout=3 "root@$(domain "$name")" true </dev/null 2>/dev/null; then
      return 0
    fi
    sleep 3
  done
  die "$(domain "$name") did not answer within $timeout s (virsh console $(domain "$name"))"
}

# The browser UI reaches a box's sockets in /var/lib/devbox/web/boxes/NAME (docs/web.md). For
# a VM box they are in the VM, at the same path; this forwards each one to the host. Runs
# until the connection ends; the unit starts it again.
cmd_web_forward() {
  local name="$1" dir="$WEB/$1" s
  ssh_opts "$(cid_of "$name")"
  [ "$(state "$name")" = running ] || die "devbox $name is not running"
  install -d -m 0755 "$dir"
  local fwd=()
  for s in code term api screen watch; do
    rm -f "$dir/$s.sock"
    fwd+=(-L "$dir/$s.sock:$dir/$s.sock")
  done
  exec ssh "${SSH_OPTS[@]}" -N -o ExitOnForwardFailure=yes \
    -o StreamLocalBindUnlink=yes -o StreamLocalBindMask=0177 "${fwd[@]}" "root@devbox-$name"
}

cmd_builder_start() { # ID CLOUD_IMAGE
  local id="$1" image="$2" dir="$VMS/builder"
  [ -f "$image" ] || die "no cloud image at $image"
  virsh destroy "$BUILDER" >/dev/null 2>&1 || true
  virsh undefine "$BUILDER" --nvram >/dev/null 2>&1 || virsh undefine "$BUILDER" >/dev/null 2>&1 || true
  rm -rf "$dir"
  install -d -m 0711 "$dir"
  qemu-img convert -O qcow2 "$image" "$dir/system.qcow2"
  qemu-img resize -q "$dir/system.qcow2" "$SYSTEM_SIZE"
  seed "$dir" devboxen-base "devboxen-base-$id"
  say "the builder VM for base image $id"
  define "$BUILDER" "$dir" 8192 4 "$BUILDER_CID"
}

cmd_builder_finish() { # ID
  local id="$1" dir="$VMS/builder"
  [ "$(state builder)" = stopped ] || die "the builder is still running"
  install -d -m 0700 "$BASE_DIR"
  say "base image $id"
  qemu-img convert -O qcow2 "$dir/system.qcow2" "$BASE_DIR/base-$id.qcow2.new"
  mv "$BASE_DIR/base-$id.qcow2.new" "$BASE_DIR/base-$id.qcow2"
  ln -sfn "base-$id.qcow2" "$BASE"
  virsh undefine "$BUILDER" --nvram >/dev/null 2>&1 || virsh undefine "$BUILDER" >/dev/null 2>&1 || true
  rm -rf "$dir"
  # VM boxes have copies, not overlays: only the newest two base images are kept.
  ls -1t "$BASE_DIR"/base-*.qcow2 | tail -n +3 | xargs -r rm -f
}

[ "$(id -u)" = 0 ] || die "run as root"
cmd="${1:-}"; shift || true
case "$cmd" in
  create) [ $# -eq 5 ] || die "vm create NAME MEM CPUS DISK CID"; cmd_create "$@" ;;
  state) state "${1:?vm state NAME}" ;;
  start) cmd_start "${1:?vm start NAME}" ;;
  stop) cmd_stop "${1:?vm stop NAME}" ;;
  reimage) cmd_reimage "${1:?vm reimage NAME}" ;;
  destroy) cmd_destroy "${1:?vm destroy NAME}" ;;
  ssh) cmd_ssh "$@" ;;
  proxy) cmd_proxy "${1:?vm proxy NAME}" ;;
  wait) cmd_wait "$@" ;;
  web-forward) cmd_web_forward "${1:?vm web-forward NAME}" ;;
  builder-start) [ $# -eq 2 ] || die "vm builder-start ID CLOUD_IMAGE"; cmd_builder_start "$@" ;;
  builder-finish) cmd_builder_finish "${1:?vm builder-finish ID}" ;;
  *) die "unknown command: ${cmd:-none} (see the header of $0)" ;;
esac
