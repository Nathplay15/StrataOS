#!/bin/bash
# Smart photos on one fresh VM (3 GB, 4 CPUs, one 10 GB data disk), without
# the whole end-to-end suite: setup, a volume "main", the share Photos and
# the user alice (group famille) like e2e-test.sh, then the section
# e2e.d/96c-smart-photos.sh. About 20 minutes (downloads ~300 MB of packages
# and models). Ports and state folder come from the usual VM_* variables.
# Usage: make deb && flock -o /home/nathan/Documents/StrataOS-wt/.vm.lock timeout 90m ./vm/smart-test.sh
#   (KEEP=1 keeps the VM; REUSE=1 runs the sections on a VM already set up)
set -uo pipefail
cd "$(dirname "$0")/.."
DEB=${DEB:-dist/strataos_$(cat VERSION)_amd64.deb}
export VM_MEM=${VM_MEM:-3072} VM_CPUS=${VM_CPUS:-4} VM_DISKS=${VM_DISKS:-1} VM_DISK_SIZE=${VM_DISK_SIZE:-10G}
B="http://localhost:${VM_WEB_PORT:-7070}"
ADMIN_USER=admin ADMIN_PASS=Strata-Test-2026
USER_NAME=alice USER_PASS=Alice-Pass-1
J=$(mktemp) J2=$(mktemp) TMP=$(mktemp -d)
FAILS=0 PASSES=0
c() { local m=$1 p=$2; shift 2; curl -s -m 120 -b "$J" -c "$J" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
u() { local m=$1 p=$2; shift 2; curl -s -m 120 -b "$J2" -c "$J2" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
ok() { PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1"; }
ko() { FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    %s\n' "$1" "${2:-}"; }
check() { case "$2" in *"$3"*) ok "$1" ;; *) ko "$1" "expected '$3', got: ${2:0:300}" ;; esac; }
vm() { ./vm/vm.sh ssh "$@" 2>/dev/null; }
job() {
  local id; id=$(echo "$1" | py 'print(d.get("id",""))' 2>/dev/null)
  [ -n "$id" ] || { echo "no-job: $1"; return; }
  for _ in $(seq 1 600); do [ "$(c GET "/api/jobs/$id" | py 'print(d["status"])')" != running ] && break; sleep 2; done
  c GET "/api/jobs/$id" | py 'print(d["status"], d.get("error","")); print(d.get("log","")[-1500:]) if d["status"]!="success" else None'
}
section() { printf '\n\033[1;36m== %s\033[0m\n' "$1"; }
cleanup() { rm -rf "$J" "$J2" "$TMP"; [ -n "${KEEP:-}" ] || ./vm/vm.sh destroy >/dev/null 2>&1; }
trap cleanup EXIT

if [ -z "${REUSE:-}" ]; then
  section "VM"
  ./vm/vm.sh destroy >/dev/null 2>&1
  ./vm/vm.sh create >"$TMP/create.log" 2>&1 && ./vm/vm.sh deploy "$DEB" >>"$TMP/create.log" 2>&1 || { tail -30 "$TMP/create.log"; exit 1; }
  for _ in $(seq 1 90); do curl -sf "$B/api/setup" >/dev/null && break; sleep 2; done
  c POST /api/setup -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"fullName\":\"Test\",\"hostname\":\"smart\"}" >/dev/null
  c POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null
  D=$(c GET /api/storage/disks | py 'print([x["path"] for x in d if x["usage"]=="free"][0])')
  check "volume main" "$(job "$(c POST /api/storage/volumes -d "{\"name\":\"main\",\"fsType\":\"ext4\",\"devices\":[\"$D\"]}")")" "success"
  c POST /api/groups -d '{"name":"famille"}' >/dev/null
  check "user" "$(c POST /api/users -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\",\"role\":\"user\",\"groups\":[\"famille\"]}")" '"ok":true'
  check "share Photos" "$(c POST /api/shares -d '{"name":"Photos","path":"/srv/strata/main/Photos","smb":true,"permissions":{"@famille":"rw"}}')" '"name":"Photos"'
fi
c POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null
u POST /api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}" >/dev/null
P=/srv/strata/main/Photos
. vm/e2e.d/96c-smart-photos.sh

if [ "$FAILS" -gt 0 ]; then vm 'sudo journalctl -u strata --no-pager -n 60 -o cat; free -m'; fi
printf '\n%d passed, %d failed\n' "$PASSES" "$FAILS"
[ "$FAILS" -eq 0 ]
