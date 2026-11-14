#!/bin/bash
# Debian release upgrade from the interface, end to end: a Debian 13 (trixie)
# VM with StrataOS, a Btrfs volume, a shared folder and (ZFS=1) a ZFS pool, upgraded to
# Debian 14 (forky, still testing: the "testing (not recommended)" override is turned on),
# restarted, then the post-checks. About 40 minutes (downloads ~1 GB of packages).
#   make deb && flock -o /home/nathan/Documents/StrataOS-wt/.vm.lock timeout 90m ./vm/distupgrade-test.sh
#   (recreates the VM in VM_STATE, default vm/state-dist; KEEP=1 keeps it)
set -uo pipefail
cd "$(dirname "$0")/.."
DEB=${DEB:-dist/strataos_$(cat VERSION)_amd64.deb}
export VM_STATE=${VM_STATE:-$PWD/vm/state-dist} VM_DEBIAN=13 VM_MEM=${VM_MEM:-2560} VM_CPUS=${VM_CPUS:-4}
export VM_DISKS=${VM_DISKS:-2} VM_DISK_SIZE=${VM_DISK_SIZE:-8G} VM_EXTRA_PORTS=${VM_EXTRA_PORTS-}
P=${VM_WEB_PORT:-7070}
ADMIN_USER=admin ADMIN_PASS=Strata-Test-2026
T=$(mktemp -d)
PASSES=0 FAILS=0 START=$(date +%s)
ok() { PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1"; }
ko() { FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    %s\n' "$1" "$2"; }
check() { case "$2" in *"$3"*) ok "$1" ;; *) ko "$1" "expected '$3', got: ${2:0:1500}" ;; esac; }
section() { printf '\n\033[1;36m== %s  (%d min)\033[0m\n' "$1" $(( ($(date +%s) - START) / 60 )); }
api() { local m=$1 p=$2; shift 2; curl -s -m 120 -b "$T/ck" -c "$T/ck" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "http://localhost:$P$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
vm() { ./vm/vm.sh ssh "$@" 2>/dev/null | tr -d '\r'; }
job() { local id; id=$(echo "$1" | py 'print(d.get("id",""))' 2>/dev/null); [ -n "$id" ] || { echo "no-job: $1"; return; }
  for _ in $(seq 1 ${2:-400}); do [ "$(api GET "/api/jobs/$id" | py 'print(d["status"])' 2>/dev/null)" = running ] || break; sleep 3; done
  api GET "/api/jobs/$id" | py 'print(d["status"], d.get("error","")); print(d.get("log","")[-3000:])'; }
login() { api POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null; }
finish() { printf '\n%s passed, %s failed (%d min)\n' "$PASSES" "$FAILS" $(( ($(date +%s) - START) / 60 ))
  [ "$FAILS" -gt 0 ] && vm 'sudo journalctl -u strata --no-pager -n 60 -o cat' | tail -60
  [ -n "${KEEP:-}" ] || ./vm/vm.sh destroy >/dev/null 2>&1; rm -rf "$T"; [ "$FAILS" -eq 0 ]; exit; }

section "Debian 13 NAS"
./vm/vm.sh destroy >/dev/null 2>&1
./vm/vm.sh create >"$T/create.log" 2>&1 && ./vm/vm.sh deploy "$DEB" >>"$T/create.log" 2>&1 || { tail -20 "$T/create.log"; finish; }
check "running Debian 13" "$(vm 'grep VERSION_CODENAME /etc/os-release')" "trixie"
api POST /api/setup -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"fullName\":\"Test\",\"hostname\":\"dist\"}" >/dev/null
login
D=$(api GET /api/storage/disks | py 'print([x["path"] for x in d if x["usage"]=="free"][0])')
check "Btrfs volume" "$(job "$(api POST /api/storage/volumes -d "{\"name\":\"volume1\",\"fsType\":\"btrfs\",\"devices\":[\"$D\"],\"dataProfile\":\"single\"}")")" "success"
vm 'sudo mkdir -p /srv/strata/volume1/Docs && echo kept | sudo tee /srv/strata/volume1/Docs/note.txt >/dev/null'
check "shared folder" "$(api POST /api/shares -d '{"name":"Docs","path":"/srv/strata/volume1/Docs","smb":true,"permissions":{"admin":"rw"}}')" '"name":"Docs"'
if [ -n "${ZFS:-}" ]; then
  check "ZFS component" "$(job "$(api POST /api/system/components/zfs/install)" 900 | head -1)" "success"
  D2=$(api GET /api/storage/disks | py 'print([x["path"] for x in d if x["usage"]=="free"][0])')
  check "ZFS pool" "$(job "$(api POST /api/storage/zfs -d "{\"name\":\"tank\",\"vdevs\":[{\"type\":\"stripe\",\"disks\":[\"$D2\"]}]}")" | head -1)" "success"
fi

section "Pre-checks"
S=$(api GET /api/system/dist-upgrade)
check "target forky, not released yet" "$(echo "$S" | py 'print(d["current"], d["target"], d["available"], d["testing"])')" "trixie forky False forky"
check "start refused while not offered" "$(api POST /api/system/dist-upgrade/start -d '{"confirm":"forky"}')" "not released yet"
check "testing allowed by the administrator" "$(api PUT /api/system/dist-upgrade -d '{"allowTesting":true}' | py 'print(d["available"])')" "True"
# The updates may restart the StrataOS service (its job is then lost): wait for none left.
api POST /api/system/updates/refresh >/dev/null; sleep 20
api POST /api/system/updates/upgrade >/dev/null
for _ in $(seq 1 120); do sleep 5; [ "$(vm 'systemctl is-active strata-apt.service')" = active ] && continue
  [ "$(api GET /api/system/updates | py 'print(len(d))' 2>/dev/null)" = 0 ] && break; done
login
check "pending updates installed first" "$(api GET /api/system/updates | py 'print(len(d))')" "0"
C=$(api POST /api/system/dist-upgrade/check)
echo "$C" | py 'print("\n".join("    %s: %s" % (c["level"], c["text"]) for c in d["checks"]))'
check "pre-checks pass" "$(echo "$C" | py 'print(d["blocked"], [c["id"] for c in d["checks"] if c["level"]=="block"])')" "False []"
check "Debian sources move to forky" "$(echo "$C" | py 'print(sorted({(s["from"], s["to"]) for s in d["sources"]}))')" "('trixie', 'forky')"
check "wrong confirmation refused" "$(api POST /api/system/dist-upgrade/start -d '{"confirm":"yes"}')" "type forky"

section "Upgrade"
R=$(job "$(api POST /api/system/dist-upgrade/start -d '{"confirm":"forky"}')" 1500)
check "upgrade job" "$R" "success"
echo "$R" | grep -E "restore point|packages to upgrade|→|ZFS" | head -12 | sed 's/^/    /'
check "waiting for the restart" "$(api GET /api/system/dist-upgrade | py 'r=d["run"]; print(r["phase"], r["to"], bool(r.get("kernel")))')" "reboot forky True"
check "sources on forky" "$(vm 'cat /etc/apt/sources.list.d/debian.sources 2>/dev/null || cat /etc/apt/sources.list' | grep -m1 -E '^Suites|^deb ')" "forky"
check "StrataOS sources untouched" "$(vm 'grep Suites /etc/apt/sources.list.d/strataos.sources 2>/dev/null || echo "Suites: stable (no repository in this build)"')" "stable"
check "managed configuration kept (smb.conf)" "$(vm 'sudo testparm -s 2>/dev/null | grep -c "^\[Docs\]"')" "1"
check "no half-installed package" "$(vm 'sudo dpkg --audit | wc -l')" "0"
check "restart" "$(api POST /api/system/dist-upgrade/reboot)" '"ok":true'
sleep 20
for _ in $(seq 1 60); do curl -sf -m 5 "http://localhost:$P/api/setup" >/dev/null && break; sleep 5; done
login

section "After the restart"
check "Debian 14 running" "$(vm 'grep VERSION_CODENAME /etc/os-release; uname -r')" "forky"
for _ in $(seq 1 60); do [ "$(api GET /api/system/dist-upgrade | py 'print(d["run"]["phase"])')" = done ] && break; sleep 5; done
PC=$(api GET /api/system/dist-upgrade | py 'r=d["run"]; print(r["phase"], r.get("error","")); print("\n".join("    %s: %s" % (c["level"], c["text"]) for c in r.get("postChecks", [])))')
echo "$PC"
check "post-checks done" "$PC" "done"
check "no failed post-check" "$(echo "$PC" | grep -c '^    block')" "0"
check "the new kernel runs" "$(echo "$PC" | grep -c 'not the new')" "0"
check "volume mounted, data there" "$(vm 'sudo cat /srv/strata/volume1/Docs/note.txt')" "kept"
check "StrataOS answers" "$(api GET /api/me)" "\"username\":\"$ADMIN_USER\""
check "Samba running" "$(vm 'systemctl is-active smbd')" "active"
[ -n "${ZFS:-}" ] && check "ZFS pool imported" "$(vm 'sudo zpool list -H -o name,health')" "ONLINE"
check "the upgrade is not offered again" "$(api GET /api/system/dist-upgrade | py 'print(d["current"], d["target"])')" "forky duke"
finish
