#!/bin/bash
# End-to-end test of StrataOS against a FRESH test VM (runs the setup wizard).
# Exits non-zero if any check fails. Credentials below are test-only.
#   STRATA_URL (default http://localhost:7070)
set -uo pipefail
cd "$(dirname "$0")/.."
B=${STRATA_URL:-http://localhost:7070}
ADMIN_USER=admin ADMIN_PASS=Strata-Test-2026
USER_NAME=alice USER_PASS=Alice-Pass-1
J=$(mktemp) J2=$(mktemp) TMP=$(mktemp -d)
SINKS_PID=""
trap 'rm -rf "$J" "$J2" "$TMP"; [ -n "$SINKS_PID" ] && kill "$SINKS_PID" 2>/dev/null' EXIT
FAILS=0 PASSES=0

c() { local m=$1 p=$2; shift 2; curl -s -b "$J" -c "$J" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
u() { local m=$1 p=$2; shift 2; curl -s -b "$J2" -c "$J2" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
ok() { PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1"; }
ko() { FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    %s\n' "$1" "${2:-}"; }
# check "description" "actual" "expected substring"
check() { case "$2" in *"$3"*) ok "$1" ;; *) ko "$1" "expected '$3', got: ${2:0:300}" ;; esac; }
vm() { ./vm/vm.sh ssh "$@" 2>/dev/null; }
job() { # waits for a job returned by the API and prints its final status
  local id; id=$(echo "$1" | py 'print(d.get("id",""))' 2>/dev/null)
  [ -n "$id" ] || { echo "no-job: $1"; return; }
  for _ in $(seq 1 300); do
    st=$(c GET "/api/jobs/$id" | py 'print(d["status"])'); [ "$st" != running ] && break; sleep 2
  done
  c GET "/api/jobs/$id" | py 'print(d["status"], d.get("error",""))'
}
raid_health() { c GET /api/storage/raid | py 'print(d[0]["health"] if d else "none")' 2>/dev/null; }
wait_api() { for _ in $(seq 1 90); do curl -sf "$B/api/setup" >/dev/null && return; sleep 2; done; }
section() { printf '\n\033[1;36m== %s\033[0m\n' "$1"; }

section "Setup & auth"
check "setup wizard" "$(c POST /api/setup -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"fullName\":\"Test\",\"hostname\":\"strata-vm\"}")" '"role":"admin"'
check "first admin can use sudo" "$(vm "id $ADMIN_USER")" "(sudo)"
check "setup cannot run twice" "$(c POST /api/setup -d '{"username":"x","password":"xxxxxxxxx"}')" "already"
check "bad password rejected" "$(curl -s -X POST -H 'X-Strata: 1' $B/api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"wrong\"}")" "invalid"
check "cross-origin write rejected" "$(curl -s -b "$J" -X POST -H 'Origin: http://evil.test' $B/api/groups -d '{"name":"x"}')" "cross-origin"
check "disks detected" "$(c GET /api/storage/disks | py 'print(sum(1 for x in d if x["usage"]=="free" and not x["serial"].startswith("STRATA-VD-1")))')" "9"

section "Storage"
# Kernel names (/dev/sdX) change with the number of disks and between boots: use serials.
dev() { c GET /api/storage/disks | py "print([x['path'] for x in d if x['serial']=='$1'][0])"; }
H1=$(dev STRATA-HDD-1) H2=$(dev STRATA-HDD-2) H3=$(dev STRATA-HDD-3) H4=$(dev STRATA-HDD-4) H5=$(dev STRATA-HDD-5) H6=$(dev STRATA-HDD-6)
check "RAID 5 created" "$(job "$(c POST /api/storage/raid -d '{"name":"data","level":"5","devices":["'"$H1"'","'"$H2"'","'"$H3"'"]}')")" "success"
check "ext4 volume on RAID" "$(job "$(c POST /api/storage/volumes -d '{"name":"main","fsType":"ext4","devices":["/dev/md/data"]}')")" "success"
check "btrfs RAID1 volume" "$(job "$(c POST /api/storage/volumes -d '{"name":"fast","fsType":"btrfs","devices":["'"$H4"'","'"$H5"'"],"dataProfile":"raid1","compression":true}')")" "success"
check "volumes mounted" "$(c GET /api/storage/volumes | py 'print(sorted(v["name"] for v in d if v["mounted"]))')" "['fast', 'main']"
check "LVM group" "$(c POST /api/storage/lvm -d '{"name":"vg0","devices":["'"$H6"'"]}')" '"ok":true'
check "LVM volume" "$(c POST /api/storage/lvm/vg0/lv -d '{"name":"lv0","size":1073741824}')" '"ok":true'
SSD=$(dev STRATA-SSD-1)
check "SSD seen as non-rotational" "$(c GET /api/storage/disks | py "print([x['rotational'] for x in d if x['serial']=='STRATA-SSD-1'][0])")" "False"
check "SSD cache attached" "$(job "$(c POST /api/storage/lvm/vg0/lv/lv0/cache -d '{"device":"'"$SSD"'","mode":"writethrough"}')")" "success"
check "cache listed" "$(c GET /api/storage/lvm | py 'print([(l["cache"]["mode"], l["cache"]["device"]) for g in d for l in g["lvs"] if l.get("cache")])')" "('writethrough', '$SSD')"
check "SSD cache removed" "$(job "$(c DELETE /api/storage/lvm/vg0/lv/lv0/cache)")" "success"
check "SSD free again" "$(c GET /api/storage/disks | py "print([x['usage'] for x in d if x['serial']=='STRATA-SSD-1'][0])")" "free"
check "LVM cleanup" "$(c DELETE /api/storage/lvm/vg0/lv/lv0; c DELETE /api/storage/lvm/vg0)" '{"ok":true}'
check "volume created with an SSD cache" "$(job "$(c POST /api/storage/volumes -d '{"name":"cached","fsType":"ext4","devices":["'"$H6"'"],"cacheDevice":"'"$SSD"'","cacheMode":"writethrough"}')")" "success"
check "cached volume mounted" "$(c GET /api/storage/volumes | py 'print([(v["mounted"], v.get("managedVg")) for v in d if v["name"]=="cached"])')" "(True, 'vol_cached')"
check "cache active under the volume" "$(vm "sudo lvs --noheadings -o segtype,cache_mode vol_cached/data")" "cache"
CV=$(c GET /api/storage/volumes | py 'print([v["id"] for v in d if v["name"]=="cached"][0])')
check "cached volume deleted" "$(c DELETE "/api/storage/volumes/$CV?wipe=1")" '"ok":true'
# udev may take a moment to see the freed SSD.
for _ in $(seq 1 10); do [ "$(c GET /api/storage/disks | py "print(sorted(x['usage'] for x in d if x['serial'] in ('STRATA-SSD-1','STRATA-HDD-6')))")" = "['free', 'free']" ] && break; sleep 2; done
check "disk and SSD freed" "$(c GET /api/storage/disks | py "print(sorted(x['usage'] for x in d if x['serial'] in ('STRATA-SSD-1','STRATA-HDD-6')))")" "['free', 'free']"
check "refuses system disk" "$(c POST /api/storage/disks/wipe -d '{"device":"/dev/vda"}')" "system disk"

section "Users & shares"
check "group" "$(c POST /api/groups -d '{"name":"famille"}')" '"ok":true'
check "user" "$(c POST /api/users -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\",\"role\":\"user\",\"groups\":[\"famille\"]}")" '"ok":true'
check "SMB/NFS share" "$(c POST /api/shares -d '{"name":"Photos","path":"/srv/strata/main/Photos","smb":true,"nfs":true,"browseable":true,"recycleBin":true,"permissions":{"@famille":"rw"},"nfsClients":"10.0.2.0/24"}')" '"name":"Photos"'
check "share outside volumes refused" "$(c POST /api/shares -d '{"name":"Root","path":"/etc","smb":true}')" "error"
check "user login" "$(u POST /api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}")" '"role":"user"'
check "user cannot list users" "$(u GET /api/users)" "administrator rights required"
check "SMB write as user" "$(vm "command -v smbclient >/dev/null || sudo apt-get install -y -qq smbclient >/dev/null 2>&1; smbclient //localhost/Photos -U $USER_NAME%$USER_PASS -c 'put /etc/hostname via-smb.txt; ls' 2>&1")" "via-smb.txt"
check "NFS export" "$(vm "sudo exportfs")" "/srv/strata/main/Photos"
check "fs ACL: no blanket group access" "$(vm "sudo getfacl -p /srv/strata/main/Photos | grep '^group::'")" "group::---"

section "File manager security"
P=/srv/strata/main/Photos
vm "sudo ln -sfn /etc $P/evil; sudo ln -sfn /etc/shadow $P/leak"
echo pwned > "$TMP/pwn.txt"; printf '<script>alert(1)</script>' > "$TMP/x.html"
check "user lists own share" "$(u GET "/api/files?path=$P")" "via-smb.txt"
check "symlink dir escape blocked" "$(u GET "/api/files?path=$P/evil")" "error"
check "symlink file escape blocked" "$(u GET "/api/files/download?path=$P/leak")" "error"
check "upload through symlink blocked" "$(curl -s -b "$J2" -H 'X-Strata: 1' -F "file=@$TMP/pwn.txt" "$B/api/files/upload?path=$P/evil")" "error"
curl -s -b "$J2" -H 'X-Strata: 1' -F "file=@$TMP/pwn.txt;filename=../../../../etc/pwn.txt" "$B/api/files/upload?path=$P" >/dev/null
check "traversal filename neutralised" "$(vm 'test -e /etc/pwn.txt && echo LEAKED || echo clean')" "clean"
curl -s -b "$J2" -H 'X-Strata: 1' -F "file=@$TMP/x.html" "$B/api/files/upload?path=$P" >/dev/null
check "HTML never served inline" "$(curl -s -D - -o /dev/null -b "$J2" "$B/api/files/download?path=$P/x.html&inline=1" | tr -d '\r')" "Content-Disposition: attachment"
check "user outside share denied" "$(u GET "/api/files?path=/srv/strata/fast")" "denied"
check "share on symlink refused" "$(c POST /api/shares -d "{\"name\":\"Evil\",\"path\":\"$P/evil\",\"smb\":true}")" "symbolic link"
check "refused share not persisted" "$(c GET /api/shares | py 'print("Evil" in [x["name"] for x in d])')" "False"
curl -s -b "$J2" -H 'X-Strata: 1' -F "file=@$TMP/pwn.txt;filename=album/2026/photo.txt" "$B/api/files/upload?path=$P" >/dev/null
check "folder upload keeps sub-folders" "$(u GET "/api/files?path=$P/album/2026")" "photo.txt"
vm "sudo rm -f $P/evil $P/leak"

section "Injection & limits"
check "NFS option injection refused" "$(c POST /api/shares -d '{"name":"Inj","path":"/srv/strata/main/Inj","nfs":true,"nfsClients":"*","nfsOptions":"rw) *(rw,no_root_squash"}')" "invalid NFS"
check "NFS default-options client refused" "$(c POST /api/shares -d '{"name":"Inj","path":"/srv/strata/main/Inj","nfs":true,"nfsClients":"-rw,no_root_squash"}')" "invalid NFS"
check "no_root_squash never exported" "$(vm "sudo exportfs -v | grep -c no_root_squash")" "0"
check "smb.conf %-substitution path refused" "$(c POST /api/shares -d '{"name":"Inj","path":"/srv/strata/main/%H","smb":true}')" "not allowed"
check "RAID fail of a non-member refused" "$(c POST /api/storage/raid/data/fail -d '{"device":"--stop"}')" "not a member"
check "ZFS device option refused" "$(c POST /api/storage/zfs/tank/offline -d '{"device":"-t"}')" "invalid device"
check "privileged group refused" "$(c POST /api/users -d '{"username":"bob","password":"Bob-Pass-12","groups":["sudo"]}')" "reserved"
check "user does not see admin jobs" "$(u GET /api/jobs)" "[]"
python3 -c 'print("{\"username\":\"" + "a"*2000000 + "\"}")' > "$TMP/big.json"
check "oversized login body refused" "$(curl -s -X POST -H 'X-Strata: 1' --data-binary @"$TMP/big.json" $B/api/login)" "too large"
check "cross-origin websocket refused" "$(curl -s -o /dev/null -w '%{http_code}' -b "$J" -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -H 'Origin: http://evil.test' "$B/api/system/terminal")" "403"

section "Docker & apps"
check "docker engine" "$(c GET /api/docker/info | py 'print(d["available"])')" "True"
check "install Uptime Kuma" "$(job "$(c POST /api/apps/uptimekuma/install -d '{"name":"uptime","values":{"DATA_DIR":"/srv/strata/main/apps/uptime-kuma"}}')")" "success"
APPUPD=$(c POST /api/schedules -d '{"name":"Apps","type":"appupdate","target":"all","cron":"0 4 * * 0","enabled":true}' | py 'print(d["id"])')
check "app update task" "$(job "$(c POST /api/schedules/$APPUPD/run)")" "success"
check "app update summary" "$(c GET /api/schedules | py '[print(s["history"][0]["message"]) for s in d if s["id"]=="'$APPUPD'"]')" "1 app(s) checked"
c DELETE /api/schedules/$APPUPD >/dev/null
check "container running" "$(c GET /api/docker/containers | py 'print([x["State"] for x in d if "uptime" in x["Names"][0]])')" "running"
check "app port reported" "$(c GET /api/apps | py 'print(d["ports"])')" "3001"

section "ZFS"
check "install ZFS (DKMS build)" "$(job "$(c POST /api/system/components/zfs/install)")" "success"
VD7=$(c GET /api/storage/disks | py 'print([x["path"] for x in d if x["serial"]=="STRATA-VD-7"][0])')
VD8=$(c GET /api/storage/disks | py 'print([x["path"] for x in d if x["serial"]=="STRATA-VD-8"][0])')
check "ZFS mirror pool" "$(job "$(c POST /api/storage/zfs -d "{\"name\":\"tank\",\"vdevs\":[{\"type\":\"mirror\",\"disks\":[\"$VD7\",\"$VD8\"]}]}")")" "success"
check "pool online" "$(c GET /api/storage/zfs | py 'print(d["pools"][0]["health"])')" "ONLINE"
check "dataset" "$(c POST /api/storage/zfs-datasets -d '{"name":"tank/media","compression":"zstd"}')" '"ok":true'
ZID=$(c POST /api/schedules -d '{"name":"z","type":"snapshot","cron":"0 * * * *","enabled":true,"targetKind":"zfs","target":"tank/media","keepCount":2}' | py 'print(d["id"])')
for _ in 1 2 3; do job "$(c POST /api/schedules/$ZID/run)" >/dev/null; sleep 1.1; done
check "ZFS retention keeps 2" "$(c GET '/api/storage/zfs-snapshots?pool=tank' | py 'print(sum(1 for x in d if "@auto-" in x["name"]))')" "2"
check "ZFS scrub task" "$(job "$(c POST /api/schedules/$(c POST /api/schedules -d '{"name":"zs","type":"scrub","cron":"0 2 8 * *","enabled":true,"targetKind":"zfs","target":"tank"}' | py 'print(d["id"])')/run)")" "success"
check "share on ZFS dataset" "$(c POST /api/shares -d '{"name":"Media","path":"/srv/strata/tank/media","smb":true,"browseable":true}')" '"name":"Media"'


section "Debian release"
check "Windows discovery service" "$(vm "systemctl is-active -q wsdd2.service || systemctl is-active -q wsdd-server.service && echo running")" "running"
check "directory clients install" "$(job "$(c POST /api/system/components/directory/install)")" "success"
check "Active Directory client present" "$(c GET /api/directory | py 'print(d["adInstalled"])')" "True"
check "Samba configuration valid" "$(vm "sudo testparm -s >/dev/null 2>/tmp/tp; echo rc=\$?")" "rc=0"
check "nftables address blocking loads" "$(vm "sudo nft list tables" | tr '\n' ' ')" "table"
echo "  (info) LDAP client: $(c GET /api/directory | py 'print(d["ldapInstalled"])'), rclone: $(vm 'command -v rclone || echo missing')"
echo "  (info) $(vm 'samba --version; nfsstat --version 2>&1 | head -1; docker --version; zfs version | head -1; systemctl --version | head -1; python3 --version' | tr '\n' ';')"

printf '\n%s passed, %s failed\n' "$PASSES" "$FAILS"
[ "$FAILS" -eq 0 ]
