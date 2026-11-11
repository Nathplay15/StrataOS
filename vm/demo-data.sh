#!/bin/bash
# Fills a FRESH test VM with believable demo data (accounts, volumes, shared
# folders, files, apps, tasks, backups, a backup repository, virtual networks,
# a firewall, notifications) for screenshots (vm/screenshots.cjs) and demos. Expects a
# VM with 4 data disks, ideally large ones (sizes are sparse):
#   VM_STATE=$PWD/vm/state-demo VM_HOSTNAME=strata-demo VM_WEB_PORT=7140 VM_SSH_PORT=2282 \
#     VM_HTTPS_PORT=7483 VM_SMB_PORT=4490 SEED_PORT=8749 VM_EXTRA_PORTS= VM_DISKS=4 VM_DISK_SIZE=4T \
#     ./vm/vm.sh create && ... ./vm/vm.sh deploy dist/strataos_<version>_amd64.deb
#   STRATA_URL=http://localhost:7140 VM_SSH_PORT=2282 VM_STATE=$PWD/vm/state-demo vm/demo-data.sh
# Credentials below are test-only.
set -euo pipefail
cd "$(dirname "$0")/.."
B=${STRATA_URL:-http://localhost:7070}
ADMIN_USER=admin ADMIN_PASS=Strata-Test-2026
J=$(mktemp)
trap 'rm -f "$J"' EXIT
c() { local m=$1 p=$2; shift 2; curl -s -b "$J" -c "$J" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "$B$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
vm() { ./vm/vm.sh ssh "$@" 2>/dev/null; }
job() { # waits for a job returned by the API
  local id; id=$(echo "$1" | py 'print(d.get("id",""))' 2>/dev/null)
  [ -n "$id" ] || { echo "  no job: $1"; return; }
  for _ in $(seq 1 600); do [ "$(c GET "/api/jobs/$id" | py 'print(d["status"])')" != running ] && break; sleep 2; done
  echo "  $(c GET "/api/jobs/$id" | py 'print(d["title"], "→", d["status"])')"
}
dev() { c GET /api/storage/disks | py "print([x['path'] for x in d if x['serial']=='$1'][0])"; }

echo ">> Setup"
c POST /api/setup -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"fullName\":\"Alex Martin\",\"hostname\":\"strata-home\"}" >/dev/null

echo ">> Storage"
job "$(c POST /api/storage/volumes -d '{"name":"data","fsType":"btrfs","devices":["'"$(dev STRATA-HDD-1)"'","'"$(dev STRATA-HDD-2)"'"],"dataProfile":"raid1","compression":true}')"
job "$(c POST /api/storage/volumes -d '{"name":"media","fsType":"btrfs","devices":["'"$(dev STRATA-HDD-3)"'","'"$(dev STRATA-HDD-4)"'"],"dataProfile":"raid1"}')"

echo ">> Accounts"
c POST /api/groups -d '{"name":"family"}' >/dev/null
for u in "alice:Alice Martin" "bob:Bob Martin" "emma:Emma Martin"; do
  c POST /api/users -d "{\"username\":\"${u%%:*}\",\"fullName\":\"${u#*:}\",\"password\":\"Demo-Pass-2026\",\"role\":\"user\",\"groups\":[\"family\"]}" >/dev/null
done

echo ">> Shared folders"
c POST /api/shares -d '{"name":"Documents","path":"/srv/strata/data/Documents","smb":true,"browseable":true,"recycleBin":true,"permissions":{"@family":"rw"}}' >/dev/null
c POST /api/shares -d '{"name":"Photos","path":"/srv/strata/data/Photos","smb":true,"browseable":true,"recycleBin":true,"permissions":{"@family":"rw"}}' >/dev/null
c POST /api/shares -d '{"name":"Media","path":"/srv/strata/media/Media","smb":true,"nfs":true,"browseable":true,"permissions":{"@family":"ro"},"nfsClients":"192.168.1.0/24"}' >/dev/null
c POST /api/shares -d '{"name":"TimeMachine","path":"/srv/strata/media/TimeMachine","smb":true,"timeMachine":true,"permissions":{"alice":"rw"},"quotaGb":1000}' >/dev/null

echo ">> Files"
vm 'sudo bash -s' <<'EOF'
set -e
D=/srv/strata/data/Documents P=/srv/strata/data/Photos M=/srv/strata/media/Media
mkdir -p "$D"/{Administration/Taxes,Administration/Insurance,School,Recipes,Work/Projects} "$P"/{2024/Summer,2024/Christmas,2025/Japan,2025/Birthday} "$M"/{Movies,Series/Planet\ Earth,Music/Jazz,Music/Classical}
for f in Administration/Taxes/tax-return-2024.pdf Administration/Insurance/home-insurance.pdf School/report-card-emma.pdf Recipes/lasagna.docx Work/Projects/budget-2025.xlsx Work/Projects/roadmap.pptx; do head -c $((RANDOM * 40)) /dev/urandom > "$D/$f"; done
# Photos hold random data (not sparse), so that backup sizes and deduplication look real.
for d in 2024/Summer 2024/Christmas 2025/Japan 2025/Birthday; do for i in $(seq -w 1 40); do head -c $(((1 + RANDOM % 3) * 1000000)) /dev/urandom > "$P/$d/IMG_$((RANDOM % 9000 + 1000))_$i.jpg"; done; done
for m in "The Grand Voyage (2021)" "Night Train (2019)" "Mountains of Light (2023)" "City Lights (1931)"; do fallocate -l $((4 + RANDOM % 8))G "$M/Movies/$m.mkv"; done
for e in $(seq -w 1 6); do fallocate -l 2G "$M/Series/Planet Earth/S01E$e.mkv"; done
for t in $(seq -w 1 12); do fallocate -l $((8 + RANDOM % 6))M "$M/Music/Jazz/Track $t.flac"; done
fallocate -l 420G /srv/strata/media/TimeMachine/alice-macbook.sparsebundle 2>/dev/null || true
chgrp -R family "$D" "$P" "$M" 2>/dev/null || true
EOF

echo ">> Apps (image downloads take a while)"
job "$(c POST /api/apps/jellyfin/install -d '{"name":"jellyfin","values":{"CONFIG_DIR":"/srv/strata/data/apps/jellyfin","MEDIA_DIR":"/srv/strata/media/Media"}}')"
job "$(c POST /api/apps/uptimekuma/install -d '{"name":"uptime-kuma","values":{"DATA_DIR":"/srv/strata/data/apps/uptime-kuma"}}')"
job "$(c POST /api/apps/homepage/install -d '{"name":"homepage","values":{"CONFIG_DIR":"/srv/strata/data/apps/homepage"}}')"

echo ">> Tasks and backup"
c POST /api/schedules/defaults >/dev/null
BK=$(c POST /api/backups -d '{"name":"Documents → media","kind":"restic","sources":["/srv/strata/data/Documents","/srv/strata/data/Photos"],"destType":"local","destPath":"/srv/strata/media/Backups/restic","password":"Demo-Restic-2026","cron":"0 2 * * *","enabled":true}' | py 'print(d["id"])')
job "$(c POST "/api/backups/$BK/run")"

echo ">> Backup repository, snapshots with retention"
DS=$(c POST /api/datastores -d '{"name":"Backup server","destType":"local","destPath":"/srv/strata/media/Backups/repository","password":"Demo-Repo-2026","verify":{"subsets":4}}' | py 'print(d["id"])')
DT=$(c POST /api/backups -d '{"name":"Family files → repository","kind":"restic","sources":["/srv/strata/data/Documents","/srv/strata/data/Photos"],"destType":"datastore","datastore":"'"$DS"'","keepLast":7,"keepDaily":14,"keepMonthly":12,"cron":"30 1 * * *","enabled":true}' | py 'print(d["id"])')
job "$(c POST "/api/backups/$DT/run")"
job "$(c POST "/api/backups/$DT/run")"
job "$(c POST "/api/datastores/$DS/verify")"
c POST "/api/datastores/$DS/stats" >/dev/null
VID=$(c GET /api/storage/volumes | py 'print([v["id"] for v in d if v["name"]=="data"][0])')
SID=$(c POST /api/schedules -d '{"name":"Hourly snapshots of data","type":"snapshot","cron":"0 * * * *","enabled":true,"targetKind":"btrfs","target":"'"$VID"'","keepHourly":24,"keepDaily":7,"keepWeekly":4,"keepMonthly":6}' | py 'print(d["id"])')
c POST "/api/schedules/$SID/run" >/dev/null

echo ">> Virtual networks and firewall (each applied on trial, then confirmed)"
confirm() {
  local tok; tok=$(echo "$1" | py 'print(d.get("token",""))' 2>/dev/null)
  [ -n "$tok" ] || { echo "  not applied: ${1:0:200}"; return; }
  for _ in $(seq 1 20); do
    sleep 3
    [ "$(c POST "$2" -d "{\"token\":\"$tok\"}" -o /dev/null -w '%{http_code}')" = 200 ] && return
  done
  echo "  not confirmed"
}
confirm "$(c POST /api/system/network/vnets -d '{"id":"iot","label":"Home automation","kind":"nat","address":"10.20.0.1/24","dhcp":true,"isolate":true,"nasPorts":"1883","logDrops":true}')" /api/system/network/confirm
confirm "$(c POST /api/system/network/vnets -d '{"id":"lab","label":"Test lab","kind":"isolated"}')" /api/system/network/confirm
FW='{"host":{"enabled":true,"policyIn":"drop","logDrops":true,"rules":[{"dir":"in","action":"drop","source":"set:blocklist","log":true,"comment":"Known bad addresses"},{"dir":"in","action":"accept","source":"lan","comment":"Local network"},{"dir":"in","action":"accept","proto":"tcp","ports":"7070,7443","comment":"Web interface"},{"dir":"in","action":"accept","proto":"udp","ports":"51820","comment":"WireGuard"}]},
"groups":[{"name":"web-server","comment":"HTTP and HTTPS from anywhere","rules":[{"dir":"in","action":"accept","proto":"tcp","ports":"80,443"}]},{"name":"admin-office","comment":"SSH from the office","rules":[{"dir":"in","action":"accept","proto":"tcp","ports":"22","source":"set:office"}]}],
"ipsets":[{"name":"office","comment":"Office network","entries":["192.168.10.0/24"]},{"name":"blocklist","entries":["203.0.113.0/24","198.51.100.7"]}]}'
confirm "$(c PUT /api/system/network/firewall -d "{\"config\":$FW,\"timeout\":60}")" /api/system/network/firewall/confirm

echo ">> Notifications, security advisor, an office NAS that does not answer (fleet health)"
BO=$(c POST /api/backups -d '{"name":"Photos → office NAS","kind":"rsync","sources":["/srv/strata/data/Photos"],"destType":"ssh","host":"192.168.1.30","user":"backup","destPath":"/volume1/backup/home","cron":"0 3 * * *","enabled":true}' | py 'print(d["id"])')
job "$(c POST "/api/backups/$BO/run")"
job "$(c POST /api/protection/restore-test -d '{}')"
c POST /api/security/advisor/run >/dev/null
# A console member added by hand (pairing needs a second NAS): shown as unreachable.
vm 'sudo bash -s' <<'EOF'
systemctl stop strata
python3 - <<'PY'
import json, datetime
p = "/var/lib/strata/state.json"
d = json.load(open(p))
d.setdefault("fleet", {}).setdefault("members", []).append({"id": "office1", "name": "strata-office", "host": "192.168.1.30", "cert": "0" * 64, "added": datetime.datetime.now(datetime.timezone.utc).isoformat()})
json.dump(d, open(p, "w"))
PY
systemctl start strata
EOF
echo ">> Demo ready: $B (user $ADMIN_USER)"
