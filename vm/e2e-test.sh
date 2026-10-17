#!/bin/bash
# End-to-end test of StrataOS against a FRESH test VM (runs the setup wizard).
# Exits non-zero if any check fails. Credentials below are test-only.
#   STRATA_URL (default http://localhost:7070)
set -uo pipefail
cd "$(dirname "$0")/.."
B=${STRATA_URL:-http://localhost:7070}
ADMIN_USER=nathan ADMIN_PASS=Strata-Test-2026
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
check "setup cannot run twice" "$(c POST /api/setup -d '{"username":"x","password":"xxxxxxxxx"}')" "already"
check "bad password rejected" "$(curl -s -X POST -H 'X-Strata: 1' $B/api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"wrong\"}")" "invalid"
check "cross-origin write rejected" "$(curl -s -b "$J" -X POST -H 'Origin: http://evil.test' $B/api/groups -d '{"name":"x"}')" "cross-origin"
check "disks detected" "$(c GET /api/storage/disks | py 'print(sum(1 for x in d if x["usage"]=="free"))')" "8"

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
check "LVM cleanup" "$(c DELETE /api/storage/lvm/vg0/lv/lv0; c DELETE /api/storage/lvm/vg0)" '{"ok":true}'
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

section "Notifications"
openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=Strata test CA" -keyout "$TMP/ca.key" -out "$TMP/ca.pem" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -subj "/CN=10.0.2.2" -keyout "$TMP/srv.key" -out "$TMP/srv.csr" 2>/dev/null
printf 'subjectAltName=IP:10.0.2.2\n' > "$TMP/san.ext"
openssl x509 -req -in "$TMP/srv.csr" -CA "$TMP/ca.pem" -CAkey "$TMP/ca.key" -CAcreateserial -days 2 -extfile "$TMP/san.ext" -out "$TMP/srv.pem" 2>/dev/null
vm "echo '$(cat "$TMP/ca.pem")' | sudo tee /usr/local/share/ca-certificates/strata-test.crt >/dev/null && sudo update-ca-certificates >/dev/null 2>&1"
python3 -I vm/test-sinks.py "$TMP/sinks" --cert "$TMP/srv.pem" --key "$TMP/srv.key" > "$TMP/sinks.out" 2>&1 &
SINKS_PID=$!
for _ in $(seq 1 20); do grep -q ready "$TMP/sinks.out" 2>/dev/null && break; sleep 0.5; done
notif() { # notif <port> <security> -> result of the test notification
  c GET /api/settings | py "d.update(webhookUrl='http://10.0.2.2:8720/hook', smtpHost='10.0.2.2', smtpPort=$1, smtpSecurity='$2', smtpUser='', smtpFrom='nas@strata.test', notifyEmail='admin@strata.test'); print(json.dumps(d))" | c PUT /api/settings -d @- >/dev/null
  c POST /api/settings/test-notification
}
check "SMTP implicit TLS + webhook" "$(notif 8465 tls)" '"ok":true'
check "mail received over TLS" "$(cat "$TMP/sinks/mail.log" 2>/dev/null)" "[tls]"
check "webhook received" "$(cat "$TMP/sinks/webhook.log" 2>/dev/null)" "Test de notification"
check "STARTTLS required but not offered -> error" "$(notif 8525 starttls)" "STARTTLS"
check "plain SMTP (local network)" "$(notif 8525 none)" '"ok":true'
check "mail received in plain" "$(cat "$TMP/sinks/mail.log")" "[plain]"
c GET /api/settings | py "d.update(webhookUrl='', smtpHost='', notifyEmail=''); print(json.dumps(d))" | c PUT /api/settings -d @- >/dev/null
kill "$SINKS_PID" 2>/dev/null; SINKS_PID=""

section "Quotas"
MAINID=$(c GET /api/storage/volumes | py 'print([v["id"] for v in d if v["name"]=="main"][0])')
check "ext4 volume created with enforced quotas" "$(c GET /api/quotas/targets | py "print([t['enabled'] for t in d if t['target']=='$MAINID'])")" "[True]"
check "set 20 MB quota" "$(c PUT /api/users/$USER_NAME/quotas -d "{\"kind\":\"ext4\",\"target\":\"$MAINID\",\"limit\":20971520}")" '"ok":true'
check "quota enforced" "$(vm "sudo -u $USER_NAME dd if=/dev/zero of=$P/q.bin bs=1M count=40 2>&1; sudo rm -f $P/q.bin")" "quota exceeded"
check "ZFS user quota" "$(c PUT /api/users/$USER_NAME/quotas -d '{"kind":"zfs","target":"tank/media","limit":104857600}'; vm "sudo zfs get -H -o value userquota@$USER_NAME tank/media")" "100M"
check "btrfs quota refused" "$(c PUT /api/users/$USER_NAME/quotas -d "{\"kind\":\"btrfs\",\"target\":\"$(c GET /api/storage/volumes | py 'print([v["id"] for v in d if v["name"]=="fast"][0])')\",\"limit\":1}")" "not available"
c PUT /api/users/$USER_NAME/quotas -d "{\"kind\":\"ext4\",\"target\":\"$MAINID\",\"limit\":0}" >/dev/null

section "Backups"
vm "sudo mkdir -p $P/2026 && echo souvenir | sudo tee $P/2026/plage.txt >/dev/null"
BK=$(c POST /api/backups -d "{\"name\":\"copie\",\"kind\":\"rsync\",\"sources\":[\"$P\"],\"destType\":\"local\",\"destPath\":\"/srv/strata/fast/sauvegardes\",\"cron\":\"0 1 * * *\",\"enabled\":true}" | py 'print(d["id"])')
check "rsync copy" "$(job "$(c POST /api/backups/$BK/run)")" "success"
check "copied file" "$(vm "sudo cat /srv/strata/fast/sauvegardes/Photos/2026/plage.txt")" "souvenir"
RS=$(c POST /api/backups -d "{\"name\":\"versions\",\"kind\":\"restic\",\"sources\":[\"$P\"],\"destType\":\"local\",\"destPath\":\"/srv/strata/fast/restic\",\"password\":\"Restic-Test-Pass\",\"cron\":\"\",\"enabled\":true}" | py 'print(d["id"])')
check "restic backup" "$(job "$(c POST /api/backups/$RS/run)")" "success"
check "password masked" "$(c GET /api/backups | py "print([b['password'] for b in d if b['id']=='$RS'][0])")" "********"
SNAP=$(c GET /api/backups/$RS/snapshots | py 'print(d[0]["id"])')
check "restic restore" "$(job "$(c POST /api/backups/$RS/restore -d "{\"snapshot\":\"$SNAP\",\"target\":\"/srv/strata/fast/restore\"}")")" "success"
check "restored file" "$(vm "sudo cat /srv/strata/fast/restore$P/2026/plage.txt")" "souvenir"
ZR=$(c POST /api/backups -d '{"name":"repl","kind":"zfs","sources":["tank/media"],"destType":"local","destPath":"tank/backup/media","cron":"","enabled":true,"keepLast":2}' | py 'print(d["id"])')
check "ZFS replication (full)" "$(job "$(c POST /api/backups/$ZR/run)")" "success"
sleep 1.2
check "ZFS replication (incremental)" "$(job "$(c POST /api/backups/$ZR/run)")" "success"
check "replica exists" "$(vm "sudo zfs list -H -o name tank/backup/media")" "tank/backup/media"

section "Two-factor authentication"
SEC=$(u POST /api/me/2fa/setup | py 'print(d["secret"])')
check "2FA enabled" "$(u POST /api/me/2fa/enable -d "{\"code\":\"$(python3 -I vm/totp.py $SEC)\"}" | py 'print(len(d["recoveryCodes"]))')" "8"
check "password alone refused" "$(curl -s -X POST -H 'X-Strata: 1' $B/api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}")" '"twoFactor":true'
sleep 31
check "login with code" "$(curl -s -X POST -H 'X-Strata: 1' $B/api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\",\"code\":\"$(python3 -I vm/totp.py $SEC)\"}")" '"role":"user"'
check "admin reset" "$(c POST /api/users/$USER_NAME/2fa/reset)" '"ok":true'
u POST /api/login -d "{\"username\":\"$USER_NAME\",\"password\":\"$USER_PASS\"}" >/dev/null

section "HTTPS certificate"
openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj "/CN=nas.strata.test" -keyout "$TMP/c.key" -out "$TMP/c.pem" 2>/dev/null
python3 -c "import json,sys; print(json.dumps({'cert':open(sys.argv[1]).read(),'key':open(sys.argv[2]).read()}))" "$TMP/c.pem" "$TMP/c.key" > "$TMP/cert.json"
check "certificate imported" "$(c PUT /api/system/certificates/custom -d @"$TMP/cert.json")" '"ok":true'
check "served on HTTPS" "$(echo | openssl s_client -connect localhost:7443 2>/dev/null | openssl x509 -noout -subject)" "nas.strata.test"
c DELETE /api/system/certificates/custom >/dev/null

section "UPS (simulation)"
check "NUT installed with the package" "$(c GET /api/ups | py 'print(d["installed"])')" "True"
check "simulation configured" "$(c PUT /api/ups -d '{"mode":"simulation","share":false,"shutdownAfter":600}')" '"ok":true'
for _ in $(seq 1 10); do c GET /api/ups | py 'print(d.get("status",{}).get("status"))' | grep -q OL && break; sleep 2; done
check "UPS on line" "$(c GET /api/ups | py 'print(d["status"]["status"])')" "OL"
c POST /api/ups/simulate -d '{"status":"OB","charge":80}' >/dev/null
for _ in $(seq 1 15); do c GET /api/system/events | grep -q "Coupure de courant" && break; sleep 2; done
check "power outage alert" "$(c GET /api/system/events | py '[print(e["message"]) for e in d[:5]]')" "Coupure de courant"
c POST /api/ups/simulate -d '{"status":"OL","charge":100}' >/dev/null
for _ in $(seq 1 15); do c GET /api/system/events | grep -q "Courant rétabli" && break; sleep 2; done
check "power restored alert" "$(c GET /api/system/events | py '[print(e["message"]) for e in d[:5]]')" "Courant rétabli"
c PUT /api/ups -d '{"mode":"off","share":false,"shutdownAfter":0}' >/dev/null

section "Network (trial + automatic rollback)"
IFACE=$(c GET /api/system/network | py 'print([i["name"] for i in d["status"]["interfaces"] if i["kind"]=="physical"][0])')
netapply() { c PUT /api/system/network/config -d "{\"config\":$1,\"timeout\":${2:-90}}" | py 'print(d.get("token") or d)'; }
netconfirm() { for _ in $(seq 1 30); do r=$(curl -s -m 4 -o /dev/null -w "%{http_code}" -X POST -H 'Content-Type: application/json' $B/api/system/network/confirm -d "{\"token\":\"$1\"}"); [ "$r" = 200 ] && { echo confirmed; return; }; sleep 2; done; echo "not confirmed ($r)"; }
check "early confirmation refused" "$(T=$(netapply "{\"interfaces\":[{\"name\":\"$IFACE\",\"mode\":\"static\",\"addresses\":[\"10.0.2.15/24\"],\"gateway\":\"10.0.2.2\",\"dns\":[\"10.0.2.3\"]}],\"bonds\":[],\"vlans\":[{\"parent\":\"$IFACE\",\"id\":20,\"mode\":\"static\",\"addresses\":[\"192.168.20.2/24\"]}]}"); curl -s -X POST -H 'Content-Type: application/json' $B/api/system/network/confirm -d "{\"token\":\"$T\"}"; echo; echo "$T" > "$TMP/tok")" "still being applied"
check "static address + VLAN confirmed" "$(netconfirm "$(cat "$TMP/tok")")" "confirmed"
check "VLAN interface up" "$(vm "ip -br addr show $IFACE.20")" "192.168.20.2/24"
T=$(netapply "{\"interfaces\":[{\"name\":\"$IFACE\",\"mode\":\"static\",\"addresses\":[\"10.9.9.9/24\"],\"gateway\":\"10.9.9.1\"}],\"bonds\":[],\"vlans\":[]}" 30)
sleep 8
check "wrong address cuts access" "$(curl -s -m 3 -o /dev/null -w '%{http_code}' $B/api/setup)" "000"
for _ in $(seq 1 30); do [ "$(curl -s -m 3 -o /dev/null -w '%{http_code}' $B/api/setup)" = 200 ] && break; sleep 3; done
check "rolled back automatically" "$(curl -s -m 3 -o /dev/null -w '%{http_code}' $B/api/setup)" "200"
check "previous config kept" "$(c GET /api/system/network | py 'print(d["config"]["vlans"][0]["id"], d["pending"])')" "20 None"
check "back to DHCP" "$(netconfirm "$(netapply "{\"interfaces\":[{\"name\":\"$IFACE\",\"mode\":\"dhcp\"}],\"bonds\":[],\"vlans\":[]}")")" "confirmed"

section "Scheduled tasks"
check "recommended plan" "$(c POST /api/schedules/defaults)" '"created":'
VID=$(c GET /api/storage/volumes | py 'print([v["id"] for v in d if v["name"]=="fast"][0])')
SID=$(c POST /api/schedules -d "{\"name\":\"t\",\"type\":\"snapshot\",\"cron\":\"0 * * * *\",\"enabled\":true,\"targetKind\":\"btrfs\",\"target\":\"$VID\",\"keepCount\":2}" | py 'print(d["id"])')
for _ in 1 2 3; do job "$(c POST /api/schedules/$SID/run)" >/dev/null; sleep 1.1; done
check "btrfs retention keeps 2" "$(c GET /api/storage/volumes/$VID/btrfs | py 'print(sum(1 for s in d["subvolumes"] if s["snapshot"]))')" "2"
for id in $(c GET /api/schedules | py 'print(" ".join(s["id"] for s in d if s["type"]!="snapshot"))'); do
  name=$(c GET /api/schedules | py "print([s['name'] for s in d if s['id']=='$id'][0])")
  check "task: $name" "$(job "$(c POST /api/schedules/$id/run)")" "success"
done

section "Reboot"
vm "sudo systemctl reboot"; sleep 8
for _ in $(seq 1 60); do vm true && break; sleep 3; done
wait_api
check "session survives reboot" "$(c GET /api/me)" "\"username\":\"$ADMIN_USER\""
check "RAID assembled" "$(raid_health)" "healthy"
check "volumes remounted" "$(c GET /api/storage/volumes | py 'print(sorted(v["name"] for v in d if v["mounted"]))')" "['fast', 'main']"
check "container restarted" "$(vm "sudo docker ps --format '{{.Names}}'")" "uptime"
check "services active" "$(vm "systemctl is-active strata smbd nfs-server docker | sort -u")" "active"

section "Boot with a RAID disk and a ZFS disk missing"
./vm/vm.sh stop >/dev/null
VM_DETACH="2 8" ./vm/vm.sh start >/dev/null
wait_api
for _ in $(seq 1 40); do [ "$(raid_health)" = degraded ] && break; sleep 3; done
check "RAID started degraded" "$(raid_health)" "degraded"
for _ in $(seq 1 40); do c GET /api/storage/volumes | py 'print([v["mounted"] for v in d if v["name"]=="main"])' | grep -q True && break; sleep 3; done
check "volume on degraded RAID mounted" "$(c GET /api/storage/volumes | py 'print([v["mounted"] for v in d if v["name"]=="main"])')" "True"
for _ in $(seq 1 20); do [ "$(vm "systemctl is-active nfs-server")" = active ] && break; sleep 3; done
check "NFS running" "$(vm "systemctl is-active nfs-server")" "active"
check "alert raised" "$(c GET /api/system/overview | py 'print(d["alerts"])')" "dégradée"
check "ZFS pool degraded" "$(c GET /api/storage/zfs | py 'print(d["pools"][0]["health"])')" "DEGRADED"
check "ZFS data still readable" "$(vm "sudo zfs list -H -o mounted tank/media")" "yes"
./vm/vm.sh stop >/dev/null; ./vm/vm.sh replace-disk 2 >/dev/null; ./vm/vm.sh replace-disk 8 >/dev/null; ./vm/vm.sh start >/dev/null
wait_api
for _ in $(seq 1 30); do [ "$(raid_health)" = degraded ] && break; sleep 3; done
NEW=$(c GET /api/storage/disks | py 'print([x["path"] for x in d if x["usage"]=="free" and x["serial"]=="STRATA-HDD-2"][0])')
check "replacement disk added" "$(c POST /api/storage/raid/data/add -d "{\"device\":\"$NEW\"}")" '"ok":true'
for _ in $(seq 1 100); do [ "$(raid_health)" = healthy ] && break; sleep 3; done
check "RAID rebuilt" "$(raid_health)" "healthy"
MISSING=$(c GET /api/storage/zfs | py 'print(d["pools"][0]["problems"][0]["device"])')
NEWZ=$(c GET /api/storage/disks | py 'print([x["path"] for x in d if x["usage"]=="free" and x["serial"]=="STRATA-VD-8"][0])')
check "ZFS replace" "$(c POST /api/storage/zfs/tank/replace -d "{\"device\":\"$MISSING\",\"newDevice\":\"$NEWZ\"}")" '"ok":true'
for _ in $(seq 1 60); do [ "$(c GET /api/storage/zfs | py 'print(d["pools"][0]["health"])')" = ONLINE ] && break; sleep 3; done
check "ZFS resilvered" "$(c GET /api/storage/zfs | py 'print(d["pools"][0]["health"])')" "ONLINE"

printf '\n%s passed, %s failed\n' "$PASSES" "$FAILS"
[ "$FAILS" -eq 0 ]
