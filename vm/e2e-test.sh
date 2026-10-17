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
check "first admin can use sudo" "$(vm "id $ADMIN_USER")" "(sudo)"
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
APPUPD=$(c POST /api/schedules -d '{"name":"Apps","type":"appupdate","target":"all","cron":"0 4 * * 0","enabled":true}' | py 'print(d["id"])')
check "app update task" "$(job "$(c POST /api/schedules/$APPUPD/run)")" "success"
check "app update summary" "$(c GET /api/schedules | py '[print(s["history"][0]["message"]) for s in d if s["id"]=="'$APPUPD'"]')" "1 application(s) vérifiée(s)"
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

section "Virtual machines (nested KVM)"
check "virtualisation installed" "$(job "$(c POST /api/system/components/vms/install)")" "success"
check "KVM available" "$(c GET /api/vms | py 'print(d["installed"], d["kvm"])')" "True True"
check "VM name validated" "$(c POST /api/vms -d '{"name":"../x","storage":"/srv/strata/main/Photos"}')" "invalid"
check "VM storage confined" "$(job "$(c POST /api/vms -d '{"name":"evil","os":"other","vcpus":1,"memoryMb":256,"diskGb":1,"storage":"/etc","network":"nat","firmware":"bios"}')")" "inside a volume"
check "VM created" "$(job "$(c POST /api/vms -d '{"name":"e2e-vm","os":"other","vcpus":1,"memoryMb":512,"diskGb":2,"storage":"/srv/strata/main/Photos","network":"nat","firmware":"uefi"}')")" "success"
check "VM running" "$(c GET /api/vms | py 'print([v["state"] for v in d["vms"] if v["name"]=="e2e-vm"])')" "running"
check "VM disk in the share" "$(vm "sudo ls /srv/strata/main/Photos/e2e-vm")" "disk0.qcow2"
check "VNC console over websocket" "$(python3 - "$J" <<'PY'
import socket, sys, base64, os
jar = [l.split("\t") for l in open(sys.argv[1]) if "\t" in l and not l.startswith("# ")]
cookie = "; ".join(f"{f[5]}={f[6].strip()}" for f in jar)
s = socket.create_connection(("localhost", 7070), timeout=10)
key = base64.b64encode(os.urandom(16)).decode()
s.sendall(f"GET /api/vms/e2e-vm/vnc HTTP/1.1\r\nHost: localhost:7070\r\nOrigin: http://localhost:7070\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: binary\r\nCookie: {cookie}\r\n\r\n".encode())
data = b""
while b"RFB" not in data and len(data) < 4096:
    chunk = s.recv(4096)
    if not chunk:
        break
    data += chunk
print("RFB" if b"RFB 003" in data else data[:200])
PY
)" "RFB"
check "VM forced off" "$(c POST /api/vms/e2e-vm/destroy)" '"ok":true'
check "VM resources changed offline" "$(c POST /api/vms/e2e-vm/resources -d '{"vcpus":2,"memoryMb":768}')" '"ok":true'
check "new resources" "$(c GET /api/vms | py 'print([(v["vcpus"], v["memoryMb"]) for v in d["vms"] if v["name"]=="e2e-vm"])')" "(2, 768)"
check "VM deleted" "$(c DELETE '/api/vms/e2e-vm?disks=1')" '"ok":true'
check "VM files removed" "$(vm "sudo ls /srv/strata/main/Photos/e2e-vm 2>&1")" "No such file"

section "Cloud sync (rclone, local WebDAV server)"
DAVPASS=$(head -c 12 /dev/urandom | base64 | tr -d '/+=')
vm "sudo mkdir -p /tmp/davroot/bi && echo cloud | sudo tee /tmp/davroot/bi/from-cloud.txt >/dev/null && sudo systemd-run --unit=e2e-dav rclone serve webdav /tmp/davroot --addr 127.0.0.1:8088 --user dav --pass '$DAVPASS' --dir-cache-time 1s >/dev/null 2>&1; sudo mkdir -p /srv/strata/main/Photos/bi && echo nas | sudo tee /srv/strata/main/Photos/bi/from-nas.txt >/dev/null"
check "rclone installed with the package" "$(c GET /api/cloudsync | py 'print(d["installed"])')" "True"
DAV='"provider":"webdav","params":{"url":"http://127.0.0.1:8088","vendor":"owncloud","user":"dav"}'
check "connection test" "$(c POST /api/cloudsync/test -d "{\"name\":\"t\",$DAV,\"secrets\":{\"pass\":\"$DAVPASS\"},\"localPath\":\"/srv/strata/main/Photos\",\"remotePath\":\"up\",\"direction\":\"upload\"}")" "Connexion réussie"
check "wrong password rejected" "$(c POST /api/cloudsync/test -d "{\"name\":\"t\",$DAV,\"secrets\":{\"pass\":\"wrong\"},\"localPath\":\"/srv/strata/main/Photos\",\"remotePath\":\"up\",\"direction\":\"upload\"}")" "error"
check "local folder confined" "$(c POST /api/cloudsync -d "{\"name\":\"t\",$DAV,\"secrets\":{\"pass\":\"x\"},\"localPath\":\"/etc\",\"remotePath\":\"up\",\"direction\":\"upload\"}")" "inside a volume"
UP=$(c POST /api/cloudsync -d "{\"name\":\"up\",$DAV,\"secrets\":{\"pass\":\"$DAVPASS\"},\"localPath\":\"/srv/strata/main/Photos/album\",\"remotePath\":\"up\",\"direction\":\"upload\",\"mirror\":true,\"cron\":\"0 */6 * * *\",\"enabled\":true}" | py 'print(d["id"])')
check "secrets never sent back" "$(c GET /api/cloudsync | py 'print([t["secrets"] for t in d["tasks"]])')" "'pass': '********'"
check "upload sync" "$(job "$(c POST /api/cloudsync/$UP/run)")" "success"
check "files in the cloud" "$(vm "sudo find /tmp/davroot/up -type f")" "photo.txt"
BI=$(c POST /api/cloudsync -d "{\"name\":\"bi\",$DAV,\"secrets\":{\"pass\":\"$DAVPASS\"},\"localPath\":\"/srv/strata/main/Photos/bi\",\"remotePath\":\"bi\",\"direction\":\"bisync\",\"cron\":\"\",\"enabled\":true}" | py 'print(d["id"])')
check "two-way sync: first merge" "$(job "$(c POST /api/cloudsync/$BI/run)")" "success"
check "both sides merged" "$(vm "sudo ls /srv/strata/main/Photos/bi /tmp/davroot/bi | sort -u | tr '\n' ' '")" "from-cloud.txt from-nas.txt"
vm "sudo rm /srv/strata/main/Photos/bi/from-nas.txt; echo new | sudo tee /tmp/davroot/bi/new-cloud.txt >/dev/null"
sleep 2
check "two-way sync: changes" "$(job "$(c POST /api/cloudsync/$BI/run)")" "success"
check "deletion propagated to the cloud" "$(vm "sudo ls /tmp/davroot/bi | tr '\n' ' '")" "from-cloud.txt new-cloud.txt "
check "new cloud file on the NAS, share ACL inherited" "$(vm "sudo getfacl -p /srv/strata/main/Photos/bi/new-cloud.txt | grep famille")" "group:famille:rw"
c DELETE /api/cloudsync/$UP >/dev/null; c DELETE /api/cloudsync/$BI >/dev/null
check "tasks deleted, files kept" "$(c GET /api/cloudsync | py 'print(len(d["tasks"]))') $(vm "sudo ls /srv/strata/main/Photos/bi | wc -l")" "0 2"
vm "sudo systemctl stop e2e-dav"

section "File services (WebDAV, FTPS, SFTP)"
UA="$USER_NAME:$USER_PASS"
check "WebDAV off by default" "$(curl -s -o /dev/null -w '%{http_code}' -u "$UA" $B/dav/)" "404"
check "FTP server installed" "$(job "$(c POST /api/system/components/ftp/install)")" "success"
check "services enabled" "$(c PUT /api/fileservices -d '{"webdav":true,"ftp":true,"ftpRequireTls":true,"sftp":true}')" '"ok":true'
check "WebDAV lists the user's shares" "$(curl -s -X PROPFIND -H 'Depth: 1' -u "$UA" $B/dav/)" "/dav/Photos/"
check "WebDAV hides other shares" "$(curl -s -o /dev/null -w '%{http_code}' -X PROPFIND -u "$UA" $B/dav/Media/)" "404"
check "WebDAV bad password" "$(curl -s -o /dev/null -w '%{http_code}' -u "$USER_NAME:wrong" $B/dav/)" "401"
echo dav > "$TMP/dav.txt"
check "WebDAV upload" "$(curl -s -o /dev/null -w '%{http_code}' -T "$TMP/dav.txt" -u "$UA" $B/dav/Photos/up-dav.txt)" "201"
check "WebDAV file owned by the user" "$(vm "sudo stat -c '%U %a' /srv/strata/main/Photos/up-dav.txt")" "$USER_NAME 660"
check "WebDAV files never rendered as pages" "$(curl -s -D - -o /dev/null -u "$UA" $B/dav/Photos/up-dav.txt)" "sandbox"
check "WebDAV traversal blocked" "$(curl -s --path-as-is -o /dev/null -w '%{http_code}' -u "$UA" "$B/dav/Photos/%2e%2e/%2e%2e/%2e%2e/etc/passwd")" "404"
check "WebDAV recycle bin out of reach" "$(curl -s -o /dev/null -w '%{http_code}' -X PROPFIND -u "$UA" $B/dav/Photos/.recycle/)" "404"
curl -s -o /dev/null -X DELETE -u "$UA" $B/dav/Photos/up-dav.txt
check "WebDAV delete goes to the recycle bin" "$(vm "sudo ls /srv/strata/main/Photos/.recycle/$USER_NAME/")" "up-dav.txt"
check "plain FTP refused (TLS required)" "$(vm "curl -s -o /dev/null -w '%{http_code}' ftp://localhost/ -u '$UA'")" "530"
check "FTPS upload" "$(vm "echo f > /tmp/f.txt; curl -s --ssl-reqd -k -T /tmp/f.txt ftp://localhost/main/Photos/up-ftp.txt -u '$UA' && sudo stat -c '%U %a' /srv/strata/main/Photos/up-ftp.txt")" "$USER_NAME 660"
check "FTP: no write outside the user's shares" "$(vm "curl -s -o /dev/null -w '%{http_code}' --ssl-reqd -k -T /tmp/f.txt ftp://localhost/tank/media/x.txt -u '$UA'")" "553"
check "FTP confined to the storage" "$(vm "curl -s -o /dev/null -w '%{http_code}' --ssl-reqd -k --path-as-is ftp://localhost/../../etc/passwd -u '$UA'")" "550"
check "SFTP-only group" "$(vm "id $USER_NAME")" "strata-sftp"
check "SFTP upload" "$(vm "echo s > /tmp/s.txt; curl -s -k -T /tmp/s.txt sftp://localhost/main/Photos/up-sftp.txt -u '$UA' && sudo stat -c '%U' /srv/strata/main/Photos/up-sftp.txt")" "$USER_NAME"
check "SFTP confined to the storage" "$(vm "curl -s -k -o /dev/null -w '%{exitcode}' sftp://localhost/etc/passwd -u '$UA'")" "78"
check "SFTP: no write outside the user's shares" "$(vm "curl -s -k -o /dev/null -w '%{exitcode}' -T /tmp/s.txt sftp://localhost/tank/media/x.txt -u '$UA'")" "9"
check "services disabled" "$(c PUT /api/fileservices -d '{"webdav":false,"ftp":false,"ftpRequireTls":true,"sftp":false}')" '"ok":true'
check "FTP stopped" "$(vm "systemctl is-active vsftpd")" "inactive"
check "SFTP access removed" "$(vm "curl -s -k -o /dev/null -w '%{exitcode}' sftp://localhost/main/Photos/ -u '$UA'")" "67"
check "WebDAV off again" "$(curl -s -o /dev/null -w '%{http_code}' -u "$UA" $B/dav/)" "404"

section "Antivirus (ClamAV, EICAR test file)"
check "ClamAV installed" "$(job "$(c POST /api/system/components/antivirus/install)")" "success"
vm "printf '%s' 'X5O!P%@AP[4\PZX54(P^)7CC)7}\$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!\$H+H*' | sudo tee /srv/strata/main/Photos/eicar.com >/dev/null"
AV=$(c POST /api/schedules -d '{"name":"AV","type":"antivirus","target":"/srv/strata/main/Photos","avAction":"quarantine","cron":"0 2 * * *","enabled":true}' | py 'print(d["id"])')
job "$(c POST /api/schedules/$AV/run)" >/dev/null
check "virus found" "$(c GET /api/schedules | py '[print(s["history"][0]["message"]) for s in d if s["id"]=="'$AV'"]')" "Eicar-Test-Signature"
check "infected file moved to quarantine" "$(vm "sudo ls /srv/strata/main/Photos/eicar.com 2>&1")" "No such file"
check "alert logged" "$(c GET /api/system/events | py '[print(e["message"]) for e in d[:5]]')" "infecté"
QID=$(c GET /api/antivirus | py 'print(d["quarantine"][0]["id"])')
check "restore from quarantine" "$(c POST /api/antivirus/quarantine/$QID/restore)" "/srv/strata/main/Photos/eicar.com"
job "$(c POST /api/schedules/$AV/run)" >/dev/null
QID=$(c GET /api/antivirus | py 'print(d["quarantine"][0]["id"])')
check "delete from quarantine" "$(c DELETE /api/antivirus/quarantine/$QID) $(c GET /api/antivirus | py 'print(len(d["quarantine"]))')" '"ok":true} 0'
c DELETE /api/schedules/$AV >/dev/null

section "Active Directory and LDAP (test servers in containers)"
DCPASS="Dc-$(head -c 9 /dev/urandom | base64 | tr -d '/+=')9"
check "directory clients installed" "$(job "$(c POST /api/system/components/directory/install)")" "success"
DCIP=$(./vm/vm.sh ssh "cat > /tmp/test-dc.sh && sudo bash /tmp/test-dc.sh '$DCPASS'" < vm/test-dc.sh 2>/dev/null | tail -1)
check "test domain controller" "$DCIP" "172.17."
vm "sudo resolvectl dns docker0 $DCIP && sudo resolvectl domain docker0 '~strata.test'"
check "wrong domain password refused" "$(job "$(c POST /api/directory/join -d '{"type":"ad","realm":"strata.test","admin":"Administrator","password":"wrong"}')")" "wrong administrator account or password"
check "domain joined" "$(job "$(c POST /api/directory/join -d "{\"type\":\"ad\",\"realm\":\"strata.test\",\"admin\":\"Administrator\",\"password\":\"$DCPASS\",\"adminGroup\":\"STRATA\\\\domain admins\"}")")" "success"
check "domain trust valid" "$(c GET /api/directory | py 'print(d["joined"], d["status"]["connected"])')" "True True"
check "join password not stored" "$(vm "sudo grep -c '$DCPASS' /var/lib/strata/state.json")" "0"
check "domain users and groups listed" "$(c GET '/api/directory/search?q=' | py 'print(d["users"], d["groups"])')" 'STRATA\\marie'
SH=$(c GET /api/shares | py 's=[x for x in d if x["name"]=="Photos"][0]; s["permissions"]["@STRATA\\compta"]="rw"; print(json.dumps(s))')
check "domain group given rights on a share" "$(c PUT /api/shares/Photos -d "$SH")" 'STRATA'
check "domain user writes over SMB" "$(printf '%s' "$DCPASS" | ./vm/vm.sh ssh 'P=$(cat); umask 077; printf "username = marie\npassword = %s\ndomain = STRATA\n" "$P" > /tmp/m.auth; smbclient //localhost/Photos -A /tmp/m.auth -c "put /etc/hostname from-marie.txt" >/dev/null 2>&1; smbclient //localhost/Media -A /tmp/m.auth -c ls 2>&1 | tail -1; rm -f /tmp/m.auth; sudo stat -c "%U" /srv/strata/main/Photos/from-marie.txt' 2>/dev/null | tr '\n' ' ')" "NT_STATUS_ACCESS_DENIED STRATA\\marie"
J3=$(mktemp)
d() { local m=$1 p=$2; shift 2; curl -s -b "$J3" -c "$J3" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
check "domain user signs in to the web UI" "$(d POST /api/login -d "{\"username\":\"marie\",\"password\":\"$DCPASS\"}")" '"role":"user"'
check "domain user: wrong password refused" "$(curl -s -X POST -H 'X-Strata: 1' $B/api/login -d '{"username":"marie","password":"wrong"}')" "invalid"
check "domain user sees only their shares" "$(d GET '/api/files?path=/srv/strata/tank/media')" "access denied"
check "domain password not changeable here" "$(d POST /api/me/password -d '{"current":"x","new":"Abcdefgh1"}')" "managed by the directory"
check "domain admins group gives admin role" "$(curl -s -X POST -H 'X-Strata: 1' $B/api/login -d "{\"username\":\"STRATA\\\\Administrator\",\"password\":\"$DCPASS\"}")" '"role":"admin"'
c PUT /api/fileservices -d '{"webdav":true,"ftp":false,"ftpRequireTls":true,"sftp":false}' >/dev/null
check "domain user over WebDAV" "$(curl -s -o /dev/null -w '%{http_code}' -T "$TMP/dav.txt" -u "marie:$DCPASS" $B/dav/Photos/dav-marie.txt) $(vm "sudo stat -c '%U' /srv/strata/main/Photos/dav-marie.txt")" "201 STRATA\\marie"
c PUT /api/fileservices -d '{"webdav":false,"ftp":false,"ftpRequireTls":true,"sftp":false}' >/dev/null
check "domain left" "$(c POST /api/directory/leave -d "{\"admin\":\"Administrator\",\"password\":\"$DCPASS\"}")" '"ok":true'
check "domain sessions ended on leave" "$(d GET /api/me)" "authentication required"
check "computer account removed, Samba standalone again" "$(vm "sudo docker exec e2e-dc samba-tool computer list | grep -ci strata-vm; grep -c 'security = ads' /etc/samba/smb.conf" | tr '\n' ' ')" "0 0"
SH=$(c GET /api/shares | py 's=[x for x in d if x["name"]=="Photos"][0]; s["permissions"].pop("@STRATA\\compta", None); print(json.dumps(s))')
c PUT /api/shares/Photos -d "$SH" >/dev/null
LDAPIP=$(./vm/vm.sh ssh "cat > /tmp/test-ldap.sh && sudo bash /tmp/test-ldap.sh '$DCPASS'" < vm/test-ldap.sh 2>/dev/null | tail -1)
check "LDAP directory joined" "$(job "$(c POST /api/directory/join -d "{\"type\":\"ldap\",\"uri\":\"ldap://$LDAPIP\",\"baseDn\":\"dc=strata,dc=test\",\"bindDn\":\"cn=admin,dc=strata,dc=test\",\"bindPassword\":\"$DCPASS\"}")")" "success"
check "LDAP bind password masked" "$(c GET /api/directory | py 'print(d["config"]["bindPassword"])')" "********"
for _ in $(seq 1 20); do c GET '/api/directory/search?q=pa' | grep -q paul && break; sleep 2; done
check "LDAP users known to the system" "$(vm "id paul")" "projets"
check "LDAP users listed" "$(c GET '/api/directory/search?q=pa')" "paul"
check "LDAP left" "$(c POST /api/directory/leave -d '{}')" '"ok":true'
check "LDAP users gone" "$(vm "id paul 2>&1")" "no such user"
vm "sudo docker rm -f e2e-dc e2e-ldap >/dev/null; sudo resolvectl revert docker0"

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
