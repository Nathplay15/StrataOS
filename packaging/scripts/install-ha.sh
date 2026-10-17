#!/bin/bash
# Installs keepalived (shared virtual address) and rsync for high availability.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends keepalived rsync python3 openssh-server
# Stays off until StrataOS pairs this NAS.
systemctl disable --now keepalived.service || true
echo ">> High availability components are installed"
