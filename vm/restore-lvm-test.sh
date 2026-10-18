#!/bin/bash
# Restore points on a system installed on LVM (like a "guided LVM" Debian):
# builds a test ISO with LVM partitioning, installs it in a VM, creates a
# point, changes the system (host name) and a StrataOS setting, rolls back
# and checks that only the system went back. Credentials are test-only.
set -uo pipefail
cd "$(dirname "$0")/.."
VERSION=$(cat VERSION)
PARTITIONING=lvm ARCH=amd64 ./packaging/iso/build-iso.sh >/dev/null || exit 1
./vm/iso-test.sh "dist/strataos-${VERSION}-amd64-lvm-test.iso" 0 bios >/dev/null || exit 1
B=http://localhost:7071 J=$(mktemp) FAILS=0
trap 'rm -f "$J"; ./vm/iso-test.sh stop' EXIT
c() { local m=$1 p=$2; shift 2; curl -s -b "$J" -c "$J" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
check() { case "$2" in *"$3"*) printf '  \033[32m✔\033[0m %s\n' "$1" ;; *) FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    expected %q, got: %s\n' "$1" "$3" "${2:0:300}" ;; esac; }
c POST /api/setup -d '{"username":"admin","password":"Strata-Test-2026","fullName":"Test","hostname":"lvm-avant"}' >/dev/null
check "LVM restore points" "$(c GET /api/system/restore-points | py 'print(d["supported"], d["mode"])')" "True lvm"
check "point created" "$(c POST /api/system/restore-points | py 'print(d["name"])')" "_manual"
c PUT /api/system/hostname -d '{"hostname":"lvm-apres"}' >/dev/null
c PUT /api/settings -d "$(c GET /api/settings | py 'd["serverString"]="Reglage-apres"; print(json.dumps(d))')" >/dev/null
RP=$(c GET /api/system/restore-points | py 'print(d["points"][0]["name"])')
check "rollback started" "$(c POST /api/system/restore-points/$RP/rollback)" '"ok":true'
sleep 30
for _ in $(seq 1 60); do curl -sf -m 3 "$B/api/setup" >/dev/null && break; sleep 5; done
check "system back to the point" "$(c GET /api/system/info | py 'print(d["hostname"])')" "lvm-avant"
check "StrataOS settings kept" "$(c GET /api/settings | py 'print(d["serverString"])')" "Reglage-apres"
[ "$FAILS" -eq 0 ]
