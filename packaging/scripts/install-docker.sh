#!/bin/bash
# Installs Docker Engine and Compose from the Debian archive.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y docker.io docker-compose
systemctl enable --now docker.service
docker version
docker compose version || docker-compose version
echo ">> Docker is ready"
