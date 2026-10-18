#!/bin/bash
# Boots the Raspberry Pi image in an emulated arm64 machine (QEMU "virt": the
# Raspberry Pi firmware itself cannot be emulated, so the kernel and
# initramfs of the image are started directly) and checks the first boot:
# system partition grown, SSH keys generated, StrataOS answering, setup.
# Needs qemu-system-aarch64 (see vm.sh) and debugfs. Credentials: test-only.
set -uo pipefail
cd "$(dirname "$0")/.."
VERSION=$(cat VERSION)
IMG=${1:-dist/strataos-${VERSION}-raspberrypi.img.xz}
STATE=$PWD/vm/state-rpi
Q=${QEMU_ARM_DIR:-$HOME/.local/opt/qemu-arm}
QEMU=$(command -v qemu-system-aarch64 || echo "$Q/usr/bin/qemu-system-aarch64")
B=http://localhost:7120 FAILS=0
[ -f "$IMG" ] || { echo "missing $IMG"; exit 1; }
[ -f "$STATE/qemu.pid" ] && kill "$(cat "$STATE/qemu.pid")" 2>/dev/null
rm -rf "$STATE"; mkdir -p "$STATE"
trap '[ -f "$STATE/qemu.pid" ] && kill "$(cat "$STATE/qemu.pid")" 2>/dev/null' EXIT
check() { case "$2" in *"$3"*) printf '  \033[32m✔\033[0m %s\n' "$1" ;; *) FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    expected %q, got: %s\n' "$1" "$3" "${2:0:300}" ;; esac; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }

xz -dc "$IMG" > "$STATE/disk.img"
# Kernel and initramfs from the system partition (ext4, read without root).
start=$(python3 -c "import struct,sys; b=open(sys.argv[1],'rb').read(512); print(struct.unpack('<I', b[446+16+8:446+16+12])[0]*512)" "$STATE/disk.img")
dd if="$STATE/disk.img" of="$STATE/root.ext4" bs=1M iflag=skip_bytes skip="$start" status=none
kernel=$(debugfs -R "ls -p /boot" "$STATE/root.ext4" 2>/dev/null | tr '/' '\n' | grep '^vmlinuz-' | sort -V | tail -1)
debugfs -R "dump /boot/$kernel $STATE/vmlinuz" "$STATE/root.ext4" >/dev/null 2>&1
debugfs -R "dump /boot/initrd.img-${kernel#vmlinuz-} $STATE/initrd" "$STATE/root.ext4" >/dev/null 2>&1
rm -f "$STATE/root.ext4"
[ -s "$STATE/vmlinuz" ] && [ -s "$STATE/initrd" ] || { echo "kernel not found in the image"; exit 1; }
# A bigger "card": the first boot must grow the system partition.
truncate -s 8G "$STATE/disk.img"
"$QEMU" -machine virt -cpu max -smp 4 -m 2048 -kernel "$STATE/vmlinuz" -initrd "$STATE/initrd" \
  -append "root=LABEL=RASPIROOT rw console=ttyAMA0" \
  -drive "file=$STATE/disk.img,format=raw,if=virtio" \
  -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:7120-:7070,hostfwd=tcp:127.0.0.1:2272-:22" -device virtio-net-pci,netdev=n0 \
  -display none -serial "file:$STATE/console.log" -pidfile "$STATE/qemu.pid" -daemonize
echo "booting $(basename "$IMG") (emulated, slow)"
for _ in $(seq 1 360); do curl -sf -m 3 "$B/api/setup" >/dev/null && break; sleep 10; done
check "StrataOS answers" "$(curl -s $B/api/setup)" "\"version\":\"$VERSION\""
check "first boot finished" "$(grep -a -c 'StrataOS first boot' "$STATE/console.log")" "1"
J=$(mktemp)
check "setup wizard" "$(curl -s -c "$J" -X POST -H 'X-Strata: 1' -H 'Content-Type: application/json' $B/api/setup -d '{"username":"nathan","password":"Strata-Test-2026","fullName":"Test","hostname":"strata-pi"}')" '"role":"admin"'
check "system partition grown to the card" "$(curl -s -b "$J" -H 'X-Strata: 1' $B/api/system/info | py 'print(int(d.get("rootTotal", 0) > 6e9))' 2>/dev/null || echo unknown)" "1"
rm -f "$J"
[ "$FAILS" -eq 0 ]
