#!/bin/bash
# Installs StrataOS from its ISO in a blank QEMU VM, as on a real machine.
#
#   vm/iso-test.sh <strataos.iso> [extra-data-disks] [uefi]
#   vm/iso-test.sh screenshot <file.png>   capture the VM screen
#   vm/iso-test.sh key <keys...>           type keys (QEMU sendkey names, e.g. ret down)
#   vm/iso-test.sh stop
#
# The web interface of the installed system is forwarded to http://localhost:7071
# and SSH to port 2223 (the regular test VM keeps 7070/2222).
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
STATE="$DIR/state-iso"
MON="$STATE/monitor.sock"
PID="$STATE/qemu.pid"

mon() { echo "$*" | socat - "UNIX-CONNECT:$MON" >/dev/null; }

case "${1:-}" in
  screenshot)
    out=${2:-$STATE/screen.png}
    mon "screendump $STATE/screen.ppm"
    sleep 0.5
    python3 -I - "$STATE/screen.ppm" "$out" <<'PY'
import sys, zlib, struct
data = open(sys.argv[1], "rb").read()
# Parse the binary PPM written by QEMU (P6 width height maxval).
parts, i = [], 0
while len(parts) < 4:
    while data[i:i+1].isspace(): i += 1
    if data[i:i+1] == b"#":
        while data[i:i+1] != b"\n": i += 1
        continue
    j = i
    while not data[j:j+1].isspace(): j += 1
    parts.append(data[i:j]); i = j
i += 1
w, h = int(parts[1]), int(parts[2])
pix = data[i:i + w * h * 3]
raw = b"".join(b"\x00" + pix[y * w * 3:(y + 1) * w * 3] for y in range(h))
chunk = lambda t, d: struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d) & 0xffffffff)
png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(raw, 6)) + chunk(b"IEND", b"")
open(sys.argv[2], "wb").write(png)
PY
    echo "$out"; exit 0 ;;
  key)
    shift
    for k in "$@"; do mon "sendkey $k"; sleep 0.2; done
    exit 0 ;;
  stop)
    [ -f "$PID" ] && kill "$(cat "$PID")" 2>/dev/null || true
    exit 0 ;;
esac

ISO=$(realpath "${1:?usage: iso-test.sh <strataos.iso> [extra-disks] [uefi]}")
EXTRA=${2:-0}
MODE=${3:-bios}
[ -f "$PID" ] && kill "$(cat "$PID")" 2>/dev/null || true
rm -rf "$STATE"; mkdir -p "$STATE"
qemu-img create -q -f qcow2 "$STATE/system.qcow2" 16G
args=(-name strataos-iso -machine q35,accel=kvm -cpu host -smp 4 -m 4096
  -drive "file=$STATE/system.qcow2,if=none,id=sys,format=qcow2" -device ahci,id=ahci0
  -device "ide-hd,drive=sys,bus=ahci0.0,serial=STRATA-SYS,model=StrataVirtual SSD,rotation_rate=1"
  -drive "file=$ISO,media=cdrom,if=none,id=cd" -device "ide-cd,drive=cd,bus=ahci0.5"
  -boot order=c,once=d
  -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:7071-:7070,hostfwd=tcp:127.0.0.1:2223-:22" -device virtio-net-pci,netdev=n0
  -display none -vga std -serial "file:$STATE/console.log"
  -monitor "unix:$MON,server,nowait" -pidfile "$PID" -daemonize)
for i in $(seq 1 "$EXTRA"); do
  qemu-img create -q -f qcow2 "$STATE/data$i.qcow2" 10G
  args+=(-drive "file=$STATE/data$i.qcow2,if=none,id=d$i,format=qcow2" -device "ide-hd,drive=d$i,bus=ahci0.$i,serial=STRATA-DATA-$i,model=StrataVirtual HDD $i")
done
if [ "$MODE" = uefi ]; then
  cp /usr/share/OVMF/OVMF_VARS_4M.fd "$STATE/vars.fd"
  args+=(-drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd -drive "if=pflash,format=raw,file=$STATE/vars.fd")
fi
qemu-system-x86_64 "${args[@]}"
echo "installing from $(basename "$ISO") ($MODE, $EXTRA extra disk(s)); screen: vm/iso-test.sh screenshot"
start=$SECONDS
until curl -sf -m 3 http://localhost:7071/api/setup >/dev/null; do
  sleep 15
  kill -0 "$(cat "$PID")" 2>/dev/null || { echo "VM stopped"; exit 1; }
  [ $((SECONDS - start)) -lt 3600 ] || { echo "timeout"; exit 1; }
done
echo "StrataOS answers on http://localhost:7071 after $(( (SECONDS - start) / 60 )) min"
curl -s http://localhost:7071/api/setup; echo
