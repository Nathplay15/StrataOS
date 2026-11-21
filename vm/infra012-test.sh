#!/bin/bash
# StrataOS 0.12 infrastructure test with two VMs on a shared LAN (QEMU multicast, no root):
#   A = vm/state-ia (10.97.0.1), B = vm/state-ib (10.97.0.2), 2 GB of RAM each.
#   - incremental VM backups on A: a running VM (no OS, written with qemu-io) backed up
#     in full then incrementally (fewer bytes read), restored identical; full read again
#     after QEMU was killed (broken bitmaps) and after a disk resize;
#   - S3 on A: bucket versioning and object lock (governance, compliance, legal hold)
#     with the aws command;
#   - a VXLAN network declared on A, sent to B over the Multi-NAS console link
#     (permission on B), traffic from a namespace on B to A, then encrypted
#     (WireGuard), then removed everywhere.
# About 25 minutes. Usage:
#   make deb && flock -o /home/nathan/Documents/StrataOS-wt/.vm.lock timeout 90m ./vm/infra012-test.sh
#   (recreates both VMs; KEEP=1 keeps them; ONLY="backup s3 vxlan" picks sections)
# Ports: IA_WEB/IA_SSH/… and IB_… (defaults below).
set -uo pipefail
cd "$(dirname "$0")/.."
DEB=${DEB:-dist/strataos_$(cat VERSION)_amd64.deb}
ADMIN_USER=admin ADMIN_PASS=Strata-Test-2026
LAN=${INFRA_LAN:-230.0.0.12:12390}
ONLY=${ONLY:-backup s3 vxlan}
T=$(mktemp -d)
PASSES=0 FAILS=0
START=$(date +%s)
ok() { PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1"; }
ko() { FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    %s\n' "$1" "$2"; }
check() { case "$2" in *"$3"*) ok "$1" ;; *) ko "$1" "expected '$3', got: ${2:0:600}" ;; esac; }
section() { printf '\n\033[1;36m== %s  (%d min)\033[0m\n' "$1" $(( ($(date +%s) - START) / 60 )); }
want() { case " $ONLY " in *" $1 "*) return 0 ;; esac; return 1; }
A() { VM_STATE=$PWD/vm/state-ia VM_HOSTNAME=ia VM_WEB_PORT=${IA_WEB:-7420} VM_SSH_PORT=${IA_SSH:-2522} VM_HTTPS_PORT=${IA_HTTPS:-7723} VM_SMB_PORT=${IA_SMB:-4720} SEED_PORT=${IA_SEED:-9029} \
  VM_EXTRA_PORTS="" VM_MEM=2048 VM_CPUS=2 VM_DISKS=1 VM_DISK_SIZE=12G VM_LAN=$LAN ./vm/vm.sh "$@" | tr -d '\r'; }
B() { VM_STATE=$PWD/vm/state-ib VM_HOSTNAME=ib VM_WEB_PORT=${IB_WEB:-7430} VM_SSH_PORT=${IB_SSH:-2532} VM_HTTPS_PORT=${IB_HTTPS:-7733} VM_SMB_PORT=${IB_SMB:-4730} SEED_PORT=${IB_SEED:-9039} \
  VM_EXTRA_PORTS="" VM_MEM=2048 VM_CPUS=2 VM_DISKS=1 VM_DISK_SIZE=8G VM_LAN=$LAN ./vm/vm.sh "$@" | tr -d '\r'; }
PA=${IA_WEB:-7420} PB=${IB_WEB:-7430}
api() { local port=$1 m=$2 p=$3; shift 3; curl -s -m 120 -b "$T/ck$port" -c "$T/ck$port" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "http://localhost:$port$p" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
login() { api "$1" POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null; }
job() { local port=$1 id; id=$(echo "$2" | py 'print(d.get("id",""))' 2>/dev/null); [ -n "$id" ] || { echo "no-job: $2"; return; }
  for _ in $(seq 1 ${3:-400}); do [ "$(api "$port" GET "/api/jobs/$id" | py 'print(d["status"])' 2>/dev/null)" = running ] || break; sleep 3; done
  api "$port" GET "/api/jobs/$id" | py 'print(d["status"], d.get("error","")); print(d.get("log","")[-2500:])'; }
virsh() { A ssh "sudo virsh -c qemu:///system $*" 2>/dev/null; }
# Network change on trial, confirmed once the links settled.
trial() { local port=$1 out tok; out=$(cat); tok=$(echo "$out" | py 'print(d.get("token",""))' 2>/dev/null)
  [ -n "$tok" ] || { echo "no-trial: $out"; return; }
  for _ in $(seq 1 30); do r=$(api "$port" POST /api/system/network/confirm -d "{\"token\":\"$tok\"}"); case "$r" in *'"ok":true'*) echo confirmed; return ;; esac; sleep 1; done; echo "not confirmed: $r"; }
cleanup_vms() { [ -n "${KEEP:-}" ] && return; A destroy >/dev/null 2>&1; B destroy >/dev/null 2>&1; }
diag() {
  [ "$FAILS" -eq 0 ] && return
  for w in A B; do printf '\n--- %s ---\n' "$w"; $w ssh 'sudo journalctl -u strata --no-pager -n 40 -o cat; free -m' 2>&1 | tail -50; done
}
trap 'diag; cleanup_vms; exit 1' INT TERM

section "Two NAS on a shared LAN"
A destroy >/dev/null 2>&1; B destroy >/dev/null 2>&1
(A create >"$T/ca.log" 2>&1 && A deploy "$DEB" >>"$T/ca.log" 2>&1; echo $? >"$T/ca.rc") &
(B create >"$T/cb.log" 2>&1 && B deploy "$DEB" >>"$T/cb.log" 2>&1; echo $? >"$T/cb.rc") &
wait
for w in a b; do [ "$(cat "$T/c$w.rc")" = 0 ] || { tail -20 "$T/c$w.log"; cleanup_vms; exit 1; }; done
A ssh 'sudo ip link set enp0s5 up && sudo ip addr add 10.97.0.1/24 dev enp0s5'
B ssh 'sudo ip link set enp0s5 up && sudo ip addr add 10.97.0.2/24 dev enp0s5'
check "LAN between the NAS" "$(B ssh 'ping -c1 -W2 10.97.0.1 >/dev/null && echo up')" "up"
for p in $PA $PB; do
  api $p POST /api/setup -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"fullName\":\"Test\",\"hostname\":\"nas-$p\"}" >/dev/null
  login $p
  D=$(api $p GET /api/storage/disks | py 'print([x["path"] for x in d if x["usage"]=="free"][0])')
  check "volume on $p" "$(job $p "$(api $p POST /api/storage/volumes -d "{\"name\":\"volume1\",\"fsType\":\"btrfs\",\"devices\":[\"$D\"],\"dataProfile\":\"single\"}")")" "success"
done
if want backup; then
  for c in vms restic; do
    [ "$(api $PA GET /api/system/components | py "print(d.get('$c'))")" = True ] || check "$c component" "$(job $PA "$(api $PA POST /api/system/components/$c/install)" 600)" "success"
  done
fi

if want backup; then
section "Incremental VM backups (dirty bitmaps)"
A ssh 'sudo mkdir -p /srv/strata/volume1/vms'
check "VM on A" "$(job $PA "$(api $PA POST /api/vms -d '{"name":"inc","os":"other","vcpus":1,"memoryMb":256,"diskGb":1,"storage":"/srv/strata/volume1/vms","network":"nat"}')")" "success"
check "VM running (no system: it never writes)" "$(virsh domstate inc)" "running"
qio() { virsh "qemu-monitor-command inc --hmp 'qemu-io -d /machine/peripheral/virtio-disk0/virtio-backend \"$1\"'"; }
qio "write -P 0x11 0 32M" >/dev/null
DS=$(api $PA POST /api/datastores -d '{"name":"infra","destType":"local","destPath":"/srv/strata/volume1/ds","password":"Infra-Test-012","verify":{}}' | py 'print(d["id"])')
TK=$(api $PA POST /api/backups -d '{"name":"inc images","kind":"restic","sources":[],"vms":["inc"],"destType":"datastore","datastore":"'$DS'","keepLast":10,"cron":"","enabled":true}' | py 'print(d["id"])')
hist() { api $PA GET /api/backups | py "print([b['history'][0]['message'] for b in d if b['id']=='$TK'][0])"; }
R1=$(job $PA "$(api $PA POST /api/backups/$TK/run)")
check "first backup (full, live)" "$R1" "success"
check "read in full the first time" "$R1" "read in full"
check "no incremental the first time" "$(hist | grep -c incremental)" "0"
check "checkpoint of the task" "$(virsh checkpoint-list inc --name)" "strata-$TK-"
qio "write -P 0x22 100M 4M" >/dev/null
qio "write -P 0x33 700M 64k" >/dev/null
R2=$(job $PA "$(api $PA POST /api/backups/$TK/run)")
check "second backup" "$R2" "success"
check "only the changed blocks read" "$R2" "changed since the last backup"
H2=$(hist)
check "job tells the incremental read" "$H2" "incremental:"
READ=$(echo "$H2" | python3 -c "import re,sys; m=re.search(r'incremental: ([0-9.]+) (B|KB|MB|GB)', sys.stdin.read()); f={'B':1,'KB':1e3,'MB':1e6,'GB':1e9}; print(int(float(m.group(1))*f[m.group(2)]) if m else -1)")
check "fewer bytes read than the full disk (read $READ)" "$( [ "$READ" -gt 0 ] && [ "$READ" -lt 64000000 ] && echo fewer)" "fewer"
check "one checkpoint kept for the task" "$(virsh checkpoint-list inc --name | grep -c "strata-$TK-")" "1"
SNAP=$(api $PA GET /api/datastores/$DS/contents | py "print([i['id'] for i in d['items'] if i['name']=='inc'][0])")
virsh destroy inc >/dev/null
check "restored from the incremental backup" "$(job $PA "$(api $PA POST /api/datastores/$DS/restore -d "{\"snapshot\":\"$SNAP\",\"name\":\"inc2\",\"storage\":\"/srv/strata/volume1/vms\"}")")" "success"
check "restored disk identical to the VM's" "$(A ssh 'sudo qemu-img compare -U -q /srv/strata/volume1/vms/inc/disk0.qcow2 /srv/strata/volume1/vms/inc2/disk0.qcow2 && echo same')" "same"
# QEMU killed: its bitmaps are inconsistent, libvirt refuses them.
api $PA POST /api/vms/inc/start >/dev/null; sleep 3
qio "write -P 0x44 200M 1M" >/dev/null
A ssh 'sudo kill -9 $(cat /run/libvirt/qemu/inc.pid)'; sleep 3
api $PA POST /api/vms/inc/start >/dev/null; sleep 3
R3=$(job $PA "$(api $PA POST /api/backups/$TK/run)")
check "after a crash: full read, success" "$R3" "success"
check "fallback logged" "$R3" "read in full"
qio "write -P 0x55 300M 1M" >/dev/null
check "incremental again after the fallback" "$(job $PA "$(api $PA POST /api/backups/$TK/run)" >/dev/null; hist)" "incremental:"
virsh "blockresize inc vda 2G" >/dev/null
R5=$(job $PA "$(api $PA POST /api/backups/$TK/run)")
check "resized disk read in full" "$R5" "read in full"
check "resized disk backed up" "$R5" "success"
api $PA POST /api/vms/inc/destroy >/dev/null; sleep 2
check "stopped VM: classic full read keeps the checkpoint" "$(job $PA "$(api $PA POST /api/backups/$TK/run)" | head -1; virsh checkpoint-list inc --name | grep -c "strata-$TK-")" "success"
api $PA DELETE /api/backups/$TK >/dev/null
for _ in 1 2 3 4 5 6; do [ "$(virsh checkpoint-list inc --name | grep -c "strata-$TK-")" = 0 ] && break; sleep 5; done
check "task deleted: its checkpoints removed" "$(virsh checkpoint-list inc --name | grep -c "strata-$TK-")" "0"
check "VM deleted with its checkpoints" "$(api $PA DELETE "/api/vms/inc2?disks=1"; api $PA DELETE "/api/vms/inc?disks=1"; virsh list --all --name | grep -c '^inc')" "0"
fi

if want s3; then
section "S3: versioning and object lock"
check "S3 component" "$(job $PA "$(api $PA POST /api/system/components/s3/install)" 300)" "success"
A ssh 'sudo mkdir -p /srv/strata/volume1/Docs /srv/strata/volume1/Vault'
api $PA POST /api/shares -d '{"name":"Docs","path":"/srv/strata/volume1/Docs","smb":true,"permissions":{"admin":"rw"}}' >/dev/null
api $PA POST /api/shares -d '{"name":"Vault","path":"/srv/strata/volume1/Vault","smb":true,"permissions":{"admin":"rw"}}' >/dev/null
check "lock without versioning refused" "$(api $PA PUT /api/s3 -d '{"enabled":true,"port":7380,"buckets":[{"name":"docs","share":"Docs","lock":true}]}')" "needs versioning"
check "versioning on docs, compliance lock on vault" "$(api $PA PUT /api/s3 -d '{"enabled":true,"port":7380,"buckets":[{"name":"docs","share":"Docs","versioning":"enabled","lock":true,"lockMode":"GOVERNANCE","lockDays":1},{"name":"vault","share":"Vault","versioning":"enabled","lock":true,"lockMode":"COMPLIANCE","lockDays":1}]}')" '"ok":true'
check "settings shown" "$(api $PA GET /api/s3 | py 'print(sorted((b["name"], b.get("versioning"), b.get("lockMode"), b.get("writable")) for b in d["buckets"]))')" "('docs', 'enabled', 'GOVERNANCE', True)"
check "versions folder on the volume, root only" "$(A ssh 'sudo stat -c "%a %U" /srv/strata/volume1/.strata-s3-versions; sudo readlink /var/lib/strata/s3/versions/docs')" "700 root"
K=$(api $PA POST /api/me/s3/keys -d '{"name":"infra"}')
AK=$(echo "$K" | py 'print(d["access"])') SK=$(echo "$K" | py 'print(d["secret"])')
A ssh "command -v aws >/dev/null || sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq awscli >/dev/null 2>&1 || sudo pip3 install -q --break-system-packages awscli >/dev/null 2>&1; true"
s3() { A ssh "PYTHONWARNINGS=ignore AWS_ACCESS_KEY_ID=$AK AWS_SECRET_ACCESS_KEY=$SK AWS_DEFAULT_REGION=us-east-1 aws --no-verify-ssl --endpoint-url https://localhost:7380 s3api $* 2>&1"; }
# The gateway's root account (the NAS administrator's side): reads retentions and holds.
s3root() { A ssh "sudo sh -c '. /var/lib/strata/s3/root.env; PYTHONWARNINGS=ignore AWS_ACCESS_KEY_ID=\$ROOT_ACCESS_KEY AWS_SECRET_ACCESS_KEY=\$ROOT_SECRET_KEY AWS_DEFAULT_REGION=us-east-1 aws --no-verify-ssl --endpoint-url https://localhost:7380 s3api $* 2>&1'"; }
A ssh 'echo version-one > /tmp/v1; echo version-two > /tmp/v2'
check "aws CLI present" "$(A ssh 'aws --version 2>&1')" "aws-cli"
s3 put-object --bucket docs --key report.txt --body /tmp/v1 >/dev/null
s3 put-object --bucket docs --key report.txt --body /tmp/v2 >/dev/null
check "two versions" "$(s3 list-object-versions --bucket docs --prefix report.txt --query "'length(Versions)'")" "2"
check "default retention applied" "$(s3root get-object-retention --bucket docs --key report.txt)" "GOVERNANCE"
OLD=$(s3 list-object-versions --bucket docs --prefix report.txt --query "'Versions[?IsLatest==\`false\`].VersionId'" --output text | tr -d '[:space:]')
check "delete leaves a marker, the file stays readable by version" "$(s3 delete-object --bucket docs --key report.txt >/dev/null; s3 get-object --bucket docs --key report.txt --version-id $OLD /tmp/out >/dev/null; A ssh cat /tmp/out)" "version-one"
check "locked version cannot be deleted with the user key" "$(s3 delete-object --bucket docs --key report.txt --version-id $OLD)" "AccessDenied"
check "nor with the governance bypass" "$(s3 delete-object --bucket docs --key report.txt --version-id $OLD --bypass-governance-retention)" "AccessDenied"
s3 put-object --bucket vault --key w.txt --body /tmp/v1 >/dev/null
VW=$(s3 list-object-versions --bucket vault --prefix w.txt --query "'Versions[0].VersionId'" --output text | tr -d '[:space:]')
check "compliance retention" "$(s3root get-object-retention --bucket vault --key w.txt --version-id $VW)" "COMPLIANCE"
check "compliance version kept" "$(s3 delete-object --bucket vault --key w.txt --version-id $VW)" "AccessDenied"
s3 put-object --bucket docs --key hold.txt --body /tmp/v1 >/dev/null
VH=$(s3 list-object-versions --bucket docs --prefix hold.txt --query "'Versions[0].VersionId'" --output text | tr -d '[:space:]')
echo "    (info) legal hold with the user key: $(s3 put-object-legal-hold --bucket docs --key hold.txt --version-id $VH --legal-hold Status=ON | head -2 | tr '\n' ' ')"
check "legal hold set" "$(s3root put-object-legal-hold --bucket docs --key hold.txt --version-id $VH --legal-hold Status=ON; s3root get-object-legal-hold --bucket docs --key hold.txt --version-id $VH)" '"ON"'
check "held version cannot be deleted" "$(s3root delete-object --bucket docs --key hold.txt --version-id $VH --bypass-governance-retention)" "AccessDenied"
check "older versions out of the share" "$(A ssh 'sudo find /srv/strata/volume1/.strata-s3-versions/docs -type f | wc -l; sudo ls /srv/strata/volume1/Docs')" "report.txt"
check "lock cannot be turned off" "$(api $PA PUT /api/s3 -d '{"enabled":true,"port":7380,"buckets":[{"name":"docs","share":"Docs","versioning":"enabled"},{"name":"vault","share":"Vault","versioning":"enabled","lock":true,"lockMode":"COMPLIANCE","lockDays":1}]}')" "cannot be turned off"
check "default retention changed, lock stays" "$(api $PA PUT /api/s3 -d '{"enabled":true,"port":7380,"buckets":[{"name":"docs","share":"Docs","versioning":"enabled","lock":true},{"name":"vault","share":"Vault","versioning":"enabled","lock":true,"lockMode":"COMPLIANCE","lockDays":1}]}' >/dev/null; s3root get-object-lock-configuration --bucket docs)" '"Enabled"'
check "default retention removed, lock kept" "$(s3root get-object-lock-configuration --bucket docs | grep -c DefaultRetention)" "0"
check "removing a locked bucket needs a confirmation" "$(api $PA PUT /api/s3 -d '{"enabled":true,"port":7380,"buckets":[]}')" "confirm"
check "S3 off (with confirmation)" "$(api $PA PUT /api/s3 -d '{"enabled":false,"port":7380,"buckets":[],"confirm":true}')" '"ok":true'
fi

if want vxlan; then
section "Shared VXLAN network A → B"
CODE=$(api $PB POST /api/fleet/code | py 'print(d["code"])')
check "B added to the console of A" "$(api $PA POST /api/fleet -d "{\"host\":\"10.97.0.2\",\"code\":\"$CODE\"}")" '"name":"nas-'$PB'"'
MB=$(api $PA GET /api/fleet | py 'print([r["id"] for r in d if r["id"]!="local"][0])')
MGR=$(api $PB GET /api/fleet/managers | py 'print(d[0]["id"])')
check "B listed as a possible member" "$(api $PA GET /api/system/network/vnets | py 'print([p["key"] for p in d["peers"]])')" "fleet:$MB"
check "vnet lab created on trial" "$(api $PA POST /api/system/network/vnets -d '{"id":"lab","kind":"vxlan","vni":4242,"address":"10.77.0.1/24","members":["fleet:'$MB'"]}' | trial $PA)" "confirmed"
wait_b() { for _ in $(seq 1 70); do v=$(api $PB GET /api/system/network/vnets | py "$1" 2>/dev/null); [ "$v" = "$2" ] && { echo "$v"; return; }; sleep 3; done; echo "timeout: $v"; }
check "refused while B does not allow it" "$(for _ in $(seq 1 30); do s=$(api $PA GET /api/system/network/vnets | py 'n=[n for n in d["networks"] if n["id"]=="lab"][0]; print(n.get("shared"))'); case "$s" in *"does not allow"*) echo "$s"; break ;; esac; sleep 3; done)" "does not allow"
check "permission given on B" "$(api $PB PUT /api/fleet/managers/$MGR -d '{"allowUpdates":false,"allowReboot":false,"allowNetworks":true}')" '"ok":true'
check "lab received by B" "$(wait_b 'v=[n for n in d["networks"] if n["id"]=="lab"]; print(v[0]["vni"], v[0]["subnet"], v[0]["gateway"], v[0]["originKey"].startswith("fleet:"), v[0]["peers"]) if v else print("none")' "4242 10.77.0.0/24 10.77.0.1 True ['10.97.0.1']")" "4242 10.77.0.0/24"
check "A peers with B, B confirmed" "$(for _ in $(seq 1 20); do s=$(api $PA GET /api/system/network/vnets | py 'n=[n for n in d["networks"] if n["id"]=="lab"][0]; print(n["peers"], [(s["ok"], s.get("error", "")) for s in n["shared"]])'); case "$s" in *"(True"*) break ;; esac; sleep 3; done; echo "$s")" "['10.97.0.2'] [(True"
BRB=$(api $PB GET /api/system/network/vnets | py 'print([n["bridge"] for n in d["networks"] if n["id"]=="lab"][0])')
check "managed by A: edit refused on B" "$(api $PB PUT /api/system/network/vnets/lab -d '{"kind":"vxlan","vni":1,"peers":["10.97.0.1"]}')" "managed by the NAS"
B ssh "sudo ip netns add g; sudo ip link add vg type veth peer name vgp; sudo ip link set vgp netns g; sudo ip link set vg master $BRB up; sudo ip -n g link set vgp up; sudo ip -n g addr add 10.77.0.2/24 dev vgp; sudo ip -n g link set vgp mtu \$(cat /sys/class/net/$BRB/mtu)"
check "MTU 1450 on B's bridge" "$(B ssh "cat /sys/class/net/$BRB/mtu")" "1450"
check "traffic over the tunnel B → A" "$(B ssh 'sudo ip netns exec g ping -c3 -W2 10.77.0.1 >/dev/null && echo pong')" "pong"

section "Encrypted (WireGuard)"
check "encryption turned on" "$(api $PA PUT /api/system/network/vnets/lab -d '{"kind":"vxlan","vni":4242,"address":"10.77.0.1/24","members":["fleet:'$MB'"],"encrypt":true}' | trial $PA)" "confirmed"
check "B moved to the mesh" "$(wait_b 'v=[n for n in d["networks"] if n["id"]=="lab"][0]; print(v.get("encrypt"), bool(v.get("wg")), v["status"]["mtu"])' "True True 1350")" "True True 1350"
check "A on the mesh" "$(for _ in $(seq 1 20); do s=$(api $PA GET /api/system/network/vnets | py 'n=[n for n in d["networks"] if n["id"]=="lab"][0]; print(bool(n.get("wg")), n["local"].startswith("fd"), n["peers"][0].startswith("fd"))'); [ "$s" = "True True True" ] && break; sleep 3; done; echo "$s")" "True True True"
B ssh "sudo ip -n g link set vgp mtu 1350"
BRA=$(api $PA GET /api/system/network/vnets | py 'print([n["bridge"] for n in d["networks"] if n["id"]=="lab"][0])')
check "WireGuard link on both" "$(A ssh "/usr/sbin/ip -br link show ${BRA}wg"; B ssh "/usr/sbin/ip -br link show ${BRB}wg")" "${BRB}wg"
check "traffic over the encrypted tunnel" "$(for _ in $(seq 1 10); do B ssh 'sudo ip netns exec g ping -c2 -W2 10.77.0.1 >/dev/null' && { echo pong; break; }; sleep 3; done)" "pong"
check "VXLAN inside WireGuard (packets on A's WireGuard link)" "$(A ssh "/usr/sbin/ip -s -j link show ${BRA}wg" | py 'print("wg" if d[0]["stats64"]["rx"]["packets"] > 0 else "none")')" "wg"
check "big packets pass (MTU)" "$(B ssh 'sudo ip netns exec g ping -c2 -W2 -M do -s 1322 10.77.0.1 >/dev/null && echo fits')" "fits"

section "Removal propagated"
B ssh 'sudo ip netns del g; sudo ip link del vg 2>/dev/null; true'
check "lab removed on A" "$(api $PA DELETE /api/system/network/vnets/lab | trial $PA)" "confirmed"
check "lab removed on B" "$(wait_b 'print(len([n for n in d["networks"] if n["id"]=="lab"]))' 0)" "0"
check "WireGuard key removed on B" "$(B ssh 'sudo ls /etc/strata-vnet-wg/ 2>/dev/null | grep -c lab')" "0"
fi

printf '\n%s passed, %s failed (%d min)\n' "$PASSES" "$FAILS" $(( ($(date +%s) - START) / 60 ))
diag
cleanup_vms
rm -rf "$T"
[ "$FAILS" -eq 0 ]
