#!/bin/bash
# Installs Active Directory (Samba winbind + Kerberos) and LDAP (sssd) clients.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends winbind libnss-winbind libpam-winbind krb5-user samba-common-bin \
  sssd-ldap libnss-sss libpam-sss ldap-utils
# Both stay off until StrataOS joins a directory.
systemctl disable --now winbind.service sssd.service 2>/dev/null || true
echo ">> Directory clients are installed"
