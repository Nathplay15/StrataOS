#!/bin/bash
# Installs OpenZFS from Debian contrib (DKMS module built for the running kernel).
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo ">> Enabling the contrib component"
if [ -f /etc/apt/sources.list.d/debian.sources ]; then
  sed -i -E '/^Components:/ { /contrib/! s/$/ contrib/ }' /etc/apt/sources.list.d/debian.sources
fi
if [ -f /etc/apt/sources.list ]; then
  sed -i -E '/^deb .*debian/ { / contrib/! s/ main/ main contrib/ }' /etc/apt/sources.list
fi

echo ">> Updating package lists"
apt-get update

echo ">> Installing kernel headers and ZFS (the module build can take several minutes)"
# linux-headers-<arch> follows kernel upgrades, so DKMS rebuilds ZFS for every
# new kernel; linux-headers-rpi-<flavour> with the Raspberry Pi kernels.
pkgs="dpkg-dev"
metas=""
for meta in "$(dpkg --print-architecture)" rpi-v8 rpi-2712; do
  dpkg-query -W -f '${Status}' "linux-image-$meta" 2>/dev/null | grep -q " installed$" && metas="$metas $meta"
done
[ -n "$metas" ] || metas=$(dpkg --print-architecture)
for meta in $metas; do pkgs="$pkgs linux-headers-$meta"; done
for k in /lib/modules/*; do
  v=$(basename "$k")
  apt-cache show "linux-headers-$v" >/dev/null 2>&1 && pkgs="$pkgs linux-headers-$v"
done
# The headers of the running kernel are gone from the archive when a newer
# kernel replaced it (Debian testing, or a stable point release): the module
# can then only be built for that newer kernel, so install it too.
running=$(uname -r)
if ! apt-cache show "linux-headers-$running" >/dev/null 2>&1 && [ ! -d "/usr/src/linux-headers-$running" ]; then
  echo ">> No headers for the running kernel $running any more: installing the current kernel"
  for meta in $metas; do pkgs="$pkgs linux-image-$meta"; done
fi
apt-get install -y $pkgs
apt-get install -y zfs-dkms zfsutils-linux zfs-zed

echo ">> Building the module for every installed kernel"
for k in /lib/modules/*; do
  v=$(basename "$k")
  [ -d "/usr/src/linux-headers-$v" ] && dkms autoinstall -k "$v" || true
done

echo ">> Loading the module"
if ! modprobe zfs; then
  if [ ! -e "/lib/modules/$running/updates/dkms/zfs.ko" ] && ls /lib/modules/*/updates/dkms/zfs.ko* >/dev/null 2>&1; then
    systemctl enable zfs-import-cache.service zfs-mount.service zfs-zed.service zfs.target 2>/dev/null || true
    echo ">> ZFS is built for a newer kernel than the running one ($running): restart the NAS to use it" >&2
  fi
  exit 1
fi
systemctl enable --now zfs-import-cache.service zfs-mount.service zfs-zed.service zfs.target 2>/dev/null || true
zfs version
echo ">> Importing existing pools"
zpool import -a 2>/dev/null || true
zfs mount -a 2>/dev/null || true
zpool list
echo ">> ZFS is ready"
