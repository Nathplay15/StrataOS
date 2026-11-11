#!/bin/bash
# System disk protection on the test VM (Debian cloud image: ext4 system on a
# partition, BIOS boot), each part on a brand new VM with 2 extra 24 GB disks:
#
#   vm/sysdisk-test.sh mirror   mirror the system disk onto disk 1, restart into
#                               the mirror, complete it, start with disk 1
#                               unplugged (degraded), replace it with a blank disk
#                               through the replacement wizard, start from the
#                               new disk alone
#   vm/sysdisk-test.sh move     system backup onto a data volume, move the system
#                               to disk 1, restart, start without the old disk
#
# Uses its own VM (VM_STATE, default vm/state-sysdisk) and the package in dist/.
# Credentials are test-only.
set -uo pipefail
cd "$(dirname "$0")/.."
export VM_STATE=${VM_STATE:-$PWD/vm/state-sysdisk} VM_DISKS=2 VM_DISK_SIZE=24G VM_MEM=${VM_MEM:-2048} VM_EXTRA_PORTS=${VM_EXTRA_PORTS-}
B=http://localhost:${VM_WEB_PORT:-7070}
J=$(mktemp)
FAILS=0
trap 'rm -f "$J"' EXIT
c() { local m=$1 p=$2; shift 2; curl -s -m 60 -b "$J" -c "$J" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
check() { case "$2" in *"$3"*) printf '  \033[32m✔\033[0m %s\n' "$1" ;; *) FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    expected %q, got: %s\n' "$1" "$3" "${2:0:400}" ;; esac; }
vm() { ./vm/vm.sh ssh "$@" 2>/dev/null; }
login() { c POST /api/login -d '{"username":"admin","password":"Strata-Test-2026"}' >/dev/null; }
wait_api() { for _ in $(seq 1 120); do curl -sf -m 3 "$B/api/setup" >/dev/null && { login; return 0; }; sleep 3; done; return 1; }
job() {
  local id; id=$(echo "$1" | py 'print((d.get("job") or d).get("id",""))' 2>/dev/null)
  [ -n "$id" ] || { echo "no-job: $1"; return; }
  for _ in $(seq 1 900); do
    st=$(c GET "/api/jobs/$id" | py 'print(d["status"])' 2>/dev/null); [ "$st" = running ] || break; sleep 2
  done
  c GET "/api/jobs/$id" | py 'print(d["status"], d.get("error",""))'
}
joblog() { local id; id=$(echo "$1" | py 'print((d.get("job") or d).get("id",""))'); c GET "/api/jobs/$id" | py 'print(d.get("log",""))'; }
dev() { c GET /api/storage/disks | py "print([x['path'] for x in d if x['serial']=='$1'][0])"; }
op() { c GET /api/system/disk | py "o=d['ops'][0] if d['ops'] else {}; print($1)"; }
# Waits for the VM to go down (restart) and come back.
restarted() {
  for _ in $(seq 1 60); do curl -sf -m 2 "$B/api/setup" >/dev/null || break; sleep 2; done
  wait_api
}
fresh_vm() {
  ./vm/vm.sh destroy >/dev/null 2>&1
  ./vm/vm.sh create >/dev/null || { echo "VM creation failed"; exit 1; }
  ./vm/vm.sh deploy "dist/strataos_$(cat VERSION)_amd64.deb" >/dev/null || { echo "deploy failed"; exit 1; }
  wait_api
  c POST /api/setup -d '{"username":"admin","password":"Strata-Test-2026","fullName":"Test","hostname":"sysdisk-vm"}' >/dev/null
  login
}
restart_vm() { # VM_DETACH as given
  ./vm/vm.sh stop >/dev/null
  ./vm/vm.sh start >/dev/null || return 1
  wait_api
}

part_mirror() {
  printf '\n\033[1;36m== Mirror the system disk (BIOS, ext4)\033[0m\n'
  fresh_vm
  H1=$(dev STRATA-HDD-1)
  check "layout: ext4 on a partition, BIOS" "$(c GET /api/system/disk | py 'l=d["layout"]; print(l["boot"], l["root"]["fstype"], l["rootOn"], l["mirrored"])')" "bios ext4 part False"
  check "advisor: no redundancy" "$(c POST /api/security/advisor/run | py 'print([i["severity"] for i in d["items"] if i["id"]=="system-disk"])')" "['low']"
  check "plan: disk 1 is a candidate" "$(c GET "/api/system/disk/plan?kind=mirror&target=$H1" | py 'print(d.get("refusal",""), d["target"]["path"], [p["code"] for p in d["parts"]])')" " $H1 ['EF02', 'FD00']"
  check "the system disk is refused" "$(c GET '/api/system/disk/plan?kind=mirror&target=/dev/vda' | py 'print(d["refusal"])')" "cannot receive"
  check "a wrong confirmation is refused" "$(c POST /api/system/disk/start -d '{"kind":"mirror","target":"'"$H1"'","confirm":"vda"}')" "type the name"
  R=$(c POST /api/system/disk/start -d '{"kind":"mirror","target":"'"$H1"'","confirm":"'"${H1#/dev/}"'"}')
  check "copy onto the degraded mirror" "$(job "$R")" "success"
  joblog "$R" | grep -E '^\$ (sgdisk|mdadm|mkfs)' | head -5
  check "ready to restart" "$(op 'o["phase"]')" "ready"
  check "a second operation is refused" "$(c GET "/api/system/disk/plan?kind=move&target=$(dev STRATA-HDD-2)" | py 'print(d["refusal"])')" "not finished"
  ID=$(op 'o["id"]')
  R=$(c POST "/api/system/disk/$ID/switch")
  check "switch prepared" "$(job "$R")" "success"
  joblog "$R" | grep -E 'grub|fstab|initramfs' | head -8
  restarted
  check "started from the mirror" "$(op 'o["phase"]')" "booted"
  check "/ on the md array" "$(vm "findmnt -n -o SOURCE /")" "/dev/md"
  check "old disk untouched until confirmed" "$(vm "sudo blkid /dev/vda1")" 'TYPE="ext4"'
  check "complete needs the old disk name" "$(c POST "/api/system/disk/$ID/complete" -d '{"confirm":"sda"}')" "type the name"
  R=$(c POST "/api/system/disk/$ID/complete" -d '{"confirm":"vda"}')
  check "mirror completed and rebuilt" "$(job "$R")" "success"
  check "operation done" "$(op 'o["phase"]')" "done"
  check "both disks in the array" "$(vm "cat /proc/mdstat")" "[UU]"
  check "layout mirrored" "$(c GET /api/system/disk | py 'print(d["layout"]["mirrored"], len(d["layout"]["disks"]))')" "True 2"
  check "replacement group of the mirror" "$(c GET "/api/storage/replace/plan?disk=/dev/vda" | py 'print(d["plan"]["group"]["id"], d["plan"]["mode"])')" "sys:mirror add-first"
  check "advisor: no system disk item" "$(c POST /api/security/advisor/run | py 'print([i["id"] for i in d["items"] if i["id"]=="system-disk"])')" "[]"
  check "restart on the complete mirror" "$(restart_vm && vm "cat /proc/mdstat")" "[UU]"

  printf '\n\033[1;36m== Degraded start and replacement of a mirror disk\033[0m\n'
  VM_DETACH=1 restart_vm
  check "starts with disk 1 unplugged" "$(vm "findmnt -n -o SOURCE /")" "/dev/md"
  check "mirror degraded" "$(vm "cat /proc/mdstat")" "[U_]"
  check "alert names the system mirror" "$(c GET /api/system/overview 2>/dev/null | grep -o 'system mirror is degraded' | head -1)" "system mirror is degraded"
  check "advisor: degraded" "$(c POST /api/security/advisor/run | py 'print([i["severity"] for i in d["items"] if i["id"]=="system-disk"])')" "['high']"
  ./vm/vm.sh stop >/dev/null
  ./vm/vm.sh replace-disk 1 >/dev/null
  restart_vm
  H1=$(dev STRATA-HDD-1)
  check "plan: degraded replacement" "$(c GET "/api/storage/replace/plan?group=sys:mirror&new=$H1" | py 'p=d["plan"]; print(p.get("refusal",""), p["mode"])')" " degraded"
  R=$(c POST /api/storage/replace -d '{"group":"sys:mirror","new":"'"$H1"'","confirm":"'"${H1#/dev/}"'"}')
  RID=$(echo "$R" | py 'print(d["id"])')
  check "replacement finished" "$(job "$R")" "success"
  joblog "$R" | grep -E '^\$ ' | head -10
  check "replacement record done" "$(c GET /api/storage/replace | py "print([x['phase'] for x in d['items'] if x['id']=='$RID'])")" "['done']"
  check "mirror complete again" "$(vm "cat /proc/mdstat")" "[UU]"
  VM_DETACH=sys restart_vm
  check "starts from the new disk alone" "$(vm "findmnt -n -o SOURCE /; cat /proc/mdstat")" "[U_]"
}

part_move() {
  printf '\n\033[1;36m== System backup and move of the system (BIOS, ext4)\033[0m\n'
  fresh_vm
  H1=$(dev STRATA-HDD-1) H2=$(dev STRATA-HDD-2)
  check "data volume for the backup" "$(job "$(c POST /api/storage/volumes -d '{"name":"bk","fsType":"ext4","devices":["'"$H2"'"]}')")" "success"
  check "backup on the system disk refused" "$(c POST /api/system/disk/backup -d '{"folder":"/srv/strata"}')" "data volume"
  check "system backup" "$(job "$(c POST /api/system/disk/backup -d '{"folder":"/srv/strata/bk"}')")" "success"
  check "manifest written" "$(vm "sudo sh -c 'cat /srv/strata/bk/strataos-system-backup/*/manifest.json'")" '"sha256"'
  check "archive readable" "$(vm "sudo sh -c 'tar -tf /srv/strata/bk/strataos-system-backup/*/system.tar.* | grep -c -E \"^./(etc/fstab|var/lib/strata/state.json)\$\"'")" "2"
  check "data volume refused as target" "$(c GET "/api/system/disk/plan?kind=move&target=$H2" | py 'print(d["refusal"])')" "in use"
  # A file the copy must carry.
  vm "echo moved-ok | sudo tee /root/marker >/dev/null"
  R=$(c POST /api/system/disk/start -d '{"kind":"move","target":"'"$H1"'","confirm":"'"${H1#/dev/}"'"}')
  check "copy onto the new disk" "$(job "$R")" "success"
  vm "echo after-copy | sudo tee /root/late >/dev/null"
  ID=$(op 'o["id"]')
  check "switch prepared" "$(job "$(c POST "/api/system/disk/$ID/switch")")" "success"
  restarted
  check "started from the new disk" "$(op 'o["phase"]')" "booted"
  check "/ on the new disk" "$(vm "findmnt -n -o SOURCE /")" "$H1"
  check "files copied, last changes too" "$(vm "sudo cat /root/marker /root/late")" "after-copy"
  check "data volume still mounted" "$(vm "findmnt -n -o TARGET /srv/strata/bk")" "/srv/strata/bk"
  check "move finished" "$(job "$(c POST "/api/system/disk/$ID/complete" -d '{}')")" "success"
  VM_DETACH=sys restart_vm
  check "starts without the old disk" "$(vm "findmnt -n -o SOURCE /")" "/dev/sd"
  check "StrataOS settings kept" "$(c GET /api/system/info | py 'print(d["hostname"])')" "sysdisk-vm"
}

parts=("$@")
[ ${#parts[@]} -gt 0 ] || parts=(mirror move)
for p in "${parts[@]}"; do
  case "$p" in
    mirror) part_mirror ;;
    move) part_move ;;
    *) echo "unknown part $p"; exit 1 ;;
  esac
done
./vm/vm.sh stop >/dev/null 2>&1
echo
[ "$FAILS" -eq 0 ] && echo "all checks passed" || echo "$FAILS check(s) failed"
[ "$FAILS" -eq 0 ]
