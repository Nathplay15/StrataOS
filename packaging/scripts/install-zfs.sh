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
# linux-headers-<arch> follows kernel upgrades, so DKMS rebuilds ZFS for every new kernel.
pkgs="linux-headers-$(dpkg --print-architecture) dpkg-dev"
for k in /lib/modules/*; do
  v=$(basename "$k")
  apt-cache show "linux-headers-$v" >/dev/null 2>&1 && pkgs="$pkgs linux-headers-$v"
done
apt-get install -y $pkgs
apt-get install -y zfs-dkms zfsutils-linux zfs-zed

echo ">> Building the module for every installed kernel"
for k in /lib/modules/*; do
  v=$(basename "$k")
  [ -d "/usr/src/linux-headers-$v" ] && dkms autoinstall -k "$v" || true
done

echo ">> Loading the module"
modprobe zfs
systemctl enable --now zfs-import-cache.service zfs-mount.service zfs-zed.service zfs.target 2>/dev/null || true
zfs version
echo ">> Importing existing pools"
zpool import -a 2>/dev/null || true
zfs mount -a 2>/dev/null || true
zpool list
echo ">> ZFS is ready"
