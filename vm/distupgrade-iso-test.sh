#!/bin/bash
# The Debian release upgrade in one button, on a system installed from the
# StrataOS ISO (Btrfs system: restore points by snapshot), UEFI:
#   1. the upgrade to Debian 14 (forky, still testing: the override is turned
#      on) with "restart and check by itself"; right after the restart a data
#      volume is unmounted by hand, so the checks after the restart fail: the
#      NAS goes back to its restore point by itself and runs Debian 13 again,
#      with its volume and settings;
#   2. the same upgrade again, left alone: Debian 14 runs, checks pass.
# About 90 minutes (two downloads of ~1 GB of packages). Usage:
#   make deb && flock -o /home/nathan/Documents/StrataOS-wt/.vm.lock timeout 150m ./vm/distupgrade-iso-test.sh
# Uses the ISO test VM (ISO_STATE, ISO_WEB_PORT 7071, ISO_SSH_PORT 2223) and
# SRC_ISO (the Debian 13 netinst ISO, vm/images by default). Credentials are test-only.
set -uo pipefail
cd "$(dirname "$0")/.."
VERSION=$(cat VERSION)
export ISO_STATE=${ISO_STATE:-$PWD/vm/state-dist-iso} ISO_MEM=${ISO_MEM:-3072} ISO_DISK_SIZE=${ISO_DISK_SIZE:-24G}
WEB=${ISO_WEB_PORT:-7071} SSHP=${ISO_SSH_PORT:-2223}
B=http://localhost:$WEB
KEYDIR=$PWD/vm/state-dist-iso-key
J=$(mktemp)
PASSES=0 FAILS=0 START=$(date +%s)
trap 'rm -f "$J"; [ -n "${KEEP:-}" ] || { ./vm/iso-test.sh stop; rm -rf "$ISO_STATE"; }' EXIT
mkdir -p "$KEYDIR"
[ -f "$KEYDIR/id" ] || ssh-keygen -q -t ed25519 -N "" -C dist-iso-test -f "$KEYDIR/id"
c() { local m=$1 p=$2; shift 2; curl -s -m 120 -b "$J" -c "$J" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
ok() { PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1"; }
ko() { FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    %s\n' "$1" "$2"; }
check() { case "$2" in *"$3"*) ok "$1" ;; *) ko "$1" "expected '$3', got: ${2:0:1500}" ;; esac; }
section() { printf '\n\033[1;36m== %s  (%d min)\033[0m\n' "$1" $(( ($(date +%s) - START) / 60 )); }
vm() { ssh -i "$KEYDIR/id" -p "$SSHP" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5 root@127.0.0.1 "$@" 2>/dev/null; }
login() { c POST /api/login -d '{"username":"admin","password":"Strata-Test-2026"}' >/dev/null; }
up() { curl -sf -m 3 "$B/api/setup" >/dev/null; }
job() { local id; id=$(echo "$1" | py 'print(d.get("id",""))' 2>/dev/null); [ -n "$id" ] || { echo "no-job: $1"; return; }
  for _ in $(seq 1 ${2:-600}); do [ "$(c GET "/api/jobs/$id" | py 'print(d["status"])' 2>/dev/null)" = running ] || break; sleep 3; done
  c GET "/api/jobs/$id" | py 'print(d["status"], d.get("error","")); print(d.get("log","")[-2500:])'; }
phase() { c GET /api/system/dist-upgrade | py 'r=d.get("run") or {}; print(r.get("phase",""))' 2>/dev/null; }
# Waits for the NAS to go down then answer again.
restarted() { for _ in $(seq 1 600); do up || break; sleep 2; done; for _ in $(seq 1 300); do up && break; sleep 4; done; sleep 3; login; }
pending_updates() { # the updates of Debian 13 first (pre-check)
  c POST /api/system/updates/refresh >/dev/null; sleep 20
  c POST /api/system/updates/upgrade >/dev/null
  for _ in $(seq 1 180); do sleep 5; [ "$(vm 'systemctl is-active strata-apt.service')" = active ] && continue
    up && [ "$(c GET /api/system/updates | py 'print(len(d))' 2>/dev/null)" = 0 ] && break; done
  login
}
finish() { printf '\n%s passed, %s failed (%d min)\n' "$PASSES" "$FAILS" $(( ($(date +%s) - START) / 60 ))
  [ "$FAILS" -gt 0 ] && vm 'journalctl -u strata --no-pager -n 50 -o cat' | tail -50
  [ "$FAILS" -eq 0 ]; exit; }

section "Debian 13 installed from the ISO"
ISO=dist/strataos-${VERSION}-amd64.iso
ISO_TEST_SSH_KEY="$KEYDIR/id.pub" ARCH=amd64 ./packaging/iso/build-iso.sh >/dev/null || { echo "ISO build failed"; exit 1; }
./vm/iso-test.sh "$ISO" 1 uefi >/dev/null || { echo "installation failed"; tail -5 "$ISO_STATE/console.log"; exit 1; }
c POST /api/setup -d '{"username":"admin","password":"Strata-Test-2026","fullName":"Test","hostname":"dist-iso"}' >/dev/null
login
check "Btrfs system on Debian 13" "$(vm 'findmnt -n -o FSTYPE /; grep VERSION_CODENAME /etc/os-release')" "trixie"
D=$(c GET /api/storage/disks | py 'print([x["path"] for x in d if x["usage"]=="free"][0])')
R=$(c POST /api/storage/volumes -d "{\"name\":\"vol\",\"fsType\":\"ext4\",\"devices\":[\"$D\"]}")
check "data volume" "$(job "$R" | head -1)" "success"
vm 'echo kept > /srv/strata/vol/note.txt'
check "restore points by snapshot" "$(c GET /api/system/restore-points | py 'print(d.get("mode"))')" "btrfs"
check "testing allowed by the administrator" "$(c PUT /api/system/dist-upgrade -d '{"allowTesting":true}' | py 'print(d["available"])')" "True"
pending_updates
C=$(c POST /api/system/dist-upgrade/check)
echo "$C" | py 'print("\n".join("    %s: %s" % (c["level"], c["text"]) for c in d["checks"]))'
check "pre-checks pass" "$(echo "$C" | py 'print(d["blocked"])')" "False"
check "automatic rollback offered" "$(echo "$C" | py 'print([c["level"] for c in d["checks"] if c["id"]=="restore-point"])')" "['ok']"

section "One button, the checks after the restart fail: back to Debian 13"
S=$(c POST /api/system/dist-upgrade/start -d '{"confirm":"forky","auto":true}')
ID=$(echo "$S" | py 'print(d["id"])')
for _ in $(seq 1 900); do up || break; [ "$(phase)" = reboot ] && break; sleep 4; done
check "packages installed, restarting by itself" "$(c GET "/api/jobs/$ID" | py 'print(d["status"]); print(d.get("log","")[-400:])')" "restarts into it now"
for _ in $(seq 1 600); do up || break; sleep 2; done
for _ in $(seq 1 300); do vm true && break; sleep 3; done
# Before the checks run (a minute after the start): a volume goes missing.
for _ in $(seq 1 60); do vm 'findmnt -n /srv/strata/vol' >/dev/null && break; sleep 2; done
check "running Debian 14 after the restart" "$(vm 'grep VERSION_CODENAME /etc/os-release')" "forky"
vm 'umount -l /srv/strata/vol'
# The checks fail, the NAS goes back to the restore point and restarts.
for _ in $(seq 1 120); do up && [ "$(phase)" = rolling-back ] && break; ! up && break; sleep 3; done
restarted
for _ in $(seq 1 60); do [ "$(phase)" = rolled-back ] && break; sleep 5; done
check "rolled back by itself" "$(c GET /api/system/dist-upgrade | py 'r=d["run"]; print(r["phase"], r.get("rolledBack",""))')" "rolled-back checks after the restart: Volumes not mounted: vol"
check "Debian 13 again" "$(vm 'grep VERSION_CODENAME /etc/os-release; grep -h -m1 -E "^Suites|^deb " /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list 2>/dev/null')" "trixie"
check "volume mounted, data there" "$(vm 'cat /srv/strata/vol/note.txt')" "kept"
check "settings kept" "$(c GET /api/me)" '"username":"admin"'
check "the upgrade is offered again" "$(c GET /api/system/dist-upgrade | py 'print(d["current"], d["target"], d["available"])')" "trixie forky True"

section "One button, left alone: Debian 14"
C=$(c POST /api/system/dist-upgrade/check)
check "pre-checks pass again" "$(echo "$C" | py 'print(d["blocked"])')" "False"
c POST /api/system/dist-upgrade/start -d '{"confirm":"forky","auto":true}' >/dev/null
for _ in $(seq 1 900); do up || break; [ "$(phase)" = reboot ] && break; sleep 4; done
restarted
for _ in $(seq 1 60); do [ "$(phase)" = done ] && break; sleep 5; done
PC=$(c GET /api/system/dist-upgrade | py 'r=d["run"]; print(r["phase"], r.get("error","")); print("\n".join("    %s: %s" % (c["level"], c["text"]) for c in r.get("postChecks", [])))')
echo "$PC"
check "finished, checks pass" "$PC" "done"
check "no failed check" "$(echo "$PC" | grep -c '^    block')" "0"
check "Debian 14 running" "$(vm 'grep VERSION_CODENAME /etc/os-release')" "forky"
check "volume mounted, data there" "$(vm 'cat /srv/strata/vol/note.txt')" "kept"
check "StrataOS answers" "$(c GET /api/me)" '"username":"admin"'
finish
