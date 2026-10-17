#!/bin/bash
# Runs (inside the test VM) a throw-away Samba Active Directory domain controller
# in a Docker container: realm STRATA.TEST, user "marie" in group "Compta".
# Usage: test-dc.sh <admin-password>   — prints the DC IP address when ready.
set -euo pipefail
PASS=$1
docker rm -f e2e-dc >/dev/null 2>&1 || true
docker run -d --name e2e-dc --hostname dc1 --privileged -e PASS="$PASS" debian:trixie bash -c '
  set -e
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq samba winbind krb5-user >/dev/null
  rm -f /etc/samba/smb.conf
  samba-tool domain provision --realm=STRATA.TEST --domain=STRATA --server-role=dc \
    --dns-backend=SAMBA_INTERNAL --adminpass="$PASS" --option="dns forwarder = 10.0.2.3" >/tmp/provision.log
  samba-tool user add marie "$PASS" >/dev/null
  samba-tool group add Compta >/dev/null
  samba-tool group addmembers Compta marie >/dev/null
  touch /ready
  exec samba -i' >/dev/null
for _ in $(seq 1 120); do
  if docker exec e2e-dc test -e /ready 2>/dev/null; then
    sleep 5
    docker inspect -f '{{.NetworkSettings.IPAddress}}' e2e-dc
    exit 0
  fi
  docker ps -q -f name=e2e-dc | grep -q . || { docker logs e2e-dc | tail -20; exit 1; }
  sleep 3
done
echo "domain controller not ready" >&2
exit 1
