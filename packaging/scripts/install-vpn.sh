#!/bin/bash
# Installs WireGuard for the VPN server of StrataOS.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends wireguard-tools nftables
echo ">> WireGuard is installed"
