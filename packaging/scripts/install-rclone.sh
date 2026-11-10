#!/bin/bash
# Installs rclone for cloud synchronisation.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
# rclone can be missing from Debian testing for a while (it was in late 2026,
# Debian 14 forky): say so instead of apt's "no installation candidate".
if [ -z "$(apt-cache policy rclone 2>/dev/null | awk '/Candidate:/ && $2 != "(none)" { print $2 }')" ]; then
  echo ">> rclone is not available from this Debian release (Debian testing removes packages with open release-critical bugs); try again later" >&2
  exit 1
fi
apt-get install -y --no-install-recommends rclone fuse3 openssh-client
echo ">> rclone $(rclone version | head -1 | awk '{print $2}') is ready"
