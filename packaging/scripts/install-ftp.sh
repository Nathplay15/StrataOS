#!/bin/bash
# Installs the FTP server (vsftpd); StrataOS writes its configuration.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends vsftpd
# Stays stopped until FTP is enabled in StrataOS.
systemctl disable --now vsftpd.service || true
echo ">> vsftpd is installed"
