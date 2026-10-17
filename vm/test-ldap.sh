#!/bin/bash
# Runs (inside the test VM) a throw-away OpenLDAP server in a Docker container:
# base dc=strata,dc=test, admin cn=admin,dc=strata,dc=test, user "paul" (uid 20001)
# in group "projets". Usage: test-ldap.sh <admin-password> — prints its IP.
set -euo pipefail
PASS=$1
docker rm -f e2e-ldap >/dev/null 2>&1 || true
docker run -d --name e2e-ldap --ulimit nofile=1024:1024 -e PASS="$PASS" debian:trixie bash -c '
  set -e
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  echo "slapd slapd/domain string strata.test" | debconf-set-selections
  echo "slapd shared/organization string Strata" | debconf-set-selections
  echo "slapd slapd/password1 password $PASS" | debconf-set-selections
  echo "slapd slapd/password2 password $PASS" | debconf-set-selections
  apt-get install -y -qq slapd ldap-utils >/dev/null
  mkdir -p /var/run/slapd && chown openldap:openldap /var/run/slapd
  slapd -h "ldap:///" -u openldap -g openldap
  sleep 2
  ldapadd -x -D cn=admin,dc=strata,dc=test -w "$PASS" >/dev/null <<LDIF
dn: ou=people,dc=strata,dc=test
objectClass: organizationalUnit
ou: people

dn: ou=groups,dc=strata,dc=test
objectClass: organizationalUnit
ou: groups

dn: uid=paul,ou=people,dc=strata,dc=test
objectClass: inetOrgPerson
objectClass: posixAccount
uid: paul
cn: Paul
sn: Test
uidNumber: 20001
gidNumber: 20001
homeDirectory: /nonexistent
loginShell: /usr/sbin/nologin
userPassword: $PASS

dn: cn=projets,ou=groups,dc=strata,dc=test
objectClass: posixGroup
cn: projets
gidNumber: 20001
memberUid: paul
LDIF
  touch /ready
  exec sleep infinity' >/dev/null
for _ in $(seq 1 100); do
  if docker exec e2e-ldap test -e /ready 2>/dev/null; then
    docker inspect -f '{{.NetworkSettings.IPAddress}}' e2e-ldap
    exit 0
  fi
  docker ps -q -f name=e2e-ldap | grep -q . || { docker logs e2e-ldap | tail -20; exit 1; }
  sleep 3
done
echo "LDAP server not ready" >&2
exit 1
