#!/bin/bash
# The system upgrade to the next Debian release, on a system installed from
# the StrataOS ISO (Btrfs system: restore points by snapshot), UEFI. Debian
# testing (forky) stands in for the next stable release (the override of
# the Updates page), as in vm/distupgrade-test.sh:
#   1. a check fails (a half-installed package): no proposal, the blocking
#      check is listed with its fix; an administrator forces the upgrade
#      (typed codename, logged and told); the checks after the restart are
#      made to fail (a root-only test file read once, as for the automatic
#      updates): the NAS goes back to its restore point by itself, on
#      Debian 13;
#   2. the package fixed, the check passes: the proposal appears and the
#      administrators are told; No hides it, Show brings it back; Yes, in
#      the maintenance window (opened by a test hook): Debian 14 runs.
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
ISO_TEST_SSH_KEY="$KEYDIR/id.pub" ISO_TEST_BOOTARGS=partman-auto/disk=/dev/sda ARCH=amd64 ./packaging/iso/build-iso.sh >/dev/null || { echo "ISO build failed"; exit 1; }
./vm/iso-test.sh "$ISO" 1 uefi >/dev/null || { echo "installation failed"; tail -5 "$ISO_STATE/console.log"; exit 1; }
c POST /api/setup -d '{"username":"admin","password":"Strata-Test-2026","fullName":"Test","hostname":"dist-iso"}' >/dev/null
login
# Notifications in English (the checks read their titles).
c GET /api/settings | py 'd["notifyLang"]="en"; print(json.dumps(d))' > "$J.set" && c PUT /api/settings -d @"$J.set" >/dev/null
check "Btrfs system on Debian 13" "$(vm 'findmnt -n -o FSTYPE /; grep VERSION_CODENAME /etc/os-release')" "trixie"
D=$(c GET /api/storage/disks | py 'print([x["path"] for x in d if x["usage"]=="free"][0])')
R=$(c POST /api/storage/volumes -d "{\"name\":\"vol\",\"fsType\":\"ext4\",\"devices\":[\"$D\"]}")
check "data volume" "$(job "$R" | head -1)" "success"
vm 'echo kept > /srv/strata/vol/note.txt'
check "restore points by snapshot" "$(c GET /api/system/restore-points | py 'print(d.get("mode"))')" "btrfs"
check "testing allowed by the administrator" "$(c PUT /api/system/dist-upgrade -d '{"allowTesting":true}' | py 'print(d["available"])')" "True"
pending_updates
# A kernel installed by the updates must run before the checks pass.
if [ "$(vm 'ls /boot/vmlinuz-* | sed "s|.*vmlinuz-||" | sort -V | tail -1')" != "$(vm 'uname -r')" ]; then vm 'systemctl reboot'; restarted; fi
dcheck() { job "$(c POST /api/system/dist-upgrade/check)" 400 >/dev/null; c GET /api/system/dist-upgrade; }

section "A check fails: no proposal, forced, rolled back after the restart"
vm 'cd /tmp && apt-get download -q hello >/dev/null 2>&1 && dpkg --unpack hello_*.deb >/dev/null 2>&1; dpkg --audit | head -3'
S=$(dcheck)
echo "$S" | py 'print("\n".join("    %s: %s" % (c["level"], c["text"]) for c in d["check"]["checks"]))'
check "the check of the half-installed package blocks" "$(echo "$S" | py 'print(d["check"]["blocked"], [c["id"] for c in d["check"]["checks"] if c["level"]=="block"])')" "True ['dpkg']"
check "no proposal while a check fails" "$(echo "$S" | py 'print(d["proposal"])')" "False"
check "its fix is said" "$(echo "$S" | py 'print([c.get("fix","") for c in d["check"]["checks"] if c["id"]=="dpkg"])')" "Install the system updates"
check "target found in the archive, generic" "$(echo "$S" | py 'print(d["target"], d["targetVersion"], d["available"], d.get("testing"))')" "forky 14 True True"
check "refused without force" "$(c POST /api/system/dist-upgrade/start -d '{"when":"now"}')" "the checks failed"
check "force needs the codename typed" "$(c POST /api/system/dist-upgrade/start -d '{"when":"now","force":true,"confirm":"yes"}')" "type forky"
vm 'touch /var/lib/strata/dist-postcheck-test'
S=$(c POST /api/system/dist-upgrade/start -d '{"when":"now","force":true,"confirm":"forky"}')
ID=$(echo "$S" | py 'print(d["id"])')
for _ in $(seq 1 10); do c GET /api/notifications | grep -q 'forced by admin' && break; sleep 1; done
check "forced upgrade told to the administrators" "$(c GET /api/notifications | grep -o 'forced by admin despite failed checks' | head -1)" "forced by admin despite failed checks"
for _ in $(seq 1 900); do up || break; [ "$(phase)" = reboot ] && break; sleep 4; done
check "packages installed, restarting by itself" "$(c GET "/api/jobs/$ID" | py 'print(d["status"], "restarts into it now" in d.get("log",""))')" "success True"
for _ in $(seq 1 600); do up || break; sleep 2; done
for _ in $(seq 1 300); do vm true && break; sleep 3; done
check "running Debian 14 after the restart" "$(vm 'grep VERSION_CODENAME /etc/os-release')" "forky"
for _ in $(seq 1 120); do up && [ "$(phase)" = rolling-back ] && break; ! up && break; sleep 3; done
restarted
for _ in $(seq 1 60); do [ "$(phase)" = rolled-back ] && break; sleep 5; done
check "rolled back by itself" "$(c GET /api/system/dist-upgrade | py 'r=d["run"]; print(r["phase"], r.get("forced"), r.get("rolledBack",""))')" "rolled-back True checks after the restart: Test failure of the checks after the restart"
check "Debian 13 again" "$(vm 'grep VERSION_CODENAME /etc/os-release; grep -h -m1 -E "^Suites|^deb " /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list 2>/dev/null')" "trixie"
check "volume mounted, data there" "$(vm 'cat /srv/strata/vol/note.txt')" "kept"
check "settings kept" "$(c GET /api/me)" '"username":"admin"'

section "The checks pass: proposal, No, Yes in the maintenance window"
vm 'dpkg --configure -a >/dev/null 2>&1; apt-get -y -q purge hello >/dev/null 2>&1'
S=$(dcheck)
check "checks pass, proposal shown" "$(echo "$S" | py 'print(d["check"]["blocked"], d["proposal"])')" "False True"
check "administrators told of the proposal" "$(c GET /api/notifications | grep -o 'System upgrade available' | head -1)" "System upgrade available"
check "No hides it" "$(c POST /api/system/dist-upgrade/dismiss | py 'print(d["proposal"], d.get("dismissed"))')" "False True"
check "Show brings it back" "$(c POST /api/system/dist-upgrade/show | py 'print(d["proposal"])')" "True"
check "Yes, in the maintenance window" "$(c POST /api/system/dist-upgrade/start -d '{"when":"window"}' | py 'print(d["scheduled"]["target"], d["proposal"])')" "forky False"
vm 'touch /run/strata/dist-window-test'
for _ in $(seq 1 60); do [ -n "$(phase)" ] && [ "$(phase)" != rolled-back ] && break; sleep 5; done
check "started by the maintenance window" "$(c GET /api/system/dist-upgrade | py 'print(d["run"]["phase"] != "rolled-back", d.get("scheduled"))')" "True None"
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
