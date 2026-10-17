#!/bin/bash
# Installs rclone for cloud synchronisation.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends rclone fuse3 openssh-client
echo ">> rclone $(rclone version | head -1 | awk '{print $2}') is ready"
