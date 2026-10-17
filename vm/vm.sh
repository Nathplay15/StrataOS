#!/bin/bash
# StrataOS test VM: Debian 13 cloud image under QEMU/KVM with 6 extra SATA disks.
#
#   vm.sh create            create the VM disks and run first boot (cloud-init)
#   vm.sh start | stop      boot / shut down the VM
#   vm.sh status            show whether the VM is running
#   vm.sh ssh [cmd...]      open a shell (or run a command) in the VM
#   vm.sh deploy <deb>      copy and install a StrataOS package
#   vm.sh console           follow the serial console log
#   vm.sh reset-disks       recreate the 6 data disks (blank)
#   vm.sh replace-disk N    swap data disk N for a new blank one (VM stopped)
#   VM_DETACH="2 5" vm.sh start   boot with data disks 2 and 5 unplugged
#   vm.sh destroy           delete the VM entirely
#
# Settings (environment): VM_MEM (MiB, 4096), VM_CPUS (4), VM_DISKS (6), VM_DISK_SIZE (10G),
# VM_WEB_PORT (7070), VM_SSH_PORT (2222), VM_EXTRA_PORTS ("8096 8080 8081 3001 9091"),
# VM_NICS (1: extra NICs for bonding tests), VM_STATE (state folder, for a second VM),
# VM_HOSTNAME (strata-vm), VM_HTTPS_PORT (7443), VM_SMB_PORT (4450), SEED_PORT (8719),
# VM_LAN (multicast address:port, e.g. 230.0.0.1:12345): an extra NIC on a LAN shared
# by every VM started with the same value (high availability tests).
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
IMAGES="$DIR/images"
STATE="${VM_STATE:-$DIR/state}"
BASE="$IMAGES/debian-13-generic-amd64.qcow2"
BASE_URL="https://cloud.debian.org/images/cloud/trixie/latest"
SYSDISK="$STATE/system.qcow2"
KEY="$STATE/id_ed25519"
PIDFILE="$STATE/qemu.pid"
MONITOR="$STATE/monitor.sock"
CONSOLE="$STATE/console.log"

VM_MEM=${VM_MEM:-4096}
VM_CPUS=${VM_CPUS:-4}
VM_DISKS=${VM_DISKS:-6}
VM_DISK_SIZE=${VM_DISK_SIZE:-10G}
VM_WEB_PORT=${VM_WEB_PORT:-7070}
VM_SSH_PORT=${VM_SSH_PORT:-2222}
VM_EXTRA_PORTS=${VM_EXTRA_PORTS-"8096 8080 8081 3001 9091"}
VM_HTTPS_PORT=${VM_HTTPS_PORT:-7443}
VM_SMB_PORT=${VM_SMB_PORT:-4450}
VM_HOSTNAME=${VM_HOSTNAME:-strata-vm}
SEED_PORT=${SEED_PORT:-8719}

SSH_OPTS=(-i "$KEY" -p "$VM_SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5)

log() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

running() { [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; }

need() {
  for b in qemu-system-x86_64 qemu-img ssh scp python3; do
    command -v "$b" >/dev/null || die "$b is required"
  done
  [ -r /dev/kvm ] && [ -w /dev/kvm ] || die "/dev/kvm is not accessible (add yourself to the kvm group)"
}

fetch_base() {
  mkdir -p "$IMAGES"
  if [ ! -f "$BASE" ]; then
    log "Downloading the Debian 13 cloud image"
    curl -fSL -o "$BASE.part" "$BASE_URL/$(basename "$BASE")"
    curl -fsSL -o "$IMAGES/SHA512SUMS" "$BASE_URL/SHA512SUMS"
    mv "$BASE.part" "$BASE"
    (cd "$IMAGES" && sha512sum -c --ignore-missing SHA512SUMS) || { rm -f "$BASE"; die "checksum mismatch"; }
  fi
}

make_data_disks() {
  for i in $(seq 1 "$VM_DISKS"); do
    rm -f "$STATE/data$i.qcow2"
    qemu-img create -q -f qcow2 "$STATE/data$i.qcow2" "$VM_DISK_SIZE"
  done
}

write_seed() {
  mkdir -p "$STATE/seed"
  local pub; pub=$(cat "$KEY.pub")
  cat > "$STATE/seed/meta-data" <<EOF
instance-id: strata-vm-$(date +%s)
local-hostname: $VM_HOSTNAME
EOF
  cat > "$STATE/seed/user-data" <<EOF
#cloud-config
hostname: $VM_HOSTNAME
timezone: Europe/Paris
ssh_pwauth: false
users:
  - name: debian
    gecos: StrataOS test VM
    groups: [sudo]
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    shell: /bin/bash
    lock_passwd: true
    ssh_authorized_keys:
      - $pub
package_update: true
packages:
  - qemu-guest-agent
  - curl
runcmd:
  - [systemctl, enable, --now, qemu-guest-agent]
  - [touch, /var/lib/strata-vm-ready]
EOF
  : > "$STATE/seed/vendor-data"
}

qemu_start() {
  local extra_fwd=""
  for p in $VM_EXTRA_PORTS; do extra_fwd+=",hostfwd=tcp:127.0.0.1:$p-:$p"; done
  local args=(
    -name "$VM_HOSTNAME" -machine q35,accel=kvm -cpu host -smp "$VM_CPUS" -m "$VM_MEM"
    -drive "file=$SYSDISK,if=none,id=sys,format=qcow2,discard=unmap"
    -device virtio-blk-pci,drive=sys,bootindex=0
    -device ahci,id=ahci0
    -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$VM_SSH_PORT-:22,hostfwd=tcp:127.0.0.1:$VM_WEB_PORT-:7070,hostfwd=tcp:127.0.0.1:$VM_HTTPS_PORT-:7443,hostfwd=tcp:127.0.0.1:$VM_SMB_PORT-:445$extra_fwd"
    -device virtio-net-pci,netdev=n0
    $(for k in $(seq 2 "${VM_NICS:-1}"); do echo "-netdev user,id=n$k -device virtio-net-pci,netdev=n$k"; done)
    $(if [ -n "${VM_LAN:-}" ]; then printf -- '-netdev socket,id=lan,mcast=%s -device virtio-net-pci,netdev=lan,mac=52:54:00:aa:%02x:%02x' "$VM_LAN" $((VM_SSH_PORT / 256 % 256)) $((VM_SSH_PORT % 256)); fi)
    -device virtio-rng-pci
    -smbios "type=1,serial=ds=nocloud;s=http://10.0.2.2:$SEED_PORT/"
    -display none -serial "file:$CONSOLE"
    -monitor "unix:$MONITOR,server,nowait"
    -pidfile "$PIDFILE" -daemonize
  )
  # Disks 1-6: SATA (AHCI) so that SMART queries work like on real hardware; 7-8: virtio; 9: SSD.
  # Every disk created with the VM is attached, whatever VM_DISKS is now.
  local n
  for n in $(ls "$STATE" | sed -n 's/^data\([0-9]*\)\.qcow2$/\1/p' | sort -n); do
    i=$n
    case " ${VM_DETACH:-} " in *" $i "*) echo "  (data disk $i unplugged)"; continue ;; esac
    args+=(-drive "file=$STATE/data$i.qcow2,if=none,id=data$i,format=qcow2")
    if [ "$i" -le 6 ]; then
      args+=(-device "ide-hd,drive=data$i,bus=ahci0.$((i - 1)),serial=STRATA-HDD-$i,model=StrataVirtual HDD $i,rotation_rate=7200")
    elif [ "$i" -eq 9 ]; then
      # Disk 9: an SSD (non-rotational), for the SSD cache tests.
      [ -n "${scsi:-}" ] || { args+=(-device virtio-scsi-pci,id=scsi0); scsi=1; }
      args+=(-device "scsi-hd,drive=data$i,bus=scsi0.0,serial=STRATA-SSD-1,rotation_rate=1")
    else
      args+=(-device "virtio-blk-pci,drive=data$i,serial=STRATA-VD-$i")
    fi
  done
  qemu-system-x86_64 "${args[@]}"
}

wait_ssh() {
  local timeout=${1:-300} t=0
  log "Waiting for SSH (up to ${timeout}s)"
  until ssh "${SSH_OPTS[@]}" debian@127.0.0.1 true 2>/dev/null; do
    sleep 3; t=$((t + 3))
    running || die "the VM stopped unexpectedly, see $CONSOLE"
    [ "$t" -lt "$timeout" ] || die "SSH did not come up, see $CONSOLE"
  done
}

cmd_create() {
  need
  running && die "the VM is already running"
  [ -f "$SYSDISK" ] && die "the VM already exists (vm.sh destroy first)"
  fetch_base
  mkdir -p "$STATE"
  [ -f "$KEY" ] || ssh-keygen -q -t ed25519 -N "" -C strata-vm -f "$KEY"
  log "Creating the system disk (20G, copy-on-write over the base image)"
  qemu-img create -q -f qcow2 -F qcow2 -b "$BASE" "$SYSDISK" 20G
  log "Creating $VM_DISKS data disks of $VM_DISK_SIZE"
  make_data_disks
  write_seed
  log "Serving cloud-init seed on 127.0.0.1:$SEED_PORT"
  python3 -m http.server "$SEED_PORT" --bind 127.0.0.1 --directory "$STATE/seed" >"$STATE/seed.log" 2>&1 &
  SEED_PID=$!
  trap 'kill "${SEED_PID:-}" 2>/dev/null || true' EXIT
  log "First boot"
  qemu_start
  wait_ssh 600
  log "Waiting for cloud-init to finish"
  ssh "${SSH_OPTS[@]}" debian@127.0.0.1 "sudo cloud-init status --wait >/dev/null; test -f /var/lib/strata-vm-ready" || die "cloud-init failed"
  kill "$SEED_PID" 2>/dev/null || true
  log "VM ready. Next: make vm-deploy  (or: vm.sh deploy dist/strataos_*.deb)"
}

cmd_start() {
  need
  running && { log "already running"; return; }
  [ -f "$SYSDISK" ] || die "no VM, run: vm.sh create"
  qemu_start
  wait_ssh 180
  log "VM started — SSH: vm.sh ssh — web UI: http://localhost:$VM_WEB_PORT"
}

cmd_stop() {
  running || { log "not running"; return; }
  log "Shutting down"
  if command -v socat >/dev/null && [ -S "$MONITOR" ]; then
    echo system_powerdown | socat - "UNIX-CONNECT:$MONITOR" >/dev/null
  else
    ssh "${SSH_OPTS[@]}" debian@127.0.0.1 "sudo systemctl poweroff" || true
  fi
  for _ in $(seq 1 60); do running || { log "stopped"; return; }; sleep 1; done
  log "forcing stop"; kill "$(cat "$PIDFILE")"
}

cmd_ssh() {
  running || die "the VM is not running"
  exec ssh "${SSH_OPTS[@]}" -t debian@127.0.0.1 "$@"
}

cmd_deploy() {
  local deb=${1:-}
  [ -f "$deb" ] || die "usage: vm.sh deploy <package.deb>"
  running || cmd_start
  log "Installing $(basename "$deb")"
  scp -q -i "$KEY" -P "$VM_SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR "$deb" debian@127.0.0.1:/tmp/strataos.deb
  ssh "${SSH_OPTS[@]}" debian@127.0.0.1 "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --reinstall /tmp/strataos.deb"
  log "StrataOS installed: http://localhost:$VM_WEB_PORT"
}

cmd_status() {
  if running; then echo "running (pid $(cat "$PIDFILE")) — http://localhost:$VM_WEB_PORT — ssh port $VM_SSH_PORT"; else echo "stopped"; fi
}

cmd_reset_disks() {
  running && die "stop the VM first"
  make_data_disks
  log "data disks recreated"
}

cmd_replace_disk() {
  local n=${1:-}
  [[ "$n" =~ ^[0-9]+$ ]] && [ -f "$STATE/data$n.qcow2" ] || die "usage: vm.sh replace-disk <1-$VM_DISKS>"
  running && die "stop the VM first"
  rm -f "$STATE/data$n.qcow2"
  qemu-img create -q -f qcow2 "$STATE/data$n.qcow2" "$VM_DISK_SIZE"
  log "data disk $n replaced by a blank disk (same serial STRATA-HDD-$n)"
}

cmd_destroy() {
  running && cmd_stop
  rm -rf "$STATE"
  log "VM deleted (the base image in $IMAGES is kept)"
}

case "${1:-}" in
  create) cmd_create ;;
  start) cmd_start ;;
  stop) cmd_stop ;;
  status) cmd_status ;;
  ssh) shift; cmd_ssh "$@" ;;
  deploy) shift; cmd_deploy "$@" ;;
  console) tail -f "$CONSOLE" ;;
  reset-disks) cmd_reset_disks ;;
  replace-disk) shift; cmd_replace_disk "$@" ;;
  destroy) cmd_destroy ;;
  *) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 1 ;;
esac
