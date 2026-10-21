#!/bin/bash
# Installs the DLNA media server (ReadyMedia, formerly MiniDLNA). StrataOS runs
# it through its own service (strata-minidlna) with its own configuration.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends minidlna
# The service of the package stays off: it would publish /var/lib/minidlna.
systemctl disable --now minidlna.service || true
echo ">> ReadyMedia (minidlna) is installed"
