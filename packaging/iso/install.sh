#!/bin/bash
# Runs inside the freshly installed system (preseed late_command).
exec > >(tee -a /var/log/strataos-install.log) 2>&1
set -eux
export DEBIAN_FRONTEND=noninteractive
# The installation CD is not mounted inside the target: its APT entry would make
# apt-get update fail.
sed -i -E 's/^(deb(-src)? cdrom:)/# \1/' /etc/apt/sources.list
apt-get update
apt-get install -y /root/strataos/strataos_*.deb
# Nobody knows root's install-time password: lock it for good.
passwd -l root
# The login screen of the machine shows where to open the web interface
# (\4 is replaced by agetty with the IPv4 address).
cat > /etc/issue <<'ISSUE'

  StrataOS — \n

  Web interface: http://\4:7070   (HTTPS: https://\4:7443)
  Create the administrator account from your browser.

ISSUE
cp /etc/issue /etc/issue.net
rm -rf /root/strataos
