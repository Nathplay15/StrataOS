#!/bin/bash
# System disk protection on systems installed from the ISO (Btrfs system):
#
#   vm/sysdisk-iso-test.sh mirror-install [uefi|bios]
#       "System on two disks (mirror)" entry, start with each disk unplugged,
#       replace a disk of the mirror with a blank one (replacement wizard)
#   vm/sysdisk-iso-test.sh move-mirror [uefi|bios]
#       regular installation, move the system to data disk 2, start without
#       the old disk, then mirror it onto data disk 1, start from disk 1 alone
#   vm/sysdisk-iso-test.sh restore [uefi|bios] <data-disk.qcow2>
#       "Restore a StrataOS system backup" entry with that disk attached
#
# Builds test ISOs (root SSH key, entry started by default), uses its own
# state folder (ISO_STATE, default vm/state-sysdisk-iso) and ports
# (ISO_WEB_PORT, ISO_SSH_PORT). Credentials are test-only.
set -uo pipefail
cd "$(dirname "$0")/.."
VERSION=$(cat VERSION)
export ISO_STATE=${ISO_STATE:-$PWD/vm/state-sysdisk-iso} ISO_MEM=${ISO_MEM:-2048}
WEB=${ISO_WEB_PORT:-7071} SSHP=${ISO_SSH_PORT:-2223}
B=http://localhost:$WEB
KEYDIR=$PWD/vm/state-sysdisk-key
J=$(mktemp)
FAILS=0
trap 'rm -f "$J"; ./vm/iso-test.sh stop' EXIT
mkdir -p "$KEYDIR"
[ -f "$KEYDIR/id" ] || ssh-keygen -q -t ed25519 -N "" -C sysdisk-test -f "$KEYDIR/id"
c() { local m=$1 p=$2; shift 2; curl -s -m 60 -b "$J" -c "$J" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
check() { case "$2" in *"$3"*) printf '  \033[32m✔\033[0m %s\n' "$1" ;; *) FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    expected %q, got: %s\n' "$1" "$3" "${2:0:400}" ;; esac; }
vm() { ssh -i "$KEYDIR/id" -p "$SSHP" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5 root@127.0.0.1 "$@" 2>/dev/null; }
login() { c POST /api/login -d '{"username":"admin","password":"Strata-Test-2026"}' >/dev/null; }
wait_api() { for _ in $(seq 1 150); do curl -sf -m 3 "$B/api/setup" >/dev/null && { sleep 5; login; return 0; }; sleep 4; done; echo "no answer"; return 1; }
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
restarted() {
  for _ in $(seq 1 60); do curl -sf -m 2 "$B/api/setup" >/dev/null || break; sleep 2; done
  wait_api
}
boot() { # ISO_DETACH as given
  ./vm/iso-test.sh stop; sleep 3
  ./vm/iso-test.sh boot && wait_api
}
iso() { # entry [bootargs]
  local out=dist/strataos-${VERSION}-amd64.iso
  [ "$1" = install ] || out=dist/strataos-${VERSION}-amd64-$1-test.iso
  ISO_TEST_SSH_KEY="$KEYDIR/id.pub" ISO_TEST_DEFAULT=$1 ISO_TEST_BOOTARGS=${2:-} ARCH=amd64 ./packaging/iso/build-iso.sh >/dev/null || { echo "ISO build failed"; exit 1; }
  echo "$out"
}
install() { # iso extra mode
  ./vm/iso-test.sh "$1" "$2" "$3" >/dev/null || { echo "installation failed"; tail -5 "$ISO_STATE/console.log"; exit 1; }
  c POST /api/setup -d '{"username":"admin","password":"Strata-Test-2026","fullName":"Test","hostname":"sysdisk-iso"}' >/dev/null
  login
}

mirror_install() {
  local mode=$1
  printf '\n\033[1;36m== ISO: system on two disks (%s)\033[0m\n' "$mode"
  ISO_SYS_DISKS=2 install "$(iso mirror strata_mirror_disks=sda,sdb)" 1 "$mode"
  check "installed on the mirror" "$(vm "findmnt -n -o SOURCE,FSTYPE /")" "/dev/md"
  check "both arrays complete" "$(vm "grep -c '\[UU\]' /proc/mdstat")" "2"
  check "layout: mirrored" "$(c GET /api/system/disk | py 'l=d["layout"]; print(l["mirrored"], l["boot"], len(l["disks"]), l["root"]["fstype"])')" "True $mode 2 btrfs"
  if [ "$mode" = uefi ]; then
    # One of them is mounted on /boot/efi: mounted again (same options), not read-only.
    check "both disks have an ESP with GRUB" "$(vm "for d in /dev/sda1 /dev/sdb1; do mount \$d /mnt && ls /mnt/EFI/debian/ | grep -c '^grub.*\.efi$'; umount /mnt; done | tr '\n' ' '")" "1 1"
    check "both ESPs have the same volume ID" "$(vm "blkid -c /dev/null -o value -s UUID /dev/sda1 /dev/sdb1 | sort -u | wc -l")" "1"
    check "/boot/efi optional at start" "$(vm "findmnt -s -n -o OPTIONS /boot/efi")" "nofail"
    check "boot entry of the second disk" "$(vm "efibootmgr")" "StrataOS (mirror"
  else
    check "GRUB on both disks" "$(vm "for d in sda sdb; do dd if=/dev/\$d bs=512 count=1 2>/dev/null | grep -c GRUB; done | tr '\n' ' '")" "1 1"
  fi
  check "RAID page: system arrays listed" "$(c GET /api/system/disk | py 'print(sorted(a["role"] for a in d["layout"]["arrays"]))')" "['root', 'swap']"
  check "advisor: no system disk item" "$(c POST /api/security/advisor/run | py 'print([i["id"] for i in d["items"] if i["id"]=="system-disk"])')" "[]"
  ISO_DETACH=sys boot
  check "starts with disk 1 unplugged" "$(vm "findmnt -n -o SOURCE /; cat /proc/mdstat")" "[2/1]"
  check "disk 1 unplugged: settings there" "$(curl -s "$B/api/setup")" '"needed":false'
  [ "$mode" = uefi ] && check "disk 1 unplugged: /boot/efi mounted from disk 2" "$(vm "findmnt -n -o FSTYPE /boot/efi")" "vfat"
  ISO_DETACH=sys2 boot
  check "starts with disk 2 unplugged" "$(vm "findmnt -n -o SOURCE /; cat /proc/mdstat")" "[2/1]"
  check "disk 2 unplugged: settings there" "$(curl -s "$B/api/setup")" '"needed":false'
  [ "$mode" = uefi ] && check "disk 2 unplugged: /boot/efi mounted from disk 1" "$(vm "findmnt -n -o FSTYPE /boot/efi")" "vfat"
  # Disk 1 dies: a blank disk takes its place.
  ./vm/iso-test.sh stop; sleep 3
  rm -f "$ISO_STATE/system.qcow2"; qemu-img create -q -f qcow2 "$ISO_STATE/system.qcow2" 16G
  ISO_DETACH="" boot
  check "degraded mirror seen" "$(c GET /api/system/disk | py 'print(d["layout"]["degraded"])')" "True"
  check "alert of the system mirror" "$(c GET /api/system/overview | grep -o 'system mirror is degraded' | head -1)" "system mirror is degraded"
  NEW=$(dev STRATA-SYS)
  check "plan of the replacement" "$(c GET "/api/storage/replace/plan?group=sys:mirror&new=$NEW" | py 'p=d["plan"]; print(p.get("refusal",""), p["mode"], p["group"]["kind"])')" " degraded system"
  R=$(c POST /api/storage/replace -d '{"group":"sys:mirror","new":"'"$NEW"'","confirm":"'"${NEW#/dev/}"'"}')
  check "replacement finished" "$(job "$R")" "success"
  joblog "$R" | grep -E '^\$ ' | head -12
  check "mirror complete again" "$(vm "grep -c '\[UU\]' /proc/mdstat")" "2"
  ISO_DETACH=sys2 boot
  check "starts from the replaced disk alone" "$(vm "findmnt -n -o SOURCE /; cat /proc/mdstat")" "[2/1]"
}

move_mirror() {
  local mode=$1
  printf '\n\033[1;36m== ISO: move the system, then mirror it (%s)\033[0m\n' "$mode"
  # With data disks the installer asks for the system disk: answered here.
  install "$(iso install partman-auto/disk=/dev/sda)" 2 "$mode"
  D1=$(dev STRATA-DATA-1) D2=$(dev STRATA-DATA-2)
  check "layout: Btrfs on a partition" "$(c GET /api/system/disk | py 'l=d["layout"]; print(l["boot"], l["root"]["fstype"], l["root"]["subvol"], l["rootOn"], [m["role"] for m in l["mounts"]])')" "$mode btrfs @rootfs part"
  vm "echo before-move > /root/marker"
  R=$(c POST /api/system/disk/start -d '{"kind":"move","target":"'"$D2"'","confirm":"'"${D2#/dev/}"'"}')
  check "move: copy" "$(job "$R")" "success"
  ID=$(op 'o["id"]')
  R=$(c POST "/api/system/disk/$ID/switch")
  check "move: switch" "$(job "$R")" "success"
  joblog "$R" | grep -E 'efibootmgr|grub-install|grub-reboot' | head -6
  restarted
  check "move: started from the new disk" "$(op 'o["phase"]')" "booted"
  check "move: / on data disk 2" "$(vm "findmnt -n -o SOURCE /")" "${D2}"
  check "move: settings subvolume" "$(vm "findmnt -n -o SOURCE /var/lib/strata")" "@strata"
  check "move: files kept" "$(vm "cat /root/marker")" "before-move"
  check "move: finished" "$(job "$(c POST "/api/system/disk/$ID/complete" -d '{}')")" "success"
  ISO_DETACH=sys boot
  check "starts without the old system disk" "$(vm "findmnt -n -o SOURCE /")" "/dev/sd"
  D1=$(dev STRATA-DATA-1) D2=$(dev STRATA-DATA-2)
  R=$(c POST /api/system/disk/start -d '{"kind":"mirror","target":"'"$D1"'","confirm":"'"${D1#/dev/}"'"}')
  check "mirror: copy" "$(job "$R")" "success"
  ID=$(op 'o["id"]')
  check "mirror: switch" "$(job "$(c POST "/api/system/disk/$ID/switch")")" "success"
  restarted
  check "mirror: started from the array" "$(vm "findmnt -n -o SOURCE /")" "/dev/md"
  check "mirror: completed" "$(job "$(c POST "/api/system/disk/$ID/complete" -d '{"confirm":"'"${D2#/dev/}"'"}')")" "success"
  check "mirror: both disks" "$(vm "grep -c '\[UU\]' /proc/mdstat")" "2"
  [ "$mode" = uefi ] && check "mirror: boot entries for both disks" "$(vm "efibootmgr | grep -c StrataOS")" "2"
  ISO_DETACH="sys 2" boot
  check "mirror: starts from data disk 1 alone" "$(vm "findmnt -n -o SOURCE /; cat /proc/mdstat")" "[2/1]"
  ISO_DETACH="sys 1" boot
  check "mirror: starts from data disk 2 alone" "$(vm "findmnt -n -o SOURCE /; cat /proc/mdstat")" "[2/1]"
}

restore() {
  local mode=$1 disk=$2
  printf '\n\033[1;36m== ISO: restore a system backup (%s)\033[0m\n' "$mode"
  ISO_DATA1=$(realpath "$disk") install "$(iso restore partman-auto/disk=/dev/sda)" 1 "$mode"
  check "restore log" "$(vm "cat /var/log/strataos-restore.log")" "strata-restore: done"
  check "settings of the backup (setup done)" "$(curl -s "$B/api/setup")" '"needed":false'
  check "host name of the backup" "$(vm hostname)" "sysdisk-vm"
  check "data volume of the backup mounted" "$(vm "findmnt -n -o TARGET /srv/strata/bk")" "/srv/strata/bk"
  check "system on the new disk" "$(vm "findmnt -n -o SOURCE,FSTYPE /")" "btrfs"
}

case "${1:-}" in
  mirror-install) mirror_install "${2:-uefi}" ;;
  move-mirror) move_mirror "${2:-uefi}" ;;
  restore) restore "${2:-uefi}" "${3:?data disk}" ;;
  *) sed -n '2,15p' "$0"; exit 1 ;;
esac
echo
[ "$FAILS" -eq 0 ] && echo "all checks passed" || echo "$FAILS check(s) failed"
[ "$FAILS" -eq 0 ]
