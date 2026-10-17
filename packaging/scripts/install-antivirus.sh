#!/bin/bash
# Installs ClamAV (on-demand scanner) and keeps its signatures up to date.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends clamav clamav-freshclam
systemctl enable clamav-freshclam.service
# First download of the signatures (the service keeps them fresh afterwards).
systemctl stop clamav-freshclam.service || true
freshclam --stdout || echo ">> warning: signatures not downloaded yet, freshclam will retry"
systemctl start clamav-freshclam.service
echo ">> ClamAV is ready"
