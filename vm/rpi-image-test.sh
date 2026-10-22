#!/bin/bash
# Checks the Raspberry Pi image: the firmware partition (kernels, device
# trees and boot configuration of every model) and the APT setup, then boots
# it in an emulated machine and checks the first boot: system partition
# grown, StrataOS answering, setup. The Raspberry Pi firmware itself cannot
# be emulated, so a kernel and initramfs are started directly:
#   (default)    generic arm64 machine (QEMU "virt"). The Raspberry Pi kernels
#                have no virtio drivers: the kernel, initramfs and modules of
#                the Debian 13 cloud image of vm.sh boot the image's system.
#   RPI_MODEL=3  emulated Raspberry Pi 3 B (1 GB, SD card, USB network) with
#                the image's own v8 kernel and initramfs; very slow.
#   RPI_MODEL=5  QEMU has no Raspberry Pi 5: the image's 2712 kernel (16K
#                pages) and its initramfs start in a "virt" machine, which has
#                none of its disk drivers, so the check ends in the initramfs
#                looking for the system partition.
# Needs qemu-system-aarch64 and qemu-img (see vm.sh), debugfs and 7z.
# Credentials: test-only.
set -uo pipefail
cd "$(dirname "$0")/.."
VERSION=$(cat VERSION)
IMG=${1:-dist/strataos-${VERSION}-raspberrypi.img.xz}
VERSION=$(basename "$IMG" | sed -n "s/^strataos-\(.*\)-raspberrypi.img.xz$/\1/p")
MODEL=${RPI_MODEL:-}
STATE=$PWD/vm/state-rpi
CLOUD=$PWD/vm/images/debian-13-generic-arm64
Q=${QEMU_ARM_DIR:-$HOME/.local/opt/qemu-arm}
QEMU=$(command -v qemu-system-aarch64 || echo "$Q/usr/bin/qemu-system-aarch64")
B=http://localhost:7120 FAILS=0
DEBUGFS=$(command -v debugfs || echo /sbin/debugfs)
[ -f "$IMG" ] || { echo "missing $IMG"; exit 1; }
command -v 7z >/dev/null || { echo "7z is required (package 7zip)"; exit 1; }
if [ -z "$MODEL" ] && { [ ! -s "$CLOUD.vmlinuz" ] || [ ! -d "$CLOUD.modules" ]; } && [ ! -f "$CLOUD.qcow2" ]; then
  echo "missing $CLOUD.qcow2 (VM_ARCH=arm64 ./vm/vm.sh create downloads it)"; exit 1
fi
[ -f "$STATE/qemu.pid" ] && kill "$(cat "$STATE/qemu.pid")" 2>/dev/null
rm -rf "$STATE"; mkdir -p "$STATE"
trap '[ -f "$STATE/qemu.pid" ] && kill "$(cat "$STATE/qemu.pid")" 2>/dev/null' EXIT
check() { case "$2" in *"$3"*) printf '  \033[32m✔\033[0m %s\n' "$1" ;; *) FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    expected %q, got: %s\n' "$1" "$3" "${2:0:300}" ;; esac; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
# Offset and size in bytes of an MBR partition: part <disk image> <1-4>.
part() { python3 -c "import struct,sys; b=open(sys.argv[1],'rb').read(512); o=446+16*(int(sys.argv[2])-1); s,n=struct.unpack('<II', b[o+8:o+16]); print(s*512, n*512)" "$1" "$2"; }
# Copies a file of the system partition: rootfile <path in the image> <copy>.
rootfile() { "$DEBUGFS" -R "dump $1 $2" "$STATE/root.ext4" >/dev/null 2>&1; [ -s "$2" ]; }

xz -dc "$IMG" > "$STATE/disk.img"
read -r fstart fsize < <(part "$STATE/disk.img" 1)
read -r rstart _ < <(part "$STATE/disk.img" 2)
dd if="$STATE/disk.img" of="$STATE/firmware.vfat" bs=1M iflag=skip_bytes,count_bytes skip="$fstart" count="$fsize" status=none
dd if="$STATE/disk.img" of="$STATE/root.ext4" bs=1M iflag=skip_bytes skip="$rstart" status=none

echo "firmware partition"
files=$(7z l -ba "$STATE/firmware.vfat" 2>/dev/null | awk '{print $NF}')
for f in bootcode.bin start.elf start4.elf kernel8.img initramfs8 kernel_2712.img initramfs_2712 \
  bcm2710-rpi-3-b.dtb bcm2711-rpi-4-b.dtb bcm2712-rpi-5-b.dtb bcm2712d0-rpi-5-b.dtb bcm2712-rpi-500.dtb overlays/vc4-kms-v3d-pi5.dtbo; do
  check "$f" "$(grep -x "$f" <<<"$files")" "$f"
done
7z e -so "$STATE/firmware.vfat" config.txt > "$STATE/config.txt" 2>/dev/null
7z e -so "$STATE/firmware.vfat" cmdline.txt > "$STATE/cmdline.txt" 2>/dev/null
check "config.txt: 64-bit kernels with their initramfs" "$(tr '\n' ' ' < "$STATE/config.txt")" "arm_64bit=1 auto_initramfs=1"
check "config.txt: Pi 5 kernel" "$(sed -n '/^\[pi5\]/,/^\[all\]/p' "$STATE/config.txt")" "kernel=kernel_2712.img"
check "cmdline.txt: system partition" "$(cat "$STATE/cmdline.txt")" "root=LABEL=RASPIROOT"
echo "system partition"
check "Raspberry Pi archive pinned" "$("$DEBUGFS" -R "cat /etc/apt/preferences.d/raspberrypi" "$STATE/root.ext4" 2>/dev/null | tr '\n' ' ')" "Pin: origin archive.raspberrypi.com Pin-Priority: -1"
sources=$("$DEBUGFS" -R "ls /etc/apt/sources.list.d" "$STATE/root.ext4" 2>/dev/null)
check "Raspberry Pi archive source" "$sources" "raspberrypi.sources"
check "StrataOS updates source" "$sources" "strataos.sources"
kernels=$("$DEBUGFS" -R "ls -p /boot" "$STATE/root.ext4" 2>/dev/null | tr '/' '\n' | grep '^vmlinuz-')
v8=$(grep -- '-rpi-v8$' <<<"$kernels" | sort -V | tail -1)
k2712=$(grep -- '-rpi-2712$' <<<"$kernels" | sort -V | tail -1)
check "kernels v8 and 2712" "$v8 $k2712" "-rpi-v8 vmlinuz-"

case "$MODEL" in
3)
  ver=${v8#vmlinuz-}
  { rootfile "/boot/$v8" "$STATE/vmlinuz" && rootfile "/boot/initrd.img-$ver" "$STATE/initrd" &&
    rootfile "/usr/lib/linux-image-$ver/broadcom/bcm2710-rpi-3-b.dtb" "$STATE/rpi3.dtb"; } || { echo "no Raspberry Pi 3 kernel in the image"; exit 1; }
  ;;
5)
  ver=${k2712#vmlinuz-}
  { rootfile "/boot/$k2712" "$STATE/vmlinuz" && rootfile "/boot/initrd.img-$ver" "$STATE/initrd"; } || { echo "no Raspberry Pi 5 kernel in the image"; exit 1; }
  ;;
*)
  # Kernel and initramfs of the Debian cloud image, extracted once.
  if [ ! -s "$CLOUD.vmlinuz" ] || [ ! -s "$CLOUD.initrd" ] || [ ! -d "$CLOUD.modules" ]; then
    qemu-img convert -O raw "$CLOUD.qcow2" "$STATE/cloud.raw" || exit 1
    # The system is its largest GPT partition.
    read -r cstart csize < <(python3 -c "
import struct,sys
f=open(sys.argv[1],'rb'); f.seek(512); h=f.read(92); lba,n,sz=struct.unpack('<QII', h[72:88])
f.seek(lba*512); best=(0,0)
for i in range(n):
    e=f.read(sz); first,last=struct.unpack('<QQ', e[32:48])
    if last and last-first+1 > best[1]: best=(first, last-first+1)
print(best[0]*512, best[1]*512)" "$STATE/cloud.raw")
    dd if="$STATE/cloud.raw" of="$STATE/cloud.ext4" bs=1M iflag=skip_bytes,count_bytes skip="$cstart" count="$csize" status=none
    ck=$("$DEBUGFS" -R "ls -p /boot" "$STATE/cloud.ext4" 2>/dev/null | tr '/' '\n' | grep '^vmlinuz-' | sort -V | tail -1)
    "$DEBUGFS" -R "dump /boot/$ck $CLOUD.vmlinuz" "$STATE/cloud.ext4" >/dev/null 2>&1
    "$DEBUGFS" -R "dump /boot/initrd.img-${ck#vmlinuz-} $CLOUD.initrd" "$STATE/cloud.ext4" >/dev/null 2>&1
    rm -rf "$CLOUD.modules"; mkdir -p "$CLOUD.modules"
    "$DEBUGFS" -R "rdump /usr/lib/modules/${ck#vmlinuz-} $CLOUD.modules" "$STATE/cloud.ext4" >/dev/null 2>&1
    rm -f "$STATE/cloud.raw" "$STATE/cloud.ext4"
  fi
  cp "$CLOUD.vmlinuz" "$STATE/vmlinuz"; cp "$CLOUD.initrd" "$STATE/initrd"
  # Its modules too (vfat for the firmware partition, nfsd...), written into
  # the copy of the system partition.
  (cd "$CLOUD.modules" && find . -mindepth 1 -type d -printf 'mkdir /usr/lib/modules/%P\n' &&
    find . -type f -printf "write $CLOUD.modules/%P /usr/lib/modules/%P\n") > "$STATE/modules.cmd"
  "$DEBUGFS" -w -f "$STATE/modules.cmd" "$STATE/root.ext4" >/dev/null 2>&1
  dd if="$STATE/root.ext4" of="$STATE/disk.img" bs=1M oflag=seek_bytes seek="$rstart" conv=notrunc status=none
  ;;
esac
rm -f "$STATE/root.ext4" "$STATE/firmware.vfat"
[ -s "$STATE/vmlinuz" ] && [ -s "$STATE/initrd" ] || { echo "no kernel to boot"; exit 1; }

if [ "$MODEL" = 5 ]; then
  echo "Raspberry Pi 5 kernel in a generic arm64 machine (emulated)"
  "$QEMU" -machine virt -cpu max -smp 4 -m 2048 -kernel "$STATE/vmlinuz" -initrd "$STATE/initrd" \
    -append "root=LABEL=RASPIROOT rootdelay=10 console=ttyAMA0" \
    -display none -serial "file:$STATE/console.log" -pidfile "$STATE/qemu.pid" -daemonize
  for _ in $(seq 1 120); do grep -q "Dropping to a shell" "$STATE/console.log" 2>/dev/null && break; sleep 5; done
  log=$(tr -d '\r' < "$STATE/console.log")
  check "Raspberry Pi 5 kernel started" "$(grep -m1 'Linux version' <<<"$log")" "Linux version $ver"
  check "initramfs running, looking for the system partition" "$log" "LABEL=RASPIROOT does not exist"
  [ "$FAILS" -eq 0 ]
  exit
fi

# A bigger "card": the first boot must grow the system partition.
truncate -s 8G "$STATE/disk.img"
NET="user,id=n0,hostfwd=tcp:127.0.0.1:7120-:7070,hostfwd=tcp:127.0.0.1:2272-:22"
if [ "$MODEL" = 3 ]; then
  # The emulated board has no Ethernet chip: a USB network adapter (usb0).
  # The Raspberry Pi kernel names the PL011 serial port ttyAMA1.
  "$QEMU" -machine raspi3b -m 1G -kernel "$STATE/vmlinuz" -initrd "$STATE/initrd" -dtb "$STATE/rpi3.dtb" \
    -append "root=LABEL=RASPIROOT rw rootwait console=ttyAMA1,115200 net.ifnames=0" \
    -drive "file=$STATE/disk.img,format=raw,if=sd" -netdev "$NET" -device usb-net,netdev=n0 \
    -L "$(dirname "$QEMU")/../share/qemu" -display none -serial "file:$STATE/console.log" -pidfile "$STATE/qemu.pid" -daemonize
else
  "$QEMU" -machine virt -cpu max -smp 4 -m 2048 -kernel "$STATE/vmlinuz" -initrd "$STATE/initrd" \
    -append "root=LABEL=RASPIROOT rw console=ttyAMA0" \
    -drive "file=$STATE/disk.img,format=raw,if=virtio" \
    -netdev "$NET" -device virtio-net-pci,netdev=n0 \
    -display none -serial "file:$STATE/console.log" -pidfile "$STATE/qemu.pid" -daemonize
fi
echo "booting $(basename "$IMG") (emulated, slow)"
for _ in $(seq 1 720); do curl -sf -m 3 "$B/api/setup" >/dev/null && break; sleep 10; done
check "StrataOS answers" "$(curl -s $B/api/setup)" "\"version\":\"$VERSION\""
J=$(mktemp)
check "setup wizard" "$(curl -s -c "$J" -X POST -H 'X-Strata: 1' -H 'Content-Type: application/json' $B/api/setup -d '{"username":"admin","password":"Strata-Test-2026","fullName":"Test","hostname":"strata-pi"}')" '"role":"admin"'
info=$(curl -s -b "$J" -H 'X-Strata: 1' $B/api/system/info)
check "system partition grown to the card" "$(py 'print(int(d.get("rootTotal", 0) > 6e9))' <<<"$info" 2>/dev/null || echo unknown)" "1"
[ "$MODEL" = 3 ] && check "Raspberry Pi kernel" "$(py 'print(d.get("kernel", ""))' <<<"$info" 2>/dev/null)" "-rpi-v8"
rm -f "$J"
[ "$FAILS" -eq 0 ]
