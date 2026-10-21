#!/bin/bash
# Installs the tools of the kernel iSCSI target (LIO): targetcli-fb, which
# brings python3-rtslib-fb and its rtslib-fb-targetctl service. StrataOS writes
# the target configuration itself; the service restores it at boot.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends targetcli-fb
if ! modprobe iscsi_target_mod; then
  echo "!! this kernel has no iSCSI target module (iscsi_target_mod)" >&2
  exit 1
fi
install -d -m 700 /etc/rtslib-fb-target
systemctl enable rtslib-fb-targetctl.service || true
echo ">> iSCSI target tools are installed"
