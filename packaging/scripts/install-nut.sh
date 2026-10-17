#!/bin/bash
# Installs Network UPS Tools.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y nut-client nut-server
upsc -V
echo ">> NUT is ready"
