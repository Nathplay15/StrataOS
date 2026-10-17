#!/bin/bash
# High availability test with two VMs on a shared LAN (QEMU multicast, no root):
#   A = vm/state (primary, 10.99.0.1), B = vm/state-b (secondary, 10.99.0.2),
#   shared address 10.99.0.100. Pairing, replication, failover (A killed),
#   changes on B, A restarted (stays passive), hand back to A.
# Usage: make deb && ./vm/ha-test.sh   (recreates both VMs)
set -uo pipefail
cd "$(dirname "$0")/.."
DEB=dist/strataos_$(cat VERSION)_amd64.deb
ADMIN_USER=nathan ADMIN_PASS=Strata-Test-2026
LAN=230.0.0.1:12345
T=$(mktemp -d)
PASSES=0 FAILS=0
ok() { PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1"; }
ko() { FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    %s\n' "$1" "$2"; }
check() { case "$2" in *"$3"*) ok "$1" ;; *) ko "$1" "expected '$3', got: ${2:0:300}" ;; esac; }
A() { VM_LAN=$LAN VM_DISKS=2 ./vm/vm.sh "$@"; }
B() { VM_STATE=$PWD/vm/state-b VM_HOSTNAME=strata-b VM_WEB_PORT=7080 VM_SSH_PORT=2232 VM_HTTPS_PORT=7453 VM_SMB_PORT=4460 SEED_PORT=8729 VM_EXTRA_PORTS="" VM_DISKS=2 VM_MEM=3072 VM_LAN=$LAN ./vm/vm.sh "$@"; }
api() { local port=$1 m=$2 p=$3; shift 3; curl -s -m 60 -b "$T/ck$port" -c "$T/ck$port" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "http://localhost:$port$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
job() { local port=$1 id; id=$(echo "$2" | py 'print(d.get("id",""))' 2>/dev/null); [ -n "$id" ] || { echo "no-job: $2"; return; }
  for _ in $(seq 1 200); do [ "$(api "$port" GET "/api/jobs/$id" | py 'print(d["status"])')" != running ] && break; sleep 2; done
  api "$port" GET "/api/jobs/$id" | py 'print(d["status"], d.get("error",""))'; }
state() { api "$1" GET /api/ha | py 'print(d["state"])'; }
section() { printf '\n\033[1;36m== %s\033[0m\n' "$1"; }

section "Two VMs on a shared LAN"
A destroy >/dev/null 2>&1; B destroy >/dev/null 2>&1
A create >/dev/null && A deploy "$DEB" >/dev/null || exit 1
B create >/dev/null && B deploy "$DEB" >/dev/null || exit 1
A ssh 'sudo ip link set enp0s5 up && sudo ip addr add 10.99.0.1/24 dev enp0s5'
B ssh 'sudo ip link set enp0s5 up && sudo ip addr add 10.99.0.2/24 dev enp0s5'
check "LAN between the VMs" "$(B ssh 'ping -c1 -W2 10.99.0.1 >/dev/null && echo up')" "up"
for p in 7070 7080; do
  api $p POST /api/setup -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"fullName\":\"Test\",\"hostname\":\"nas-$p\"}" >/dev/null
  D=$(api $p GET /api/storage/disks | py 'print([x["path"] for x in d if x["usage"]=="free"][0])')
  check "volume on $p" "$(job $p "$(api $p POST /api/storage/volumes -d "{\"name\":\"volume1\",\"fsType\":\"btrfs\",\"devices\":[\"$D\"],\"dataProfile\":\"single\"}")")" "success"
  check "HA components on $p" "$(job $p "$(api $p POST /api/system/components/ha/install)")" "success"
done
api 7070 POST /api/shares -d '{"name":"Documents","path":"/srv/strata/volume1/Documents","smb":true,"browseable":true,"recycleBin":true,"permissions":{"nathan":"rw"}}' >/dev/null
api 7070 POST /api/users -d '{"username":"alice","password":"Alice-Pass-1","role":"user"}' >/dev/null
A ssh 'echo "version 1" | sudo tee /srv/strata/volume1/Documents/rapport.txt >/dev/null && sync'

section "Pairing and replication"
check "wrong pairing code refused" "$(api 7080 POST /api/ha/prepare >/dev/null; api 7070 POST /api/ha/pair -d '{"peerHost":"10.99.0.2","vip":"10.99.0.100/24","interface":"enp0s5","interval":5,"code":"AAAAA-BBBBB"}')" "wrong or expired"
CODE=$(api 7080 POST /api/ha/prepare | py 'print(d["code"])')
check "paired" "$(api 7070 POST /api/ha/pair -d "{\"peerHost\":\"10.99.0.2\",\"vip\":\"10.99.0.100/24\",\"interface\":\"enp0s5\",\"interval\":5,\"code\":\"$CODE\"}")" '"role":"primary"'
for _ in $(seq 1 60); do [ "$(state 7070)" = MASTER ] && [ "$(state 7080)" = BACKUP ] && break; sleep 2; done
check "primary holds the shared address" "$(state 7070) $(state 7080)" "MASTER BACKUP"
# Pairing starts a first replication by itself: wait for it, then run one by hand.
for _ in $(seq 1 90); do [ -n "$(api 7070 GET /api/ha | py 'print(d["config"]["lastResult"])')" ] && break; sleep 2; done
check "first replication after pairing" "$(api 7070 GET /api/ha | py 'print(d["config"]["lastResult"])')" "OK"
A ssh 'echo "version 1" | sudo tee /srv/strata/volume1/Documents/rapport.txt >/dev/null && sync'
check "replication" "$(job 7070 "$(api 7070 POST /api/ha/sync)")" "success"
check "data on the secondary" "$(B ssh 'sudo cat /srv/strata/volume1/Documents/rapport.txt')" "version 1"
check "accounts on the secondary (same UID)" "$(A ssh 'id -u alice') $(B ssh 'id -u alice')" "$(A ssh 'id -u alice') $(A ssh 'id -u alice')"
check "secondary does not publish the shares" "$(B ssh "grep -c '^\[' /etc/samba/strata-shares.conf")" "0"

section "Failover"
A ssh 'sync' >/dev/null; kill -9 "$(cat vm/state/qemu.pid)"
for _ in $(seq 1 30); do [ "$(state 7080)" = MASTER ] && break; sleep 1; done
check "secondary takes over" "$(state 7080)" "MASTER"
check "shared address on the secondary" "$(B ssh 'ip -4 addr show enp0s5')" "10.99.0.100"
B ssh 'command -v smbclient >/dev/null || sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq smbclient >/dev/null 2>&1'
check "shares served at the shared address" "$(B ssh "smbclient //10.99.0.100/Documents -U '$ADMIN_USER%$ADMIN_PASS' -c ls 2>&1")" "rapport.txt"
check "replicated user signs in on the secondary" "$(curl -s -X POST -H 'X-Strata: 1' http://localhost:7080/api/login -d '{"username":"alice","password":"Alice-Pass-1"}')" '"username":"alice"'
check "failover alert" "$(api 7080 GET /api/system/events | py '[print(e["message"]) for e in d[:5]]')" "Bascule"
B ssh "echo 'version 2' > /tmp/v2; smbclient //10.99.0.100/Documents -U '$ADMIN_USER%$ADMIN_PASS' -c 'put /tmp/v2 rapport.txt' >/dev/null 2>&1"

section "Primary back, then hand back"
A start >/dev/null
A ssh 'sudo ip link set enp0s5 up; sudo ip addr add 10.99.0.1/24 dev enp0s5; sudo systemctl restart keepalived'
for _ in $(seq 1 60); do curl -sf http://localhost:7070/api/setup >/dev/null && break; sleep 2; done
api 7070 POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null
sleep 8
check "returning primary stays passive" "$(state 7070) $(state 7080) $(A ssh "grep -c '^\[' /etc/samba/strata-shares.conf")" "BACKUP MASTER 0"
check "primary refuses to overwrite the secondary" "$(job 7070 "$(api 7070 POST /api/ha/sync)")" "hand control back"
check "hand back" "$(job 7080 "$(api 7080 POST /api/ha/switchback)")" "success"
for _ in $(seq 1 30); do [ "$(state 7070)" = MASTER ] && break; sleep 1; done
check "primary active again" "$(state 7070) $(state 7080)" "MASTER BACKUP"
check "changes made during the outage kept" "$(A ssh 'sudo cat /srv/strata/volume1/Documents/rapport.txt')" "version 2"
section "Planned maintenance"
check "maintenance hands over" "$(job 7070 "$(api 7070 POST /api/ha/maintenance)")" "success"
for _ in $(seq 1 30); do [ "$(state 7080)" = MASTER ] && break; sleep 1; done
check "secondary serves during maintenance" "$(state 7080) $(state 7070)" "MASTER "
B ssh "echo 'version 3' > /tmp/v3"
for _ in $(seq 1 20); do B ssh "smbclient //10.99.0.100/Documents -U '$ADMIN_USER%$ADMIN_PASS' -c 'put /tmp/v3 rapport.txt' >/dev/null 2>&1" && break; sleep 2; done
check "write during maintenance" "$(B ssh 'sudo cat /srv/strata/volume1/Documents/rapport.txt')" "version 3"
check "end of maintenance" "$(api 7070 POST /api/ha/maintenance/end)" '"ok":true'
# The secondary restarts keepalived after copying, then waits 30 s (startup delay).
for _ in $(seq 1 60); do [ "$(state 7070) $(state 7080)" = "MASTER BACKUP" ] && break; sleep 2; done
check "address back to the primary automatically" "$(state 7070) $(state 7080)" "MASTER BACKUP"
check "changes made during maintenance kept" "$(A ssh 'sudo cat /srv/strata/volume1/Documents/rapport.txt')" "version 3"
check "unpaired" "$(api 7070 POST /api/ha/unpair)" '"ok":true'
check "both independent again" "$(api 7080 GET /api/ha | py 'print(d["config"]["role"] or "none")') $(A ssh 'systemctl is-active keepalived')" "none inactive"

B destroy >/dev/null 2>&1
printf '\n%s passed, %s failed\n' "$PASSES" "$FAILS"
[ "$FAILS" -eq 0 ]
