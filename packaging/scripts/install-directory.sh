#!/bin/bash
# Installs Active Directory (Samba winbind + Kerberos) and LDAP (sssd) clients.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends winbind libnss-winbind libpam-winbind krb5-user samba-common-bin ldap-utils
# sssd can be missing from Debian testing for a while (it was in late 2026,
# Debian 14 forky): Active Directory still works, LDAP waits for it.
if [ -n "$(apt-cache policy sssd-ldap 2>/dev/null | awk '/Candidate:/ && $2 != "(none)" { print $2 }')" ]; then
  apt-get install -y --no-install-recommends sssd-ldap libnss-sss libpam-sss
else
  echo ">> warning: sssd is not available from this Debian release: LDAP directories cannot be joined (Active Directory can)"
fi
# Both stay off until StrataOS joins a directory.
systemctl disable --now winbind.service sssd.service 2>/dev/null || true
echo ">> Directory clients are installed"
