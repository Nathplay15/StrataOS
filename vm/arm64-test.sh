#!/bin/bash
# Smoke test of the arm64 package in an emulated ARM VM (no KVM: slow, about
# an hour). Needs qemu-system-aarch64 and the AAVMF firmware (see vm.sh).
# Credentials below are test-only.
set -uo pipefail
cd "$(dirname "$0")/.."
VERSION=$(cat VERSION)
DEB=dist/strataos_${VERSION}_arm64.deb
[ -f "$DEB" ] || { echo "missing $DEB (make deb ARCH=arm64)"; exit 1; }
export VM_ARCH=arm64 VM_STATE=$PWD/vm/state-arm VM_HOSTNAME=strata-arm VM_WEB_PORT=7090 VM_SSH_PORT=2242 \
  VM_HTTPS_PORT=7463 VM_SMB_PORT=4470 SEED_PORT=8739 VM_EXTRA_PORTS="" VM_DISKS=2 VM_CPUS=4 VM_MEM=3072
B=http://localhost:7090
ADMIN_USER=nathan ADMIN_PASS=Strata-Test-2026
J=$(mktemp)
trap 'rm -f "$J"' EXIT
FAILS=0 PASSES=0
c() { local m=$1 p=$2; shift 2; curl -s -b "$J" -c "$J" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
check() { case "$2" in *"$3"*) PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1" ;; *) FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    expected %q, got: %s\n' "$1" "$3" "${2:0:300}" ;; esac; }
vm() { ./vm/vm.sh ssh "$@" 2>/dev/null; }
job() {
  local id; id=$(echo "$1" | py 'print(d.get("id",""))' 2>/dev/null)
  [ -n "$id" ] || { echo "no-job: $1"; return; }
  for _ in $(seq 1 600); do st=$(c GET "/api/jobs/$id" | py 'print(d["status"])'); [ "$st" != running ] && break; sleep 3; done
  c GET "/api/jobs/$id" | py 'print(d["status"], d.get("error",""))'
}

./vm/vm.sh destroy >/dev/null 2>&1
./vm/vm.sh create || exit 1
./vm/vm.sh deploy "$DEB" || exit 1
for _ in $(seq 1 120); do curl -sf "$B/api/setup" >/dev/null && break; sleep 5; done
printf '\n\033[1;36m== arm64\033[0m\n'
check "architecture" "$(vm 'uname -m; dpkg --print-architecture')" "aarch64"
check "service active" "$(vm 'systemctl is-active strata smbd')" "active"
check "setup wizard" "$(c POST /api/setup -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"fullName\":\"Test\",\"hostname\":\"strata-arm\"}")" '"role":"admin"'
D=$(c GET /api/storage/disks | py 'print([x["path"] for x in d if x["serial"]=="STRATA-VD-1"][0])')
check "Btrfs volume" "$(job "$(c POST /api/storage/volumes -d "{\"name\":\"data\",\"fsType\":\"btrfs\",\"devices\":[\"$D\"],\"dataProfile\":\"single\"}")")" "success"
check "share" "$(c POST /api/shares -d '{"name":"Docs","path":"/srv/strata/data/Docs","smb":true,"browseable":true,"permissions":{"nathan":"rw"}}')" '"name":"Docs"'
echo "bonjour arm64" > "$J.up"
check "upload" "$(curl -s -b "$J" -H 'X-Strata: 1' -F "f=@$J.up;filename=hello.txt" "$B/api/files/upload?path=/srv/strata/data/Docs")" '"uploaded":1'
check "SMB" "$(vm "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq smbclient >/dev/null 2>&1; smbclient //localhost/Docs -U '$ADMIN_USER%$ADMIN_PASS' -c 'get hello.txt /tmp/h.txt' >/dev/null 2>&1; cat /tmp/h.txt")" "bonjour arm64"
check "encrypted share (gocryptfs)" "$(c POST /api/shares -d '{"name":"Coffre","path":"/srv/strata/data/Coffre","smb":true,"encrypted":true,"passphrase":"coffre-secret-1","browseable":true,"permissions":{}}' | py 'print(len(d["recoveryKey"]))')" "71"
check "address blocking (nftables)" "$(c PUT /api/guard -d '{"enabled":true,"maxFailures":5,"windowMin":10,"blockMin":60,"neverLan":true,"geo":{"enabled":false}}'; vm 'sudo nft list table inet strata_guard | head -1')" "table inet strata_guard"
MT=$(c PUT /api/metrics -d '{"enabled":true}' | py 'print(d["token"])')
check "metrics" "$(curl -s -H "Authorization: Bearer $MT" $B/metrics | grep -c '^strata_info')" "1"
rm -f "$J.up"
printf '\n%s passed, %s failed\n' "$PASSES" "$FAILS"
./vm/vm.sh destroy >/dev/null 2>&1
[ "$FAILS" -eq 0 ]
