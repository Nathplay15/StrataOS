#!/bin/bash
# Upgrade test: installs the previous version (built from git, OLD_REF =
# latest tag by default) on a fresh VM, fills it with data and settings,
# upgrades to dist/strataos_<VERSION>_amd64.deb and checks that nothing was
# lost, including after a reboot. Credentials below are test-only.
set -uo pipefail
cd "$(dirname "$0")/.."
VERSION=$(cat VERSION)
NEW=dist/strataos_${VERSION}_amd64.deb
OLD_REF=${OLD_REF:-$(git describe --tags --abbrev=0 2>/dev/null)}
[ -f "$NEW" ] || { echo "missing $NEW (make deb)"; exit 1; }
[ -n "$OLD_REF" ] || { echo "set OLD_REF (no tag found)"; exit 1; }
export VM_STATE=$PWD/vm/state-up VM_HOSTNAME=strata-up VM_WEB_PORT=7100 VM_SSH_PORT=2252 VM_HTTPS_PORT=7473 \
  VM_SMB_PORT=4480 SEED_PORT=8749 VM_EXTRA_PORTS="" VM_DISKS=3
B=http://localhost:7100
ADMIN_USER=admin ADMIN_PASS=Strata-Test-2026 USER_NAME=alice USER_PASS=Alice-Pass-1
J=$(mktemp) J2=$(mktemp) OLD=$(mktemp -d)
trap 'rm -f "$J" "$J2"; git worktree remove --force "$OLD/src" >/dev/null 2>&1; rm -rf "$OLD"' EXIT
FAILS=0 PASSES=0
c() { local m=$1 p=$2; shift 2; curl -s -b "$J" -c "$J" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
u() { local m=$1 p=$2; shift 2; curl -s -b "$J2" -c "$J2" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
check() { case "$2" in *"$3"*) PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1" ;; *) FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    expected %q, got: %s\n' "$1" "$3" "${2:0:300}" ;; esac; }
section() { printf '\n\033[1;36m== %s\033[0m\n' "$1"; }
vm() { ./vm/vm.sh ssh "$@" 2>/dev/null; }
wait_api() { for _ in $(seq 1 90); do curl -sf "$B/api/setup" >/dev/null && return; sleep 2; done; }
job() {
  local id; id=$(echo "$1" | py 'print(d.get("id",""))' 2>/dev/null)
  [ -n "$id" ] || { echo "no-job: $1"; return; }
  for _ in $(seq 1 300); do st=$(c GET "/api/jobs/$id" | py 'print(d["status"])'); [ "$st" != running ] && break; sleep 2; done
  c GET "/api/jobs/$id" | py 'print(d["status"], d.get("error",""))'
}
dev() { c GET /api/storage/disks | py "print([x['path'] for x in d if x['serial']=='$1'][0])"; }

section "Build $OLD_REF"
git worktree add --detach "$OLD/src" "$OLD_REF" >/dev/null 2>&1 || { echo "cannot check out $OLD_REF"; exit 1; }
(cd "$OLD/src" && make deb >"$OLD/build.log" 2>&1) || { tail -20 "$OLD/build.log"; exit 1; }
OLDDEB=$(ls "$OLD"/src/dist/strataos_*_amd64.deb)
OLDVER=$(cat "$OLD/src/VERSION")
check "previous version built" "$OLDDEB" "strataos_${OLDVER}_amd64.deb"

section "Previous version with data"
./vm/vm.sh destroy >/dev/null 2>&1
./vm/vm.sh create >/dev/null || exit 1
./vm/vm.sh deploy "$OLDDEB" >/dev/null || exit 1
wait_api
check "old version running" "$(curl -s $B/api/setup)" "\"version\":\"$OLDVER\""
c POST /api/setup -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"fullName\":\"Test\",\"hostname\":\"strata-up\"}" >/dev/null
c POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"remember\":true}" >/dev/null
check "ext4 volume" "$(job "$(c POST /api/storage/volumes -d "{\"name\":\"main\",\"fsType\":\"ext4\",\"devices\":[\"$(dev STRATA-HDD-1)\"]}")")" "success"
check "btrfs volume" "$(job "$(c POST /api/storage/volumes -d "{\"name\":\"fast\",\"fsType\":\"btrfs\",\"devices\":[\"$(dev STRATA-HDD-2)\"],\"dataProfile\":\"single\"}")")" "success"
c POST /api/users -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\",\"role\":\"user\"}" >/dev/null
c POST /api/shares -d "{\"name\":\"Docs\",\"path\":\"/srv/strata/main/Docs\",\"smb\":true,\"browseable\":true,\"recycleBin\":true,\"quotaGb\":5,\"permissions\":{\"$USER_NAME\":\"rw\"}}" >/dev/null
echo "signed contract" > "$J.f"
curl -s -b "$J" -H 'X-Strata: 1' -F "f=@$J.f;filename=contract.txt" "$B/api/files/upload?path=/srv/strata/main/Docs" >/dev/null
VID=$(c GET /api/storage/volumes | py 'print([v["id"] for v in d if v["name"]=="fast"][0])')
SID=$(c POST /api/schedules -d "{\"name\":\"snap\",\"type\":\"snapshot\",\"cron\":\"0 3 * * *\",\"enabled\":true,\"targetKind\":\"btrfs\",\"target\":\"$VID\",\"keepCount\":3}" | py 'print(d["id"])')
check "snapshot task" "$(job "$(c POST /api/schedules/$SID/run)")" "success"
BK=$(c POST /api/backups -d '{"name":"versions","kind":"restic","sources":["/srv/strata/main/Docs"],"destType":"local","destPath":"/srv/strata/fast/restic","password":"Restic-Test-Pass","cron":"0 1 * * *","enabled":true}' | py 'print(d["id"])')
check "backup" "$(job "$(c POST /api/backups/$BK/run)")" "success"
SET=$(c GET /api/settings | py 'd["monthlyReport"]=True; d["webhookUrl"]="http://127.0.0.1:9/hook"; print(json.dumps(d))')
c PUT /api/settings -d "$SET" >/dev/null
BEFORE=$(date +%s)

section "Upgrade to $VERSION"
check "package upgraded" "$(./vm/vm.sh deploy "$NEW" 2>&1 | tail -3)" "StrataOS installed"
wait_api
check "new version running" "$(curl -s $B/api/setup)" "\"version\":\"$VERSION\""
check "session kept" "$(c GET /api/me)" "\"username\":\"$ADMIN_USER\""
check "user can still sign in" "$(u POST /api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}")" '"username":"alice"'
check "volumes mounted" "$(c GET /api/storage/volumes | py 'print(sorted(v["name"] for v in d if v["mounted"]))')" "['fast', 'main']"
check "share and quota kept" "$(c GET /api/shares | py 'print([(s["name"], s.get("quotaGb")) for s in d])')" "('Docs', 5"
check "file reachable over SMB" "$(vm "sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq smbclient >/dev/null 2>&1; smbclient //localhost/Docs -U '$USER_NAME%$USER_PASS' -c 'get contract.txt /tmp/c.txt' >/dev/null 2>&1; cat /tmp/c.txt")" "signed contract"
check "settings kept" "$(c GET /api/settings | py 'print(d.get("monthlyReport"), d.get("webhookUrl"))')" "True http://127.0.0.1:9/hook"
check "scheduled task kept" "$(c GET /api/schedules | py 'print([s["name"] for s in d])')" "snap"
check "snapshot task after upgrade" "$(job "$(c POST /api/schedules/$SID/run)")" "success"
check "backup after upgrade" "$(job "$(c POST /api/backups/$BK/run)")" "success"
check "backup history kept" "$(c GET /api/backups | py 'print(len(d[0]["history"]))')" "2"
check "new features available" "$(c GET /api/guard | py 'print("config" in d)') $(c GET /api/energy | py 'print("config" in d)') $(c GET /api/vaults | py 'print(type(d).__name__)') $(c GET /api/fleet | py 'print(len(d))')" "True True list 1"
check "no error since the upgrade" "$(c GET /api/system/events | py "import datetime; print([e['message'] for e in d if e['level']=='error' and datetime.datetime.fromisoformat(e['time']).timestamp() > $BEFORE])")" "[]"

section "Reboot after the upgrade"
vm "sudo systemctl reboot"; sleep 10
for _ in $(seq 1 60); do vm true && break; sleep 3; done
wait_api
check "still running after a reboot" "$(c GET /api/me) $(vm 'systemctl is-active strata smbd')" "active"
check "volumes remounted" "$(c GET /api/storage/volumes | py 'print(sorted(v["name"] for v in d if v["mounted"]))')" "['fast', 'main']"

rm -f "$J.f"
./vm/vm.sh destroy >/dev/null 2>&1
printf '\n%s passed, %s failed\n' "$PASSES" "$FAILS"
[ "$FAILS" -eq 0 ]
