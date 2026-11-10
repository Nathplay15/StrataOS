#!/bin/bash
# Runs (inside the test VM) a throw-away OpenLDAP server in a Docker container:
# base dc=strata,dc=test, admin cn=admin,dc=strata,dc=test, user "paul" (uid 20001)
# in group "projets", user "lea" (uid 20002) in groups "projets" and "nasadmins".
# It also answers on ldaps:// (636) with a certificate of a throw-away CA,
# readable with: docker exec e2e-ldap cat /ca.pem
# Usage: test-ldap.sh <admin-password> — prints its IP.
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
  apt-get install -y -qq slapd ldap-utils openssl >/dev/null
  mkdir -p /var/run/slapd && chown openldap:openldap /var/run/slapd
  IP=$(hostname -I | cut -d" " -f1)
  cd /etc/ldap
  openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=Strata test CA" -keyout ca.key -out /ca.pem 2>/dev/null
  openssl req -newkey rsa:2048 -nodes -subj "/CN=$IP" -keyout server.key -out server.csr 2>/dev/null
  printf "subjectAltName=IP:%s\n" "$IP" > san.ext
  openssl x509 -req -in server.csr -CA /ca.pem -CAkey ca.key -CAcreateserial -days 2 -extfile san.ext -out server.pem 2>/dev/null
  chown openldap:openldap server.key server.pem
  slapd -h "ldap:/// ldapi:///" -u openldap -g openldap
  sleep 2
  ldapmodify -Y EXTERNAL -H ldapi:/// >/dev/null 2>&1 <<TLS
dn: cn=config
changetype: modify
replace: olcTLSCACertificateFile
olcTLSCACertificateFile: /ca.pem
-
replace: olcTLSCertificateFile
olcTLSCertificateFile: /etc/ldap/server.pem
-
replace: olcTLSCertificateKeyFile
olcTLSCertificateKeyFile: /etc/ldap/server.key
TLS
  kill "$(cat /var/run/slapd/slapd.pid)"; sleep 2
  slapd -h "ldap:/// ldaps:/// ldapi:///" -u openldap -g openldap
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

dn: uid=lea,ou=people,dc=strata,dc=test
objectClass: inetOrgPerson
objectClass: posixAccount
uid: lea
cn: Lea
sn: Test
uidNumber: 20002
gidNumber: 20001
homeDirectory: /nonexistent
loginShell: /usr/sbin/nologin
userPassword: $PASS

dn: cn=projets,ou=groups,dc=strata,dc=test
objectClass: posixGroup
cn: projets
gidNumber: 20001
memberUid: paul
memberUid: lea

dn: cn=nasadmins,ou=groups,dc=strata,dc=test
objectClass: posixGroup
cn: nasadmins
gidNumber: 20010
memberUid: lea
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
