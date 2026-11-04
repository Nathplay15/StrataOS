#!/bin/bash
# Multi-NAS console 0.10 test with two VMs on a shared LAN (QEMU multicast, no root):
#   A = vm/state-fa (console, 10.96.0.1), B = vm/state-fb (watched, 10.96.0.2), 2 GB of RAM each.
# Fleet health dashboard (peer health, cache, summary, a NAS that needs attention,
# permissions), then VM and container transfers over the fleet link: a VM copied
# A → B with a missing bridge mapped and started there, copied back B → A live from a
# snapshot (new identity), a container moved A → B, a cancelled transfer, a transfer
# refused without the permission. About 20 minutes (downloads packages and an Alpine image).
# Usage: make deb && flock -o /home/nathan/Documents/StrataOS-wt/.vm.lock timeout 90m ./vm/fleet010-test.sh
#   (recreates both VMs; KEEP=1 keeps them; NO_LXC=1 skips the container)
set -uo pipefail
cd "$(dirname "$0")/.."
DEB=${DEB:-dist/strataos_$(cat VERSION)_amd64.deb}
ADMIN_USER=admin ADMIN_PASS=Strata-Test-2026
LAN=${FLEET_LAN:-230.0.0.10:12370}
T=$(mktemp -d)
PASSES=0 FAILS=0
START=$(date +%s)
ok() { PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1"; }
ko() { FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    %s\n' "$1" "$2"; }
check() { case "$2" in *"$3"*) ok "$1" ;; *) ko "$1" "expected '$3', got: ${2:0:600}" ;; esac; }
section() { printf '\n\033[1;36m== %s  (%d min)\033[0m\n' "$1" $(( ($(date +%s) - START) / 60 )); }
A() { VM_STATE=$PWD/vm/state-fa VM_HOSTNAME=fa VM_WEB_PORT=${FA_WEB:-7250} VM_SSH_PORT=${FA_SSH:-2352} VM_HTTPS_PORT=${FA_HTTPS:-7553} VM_SMB_PORT=${FA_SMB:-4550} SEED_PORT=${FA_SEED:-8859} \
  VM_EXTRA_PORTS="" VM_MEM=2048 VM_CPUS=2 VM_DISKS=1 VM_DISK_SIZE=8G VM_LAN=$LAN ./vm/vm.sh "$@" | tr -d '\r'; }
B() { VM_STATE=$PWD/vm/state-fb VM_HOSTNAME=fb VM_WEB_PORT=${FB_WEB:-7260} VM_SSH_PORT=${FB_SSH:-2362} VM_HTTPS_PORT=${FB_HTTPS:-7563} VM_SMB_PORT=${FB_SMB:-4560} SEED_PORT=${FB_SEED:-8869} \
  VM_EXTRA_PORTS="" VM_MEM=2048 VM_CPUS=2 VM_DISKS=1 VM_DISK_SIZE=8G VM_LAN=$LAN ./vm/vm.sh "$@" | tr -d '\r'; }
PA=${FA_WEB:-7250} PB=${FB_WEB:-7260}
api() { local port=$1 m=$2 p=$3; shift 3; curl -s -m 120 -b "$T/ck$port" -c "$T/ck$port" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "http://localhost:$port$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
login() { api "$1" POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null; }
job() { local port=$1 id; id=$(echo "$2" | py 'print(d.get("id",""))' 2>/dev/null); [ -n "$id" ] || { echo "no-job: $2"; return; }
  for _ in $(seq 1 ${3:-400}); do [ "$(api "$port" GET "/api/jobs/$id" | py 'print(d["status"])' 2>/dev/null)" = running ] || break; sleep 3; done
  api "$port" GET "/api/jobs/$id" | py 'print(d["status"], d.get("error","")); print(d.get("log","")[-2000:]) if d["status"]!="success" else None'; }
# Waits for the end of a transfer of the console (A) and prints its status and error.
xfer() { local id; id=$(echo "$1" | py 'print(d.get("id",""))' 2>/dev/null); [ -n "$id" ] || { echo "no-transfer: $1"; return; }
  for _ in $(seq 1 ${2:-300}); do [ "$(api $PA GET /api/fleet/transfers | py "print([t['status'] for t in d if t['id']=='$id'][0])")" = running ] || break; sleep 3; done
  api $PA GET /api/fleet/transfers | py "t=[t for t in d if t['id']=='$id'][0]; print(t['status'], t.get('error',''), t['stage'], t['total'])"; }
virsh() { local who=$1; shift; $who ssh "sudo virsh -c qemu:///system $*" 2>/dev/null; }
staging() { $1 ssh 'sudo find /srv/strata -maxdepth 3 -path "*/.strata-fleet/*" -type d 2>/dev/null | wc -l'; }
cleanup_vms() { [ -n "${KEEP:-}" ] && return; A destroy >/dev/null 2>&1; B destroy >/dev/null 2>&1; }
diag() {
  [ "$FAILS" -eq 0 ] && return
  for w in A B; do printf '\n--- %s ---\n' "$w"; $w ssh 'sudo journalctl -u strata --no-pager -n 40 -o cat; free -m' 2>&1; done
}
trap 'diag; cleanup_vms; exit 1' INT TERM

section "Two NAS on a shared LAN"
A destroy >/dev/null 2>&1; B destroy >/dev/null 2>&1
(A create >"$T/ca.log" 2>&1 && A deploy "$DEB" >>"$T/ca.log" 2>&1; echo $? >"$T/ca.rc") &
(B create >"$T/cb.log" 2>&1 && B deploy "$DEB" >>"$T/cb.log" 2>&1; echo $? >"$T/cb.rc") &
wait
for w in a b; do [ "$(cat "$T/c$w.rc")" = 0 ] || { tail -20 "$T/c$w.log"; cleanup_vms; exit 1; }; done
A ssh 'sudo ip link set enp0s5 up && sudo ip addr add 10.96.0.1/24 dev enp0s5'
B ssh 'sudo ip link set enp0s5 up && sudo ip addr add 10.96.0.2/24 dev enp0s5'
check "LAN between the NAS" "$(B ssh 'ping -c1 -W2 10.96.0.1 >/dev/null && echo up')" "up"
for p in $PA $PB; do
  api $p POST /api/setup -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"fullName\":\"Test\",\"hostname\":\"nas-$p\"}" >/dev/null
  login $p
  D=$(api $p GET /api/storage/disks | py 'print([x["path"] for x in d if x["usage"]=="free"][0])')
  check "volume on $p" "$(job $p "$(api $p POST /api/storage/volumes -d "{\"name\":\"volume1\",\"fsType\":\"btrfs\",\"devices\":[\"$D\"],\"dataProfile\":\"single\"}")")" "success"
done
for c in vms lxc; do
  [ "$c" = lxc ] && [ -n "${NO_LXC:-}" ] && continue
  for p in $PA $PB; do echo "$(api $p POST /api/system/components/$c/install)" >"$T/inst$p"; done
  for p in $PA $PB; do check "$c component on $p" "$(job $p "$(cat "$T/inst$p")" 600)" "success"; done
done
[ "$FAILS" -eq 0 ] || { diag; cleanup_vms; exit 1; }

section "Console and permissions"
CODE=$(api $PB POST /api/fleet/code | py 'print(d["code"])')
check "B added to the console of A" "$(api $PA POST /api/fleet -d "{\"host\":\"10.96.0.2\",\"code\":\"$CODE\"}")" '"name":"nas-7260"'
MB=$(api $PA GET /api/fleet | py 'print([r["id"] for r in d if r["id"]!="local"][0])')
MGR=$(api $PB GET /api/fleet/managers | py 'print(d[0]["id"])')
check "VM copies off by default" "$(api $PB GET /api/fleet/managers | py 'print(d[0].get("allowVms", False))')" "False"
check "transfers refused without the permission" "$(api $PA GET "/api/fleet/xfer/inventory?nas=$MB")" "does not allow the console to copy"
check "wrong token refused" "$(A ssh "curl -sk -H 'Authorization: Bearer nope' https://10.96.0.2:7443/api/fleet/peer/health")" "authentication required"
check "chunk route needs the token" "$(A ssh "curl -sk -H 'Authorization: Bearer nope' 'https://10.96.0.2:7443/api/fleet/peer/xfer-chunk/0123456789abcdef?file=x&off=0'")" "authentication required"

section "Fleet health dashboard"
H=$(api $PA GET /api/fleet/health)
check "two NAS, both online" "$(echo "$H" | py 'print(d["totals"]["total"], d["totals"]["online"])')" "2 2"
check "health of B reported" "$(echo "$H" | py 'r=[r for r in d["rows"] if r["id"]!="local"][0]; h=r["health"]; print(r["online"], h["hostname"], h["version"]!="", h["disks"]["total"]>0, len(h["space"]["volumes"]), "backups" in h, "temps" in h, h["access"]["vms"])')" "True nas-7260 True True 1 True True False"
check "local row first" "$(echo "$H" | py 'print(d["rows"][0]["id"], d["rows"][0]["name"])')" "local nas-7250"
check "levels and reasons" "$(echo "$H" | py 'print(all(r["level"] in ("ok","info","warning","critical") and isinstance(r["reasons"], list) for r in d["rows"]))')" "True"
# B's volume almost full: B needs attention.
B ssh 'sudo fallocate -l $(( $(df -B1 --output=avail /srv/strata/volume1 | tail -1) * 98 / 100 )) /srv/strata/volume1/filler'
check "B's volume filled" "$(B ssh 'df --output=pcent /srv/strata/volume1 | tail -1')" "%"
sleep 6 # a NAS reuses a report of the last 5 s, even for Refresh
H=$(api $PA POST /api/fleet/health/refresh)
check "full volume on B: needs attention" "$(echo "$H" | py 'r=[r for r in d["rows"] if r["id"]!="local"][0]; print(r["level"], [x["text"] for x in r["reasons"] if x["kind"]=="space"], d["totals"]["attention"])')" "warning ['Volume volume1 is"
check "fleet summary" "$(echo "$H" | py 'print(d["totals"]["attention"] >= 1, d["refreshed"] != "")')" "True True"
B ssh 'sudo rm -f /srv/strata/volume1/filler'
check "cached between calls" "$(api $PA GET /api/fleet/health | py 'print(d["refreshed"])')" "$(echo "$H" | py 'print(d["refreshed"])')"
check "peer health answers with the token" "$(api $PA POST /api/fleet/health/refresh | py 'r=[r for r in d["rows"] if r["id"]!="local"][0]; print(r["online"], r["level"])')" "True"

section "VM copied A → B, bridge mapped, started there"
check "permission given on B" "$(api $PB PUT /api/fleet/managers/$MGR -d '{"allowUpdates":false,"allowReboot":false,"allowVms":true}')" '"ok":true'
check "older clients keep it" "$(api $PB PUT /api/fleet/managers/$MGR -d '{"allowUpdates":false,"allowReboot":false}' >/dev/null; api $PB GET /api/fleet/managers | py 'print(d[0].get("allowVms"))')" "True"
A ssh 'sudo ip link add br0 type bridge && sudo ip link set br0 up && sudo mkdir -p /srv/strata/volume1/vms'
B ssh 'sudo ip link add brx type bridge && sudo ip link set brx up'
check "VM on A" "$(job $PA "$(api $PA POST /api/vms -d '{"name":"tvm","os":"other","vcpus":1,"memoryMb":256,"diskGb":1,"storage":"/srv/strata/volume1/vms","network":"br0"}')")" "success"
# tvm has no operating system (it would ignore a shutdown): it stays stopped on A.
api $PA POST /api/vms/tvm/destroy >/dev/null
disk_of() { $1 ssh "sudo virsh -c qemu:///system domblklist $2 --details" | awk '$2=="disk"{print $4; exit}'; }
DISK=$(disk_of A tvm)
A ssh "sudo sh -c 'qemu-io -f qcow2 -c \"write -P 0x5a 0 8M\" -c \"write -P 0xa5 512M 4M\" $DISK >/dev/null'"
SUM_A=$(A ssh "sudo sh -c 'qemu-img convert -U -O raw $DISK /tmp/a.raw && md5sum </tmp/a.raw && rm /tmp/a.raw'")
check "test data written in the VM disk" "$SUM_A" "  -"
check "inventory of B through the link" "$(api $PA GET "/api/fleet/xfer/inventory?nas=$MB" | py 'print(d["name"], d["libvirt"], "brx" in d["bridges"], [v["path"] for v in d["volumes"]])')" "nas-7260 True True ['/srv/strata/volume1']"
PLAN='{"from":"local","to":"'$MB'","kind":"vm","name":"tvm","mode":"stop","storage":"/srv/strata/volume1/vms","start":true}'
check "plan: missing bridge asked" "$(api $PA POST /api/fleet/transfers/plan -d "$PLAN" | py 'print([(i.get("bridge"), i["text"]) for i in d["issues"]])')" "('br0', 'The bridge br0 does not exist on the destination NAS"
check "refused without the mapping" "$(api $PA POST /api/fleet/transfers -d "$PLAN")" "does not exist"
PLAN='{"from":"local","to":"'$MB'","kind":"vm","name":"tvm","mode":"stop","storage":"/srv/strata/volume1/vms","bridges":{"br0":"brx"},"start":true}'
check "plan with the mapping" "$(api $PA POST /api/fleet/transfers/plan -d "$PLAN" | py 'print(d["issues"])')" "[]"
R=$(xfer "$(api $PA POST /api/fleet/transfers -d "$PLAN")")
check "copy A → B" "$R" "success"
check "running on B" "$(virsh B domstate tvm | head -1)" "running"
check "bridge mapped on B" "$(virsh B domiflist tvm)" "brx"
check "new MAC address" "$( [ "$(virsh A domiflist tvm | awk '/br0/{print $5}')" != "$(virsh B domiflist tvm | awk '/brx/{print $5}')" ] && echo different)" "different"
check "source kept on A (copy)" "$(virsh A domstate tvm | head -1)" "shut off"
DISK=$(disk_of B tvm)
SUM_B=$(B ssh "sudo sh -c 'qemu-img convert -U -O raw $DISK /tmp/b.raw && md5sum </tmp/b.raw && rm /tmp/b.raw'")
check "same disk content" "$SUM_B" "$SUM_A"
check "temporary files removed" "$(staging A) $(staging B)" "0 0"
check "jobs on both sides" "$(api $PB GET /api/jobs | py 'print([j["title"] for j in d if j["type"]=="fleet"])')" "Receiving VM tvm from"
check "events on B" "$(api $PB GET /api/system/events | py '[print(e["message"]) for e in d[:20]]')" "received from"

section "VM copied back B → A, live from a snapshot"
check "name taken on A" "$(api $PA POST /api/fleet/transfers/plan -d '{"from":"'$MB'","to":"local","kind":"vm","name":"tvm","mode":"snapshot","storage":"/srv/strata/volume1/vms","bridges":{"brx":"br0"}}' | py 'print([i["text"] for i in d["issues"]])')" "already exists"
R=$(xfer "$(api $PA POST /api/fleet/transfers -d '{"from":"'$MB'","to":"local","kind":"vm","name":"tvm","newName":"tvm2","mode":"snapshot","storage":"/srv/strata/volume1/vms","bridges":{"brx":"br0"}}')")
check "live copy B → A" "$R" "success"
check "the source kept running on B" "$(virsh B domstate tvm | head -1)" "running"
check "copy defined on A, stopped" "$(virsh A domstate tvm2 | head -1)" "shut off"
check "no snapshot left on B" "$(B ssh 'ls /srv/strata/volume1/vms/tvm/ | grep -c strata-backup || true')" "0"

section "Cancel and move refused while running"
# tvm on B has no operating system: it never answers the shutdown request, so the
# export waits for it and the transfer can be cancelled meanwhile.
R=$(api $PA POST /api/fleet/transfers -d '{"from":"'$MB'","to":"local","kind":"vm","name":"tvm","newName":"tvm3","mode":"stop","storage":"/srv/strata/volume1/vms","bridges":{"brx":"br0"}}')
ID=$(echo "$R" | py 'print(d["id"])')
sleep 8
check "export job waits on B" "$(api $PB GET /api/jobs | py 'print([j["status"] for j in d if j["title"].startswith("Preparing the copy of VM tvm")])')" "running"
check "cancel" "$(api $PA POST /api/fleet/transfers/$ID/cancel)" '"ok":true'
check "cancelled" "$(xfer "$R" 60)" "cancelled"
sleep 3
check "nothing left on either NAS" "$(staging A) $(staging B) $(virsh A list --all --name | grep -c tvm3 || true)" "0 0 0"
check "source untouched" "$(virsh B domstate tvm | head -1)" "running"
check "move must stop" "$(api $PA POST /api/fleet/transfers -d '{"from":"local","to":"'$MB'","kind":"vm","name":"tvm2","newName":"tvm4","mode":"snapshot","move":true,"storage":"/srv/strata/volume1/vms","bridges":{"br0":"brx"}}')" "A move stops the source first"

if [ -z "${NO_LXC:-}" ]; then
  section "Container moved A → B"
  check "an Alpine container on A" "$(job $PA "$(api $PA POST /api/lxc -d '{"name":"ct","image":{"distro":"alpine","release":"3.22"},"location":"/srv/strata/volume1","backend":"dir","network":{"mode":"bridge","bridge":"br0"}}')")" "success"
  api $PA POST /api/lxc/ct/start >/dev/null
  sleep 5; api $PA POST /api/lxc/ct/exec -d '{"command":["sh","-c","echo kept > /root/mark"]}' >/dev/null
  MAC_A=$(api $PA GET /api/lxc | py 'print([c["network"]["mac"] for c in d["containers"] if c["name"]=="ct"][0])')
  R=$(xfer "$(api $PA POST /api/fleet/transfers -d '{"from":"local","to":"'$MB'","kind":"lxc","name":"ct","mode":"stop","move":true,"storage":"/srv/strata/volume1","bridges":{"br0":"brx"},"start":true}')")
  check "move A → B" "$R" "success"
  check "running on B with its files" "$(api $PB GET /api/lxc | py 'print([(c["name"], c["state"], c["network"]["bridge"]) for c in d["containers"]])') $(api $PB POST /api/lxc/ct/exec -d '{"command":["cat","/root/mark"]}' | py 'print(d["stdout"].strip())')" "[('ct', 'running', 'brx')] kept"
  check "same MAC (moved, not copied)" "$(api $PB GET /api/lxc | py 'print([c["network"]["mac"] for c in d["containers"]][0])')" "$MAC_A"
  check "gone from A" "$(api $PA GET /api/lxc | py 'print(len(d["containers"]))')" "0"
  check "temporary files removed" "$(staging A) $(staging B)" "0 0"
fi

section "History"
check "transfers listed" "$(api $PA GET /api/fleet/transfers | py 'print(sorted(t["status"] for t in d))')" "success"
check "events on A" "$(api $PA GET /api/system/events | py '[print(e["message"]) for e in d[:40]]')" "copied to"

printf '\n%s passed, %s failed in %d min\n' "$PASSES" "$FAILS" $(( ($(date +%s) - START) / 60 ))
diag
cleanup_vms
[ "$FAILS" -eq 0 ]
