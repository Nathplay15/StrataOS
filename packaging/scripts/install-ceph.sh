#!/bin/bash
# Installs Ceph from Debian (monitors, managers, OSDs, metadata servers and
# the client tools), driven by StrataOS through the systemd units of the
# packages: no cephadm, no containers.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ceph-mon ceph-mgr ceph-osd ceph-mds ceph-volume ceph-common lvm2 gdisk
# VM disks on Ceph: the RBD drivers of libvirt and QEMU (when virtualisation is installed).
if dpkg -s libvirt-daemon-system >/dev/null 2>&1; then
  apt-get install -y --no-install-recommends libvirt-daemon-driver-storage-rbd qemu-block-extra
  systemctl try-restart libvirtd.service || true
fi
# Nothing runs until this NAS joins or creates a Ceph cluster.
systemctl disable --now ceph-crash.service 2>/dev/null || true
install -d -m 700 /etc/ceph/strata
echo ">> Ceph $(ceph --version | awk '{print $3}') is installed"
