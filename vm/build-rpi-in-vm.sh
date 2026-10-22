#!/bin/bash
# Builds the Raspberry Pi image without root on this machine: inside an
# emulated arm64 VM (see vm.sh VM_ARCH=arm64), where the build runs natively.
# Slow (about an hour). In CI, packaging/rpi/build-rpi-image.sh runs directly.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION=$(cat VERSION)
DEB=dist/strataos_${VERSION}_arm64.deb
[ -f "$DEB" ] || { echo "missing $DEB (make deb ARCH=arm64)"; exit 1; }
export VM_ARCH=arm64 VM_STATE=$PWD/vm/state-rpibuild VM_HOSTNAME=rpi-build VM_WEB_PORT=7130 VM_SSH_PORT=2282 \
  VM_HTTPS_PORT=7493 VM_SMB_PORT=4500 SEED_PORT=8769 VM_EXTRA_PORTS="" VM_DISKS=0 VM_CPUS=4 VM_MEM=3072
./vm/vm.sh destroy >/dev/null 2>&1 || true
./vm/vm.sh create
KEY=$VM_STATE/id_ed25519
SSH=(-i "$KEY" -P "$VM_SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR)
./vm/vm.sh ssh "mkdir -p /var/tmp/sos/packaging/rpi /var/tmp/sos/dist && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq debootstrap parted dosfstools xz-utils debian-archive-keyring >/dev/null"
scp -q "${SSH[@]}" VERSION debian@127.0.0.1:/var/tmp/sos/
scp -q "${SSH[@]}" packaging/rpi/build-rpi-image.sh packaging/rpi/raspberrypi-archive-keyring.pgp debian@127.0.0.1:/var/tmp/sos/packaging/rpi/
scp -q "${SSH[@]}" "$DEB" debian@127.0.0.1:/var/tmp/sos/dist/
./vm/vm.sh ssh "cd /var/tmp/sos && sudo ./packaging/rpi/build-rpi-image.sh"
scp -q "${SSH[@]}" "debian@127.0.0.1:/var/tmp/sos/dist/strataos-${VERSION}-raspberrypi.img.xz" dist/
./vm/vm.sh destroy >/dev/null 2>&1 || true
ls -lh "dist/strataos-${VERSION}-raspberrypi.img.xz"
