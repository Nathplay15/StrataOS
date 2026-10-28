#!/bin/bash
# VM cluster test with three NAS on a shared LAN (QEMU multicast, no root):
#   cl1..3 = vm/state-cl1..3 (10.97.0.1-3 on a bridge br0), 1280 MB of RAM each, one 8 GB data disk.
# Cluster of NAS, Ceph on one disk of each NAS, VM storage on Ceph everywhere, the
# cluster-safe CPU model, a tiny CirrOS VM on RBD (nested KVM) with its address from a
# test DHCP server, the cluster-wide view and its console relayed by another member,
# a live migration cl1 → cl2 while it is pinged, high availability: cl2 killed, the VM
# restarted on another NAS after fencing, cl2 back without running it twice, then
# maintenance of a member. About 30 minutes; needs about 4 GB of free RAM.
# Usage: make deb && ./vm/cluster-test.sh   (recreates the three VMs; KEEP=1 keeps them)
set -uo pipefail
cd "$(dirname "$0")/.."
DEB=${DEB:-dist/strataos_$(cat VERSION)_amd64.deb}
ADMIN_USER=admin ADMIN_PASS=Strata-Test-2026
LAN=${CLUSTER_LAN:-230.0.0.6:12356}
CIRROS=${CIRROS_URL:-https://download.cirros-cloud.net/0.6.2/cirros-0.6.2-x86_64-disk.img}
T=$(mktemp -d)
PASSES=0 FAILS=0
START=$(date +%s)
ok() { PASSES=$((PASSES + 1)); printf '  \033[32m✔\033[0m %s\n' "$1"; }
ko() { FAILS=$((FAILS + 1)); printf '  \033[31m✘ %s\033[0m\n    %s\n' "$1" "$2"; }
check() { case "$2" in *"$3"*) ok "$1" ;; *) ko "$1" "expected '$3', got: ${2:0:600}" ;; esac; }
section() { printf '\n\033[1;36m== %s  (%d min)\033[0m\n' "$1" $(( ($(date +%s) - START) / 60 )); }
# The multicast LAN echoes each frame back to its sender: the bridges must not learn
# addresses on the LAN port, or a VM's address would be learnt on the wrong side.
# Node i: web 7769+i, https 7772+i, ssh 2279+i, smb 4479+i, seed 8779+i.
N() {
  local i=$1; shift
  VM_STATE=$PWD/vm/state-cl$i VM_HOSTNAME=cl$i VM_MEM=${CLUSTER_VM_MEM:-1280} VM_CPUS=2 VM_DISKS=1 VM_DISK_SIZE=8G \
    VM_WEB_PORT=$((7769 + i)) VM_HTTPS_PORT=$((7772 + i)) VM_SSH_PORT=$((2279 + i)) VM_SMB_PORT=$((4479 + i)) SEED_PORT=$((8779 + i)) \
    VM_EXTRA_PORTS="" VM_LAN=$LAN ./vm/vm.sh "$@" | tr -d '\r'
}
port() { echo $((7769 + $1)); }
api() { local p; p=$(port "$1"); local m=$2 path=$3; shift 3; curl -s -m 120 -b "$T/ck$p" -c "$T/ck$p" -X "$m" -H 'X-Strata: 1' -H 'Content-Type: application/json' "http://localhost:$p$path" "$@"; }
py() { python3 -c "import sys,json; d=json.load(sys.stdin); $1"; }
login() { api "$1" POST /api/login -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" >/dev/null; }
job() { local n=$1 id; id=$(echo "$2" | py 'print(d.get("id",""))' 2>/dev/null); [ -n "$id" ] || { echo "no-job: $2"; return; }
  for _ in $(seq 1 ${3:-600}); do [ "$(api "$n" GET "/api/jobs/$id" | py 'print(d["status"])' 2>/dev/null)" = running ] || break; sleep 3; done
  api "$n" GET "/api/jobs/$id" | py 'print(d["status"], d.get("error","")); print(d.get("log","")[-3000:]) if d["status"]!="success" else None'; }
healthy() { api "$1" GET /api/ceph | py 'st=d.get("status") or {}; h=st.get("health",{}); print(h.get("status","?"), st.get("osdsUp"))' 2>/dev/null; }
id_of() { api 1 GET /api/cluster | py "print([m['id'] for m in d['members'] if m['name']=='cl$1'][0])"; }
# Where the test VM is defined and running, as each NAS's libvirt says.
where() { for i in 1 2 3; do N "$i" status | grep -q running || continue; printf 'cl%s:%s ' "$i" "$(N "$i" ssh 'sudo virsh -c qemu:///system domstate tvm 2>/dev/null | head -1' | tr ' ' _)"; done; }
running_on() { where | tr ' ' '\n' | grep ':running$' | cut -d: -f1 | tr '\n' ' '; }
bridge_lan() { # br0 with the LAN NIC and the test address, at every boot, before Ceph starts
  N "$1" ssh "sudo tee /etc/systemd/system/lan-ip.service >/dev/null <<EOF
[Unit]
Description=Test LAN bridge
BindsTo=sys-subsystem-net-devices-enp0s5.device
After=sys-subsystem-net-devices-enp0s5.device
Before=network-online.target
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'ip link add br0 type bridge 2>/dev/null; ip link set br0 type bridge stp_state 0 forward_delay 0; ip link set enp0s5 master br0; ip link set enp0s5 type bridge_slave learning off; ip link set enp0s5 up; ip link set br0 up; ip addr add 10.97.0.$1/24 dev br0 || true'
[Install]
WantedBy=multi-user.target network-online.target
EOF
sudo systemctl daemon-reload && sudo systemctl enable --now lan-ip.service"
}
cleanup_vms() { [ -n "${KEEP:-}" ] && return; for i in 1 2 3; do N "$i" destroy >/dev/null 2>&1; done; }
diag() {
  [ "$FAILS" -eq 0 ] && return
  for i in 1 2 3; do
    N "$i" status | grep -q running || continue
    printf '\n--- cl%s ---\n' "$i"
    N "$i" ssh 'sudo journalctl -u strata --no-pager -n 40 -o cat; sudo virsh -c qemu:///system list --all; free -m; sudo dmesg | grep -i -E "oom|killed process" | tail -5' 2>&1
  done
}
trap 'diag; cleanup_vms; exit 1' INT TERM

section "Three VMs on a shared LAN"
for i in 1 2 3; do N "$i" destroy >/dev/null 2>&1; done
for i in 1 2 3; do (N "$i" create >"$T/create$i.log" 2>&1 && N "$i" deploy "$DEB" >>"$T/create$i.log" 2>&1; echo $? >"$T/create$i.rc") & done
wait
for i in 1 2 3; do [ "$(cat "$T/create$i.rc")" = 0 ] || { tail -20 "$T/create$i.log"; cleanup_vms; exit 1; }; done
for i in 1 2 3; do bridge_lan "$i"; done
check "LAN between the VMs" "$(N 3 ssh 'ping -c1 -W2 10.97.0.1 >/dev/null && ping -c1 -W2 10.97.0.2 >/dev/null && echo up')" "up"
check "nested KVM" "$(N 1 ssh 'test -e /dev/kvm && echo kvm')" "kvm"
for i in 1 2 3; do
  api "$i" POST /api/setup -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"fullName\":\"Test\",\"hostname\":\"cl$i\"}" >/dev/null
  login "$i"
done
for c in ceph vms; do
  for i in 1 2 3; do echo "$(api "$i" POST /api/system/components/$c/install)" >"$T/inst$i"; done
  for i in 1 2 3; do check "$c component on cl$i" "$(job "$i" "$(cat "$T/inst$i")")" "success"; done
done
[ "$FAILS" -eq 0 ] || { diag; cleanup_vms; exit 1; }

section "Cluster of NAS and Ceph"
api 1 POST /api/cluster -d '{"name":"Lab","publicNet":"10.97.0.0/24"}' >/dev/null
for i in 2 3; do
  CODE=$(api "$i" POST /api/cluster/prepare | py 'print(d["code"])')
  check "cl$i joins" "$(api 1 POST /api/cluster/members -d "{\"host\":\"10.97.0.$i\",\"code\":\"$CODE\"}")" "\"name\":\"cl$i\""
done
DISKS=$(api 1 GET /api/ceph/disks | py '
out={}
for r in d:
  ok=[x for x in r["disks"] if x["eligible"]]
  out[r["member"]]=[ok[0]["path"]] if ok else []
print(json.dumps(out))')
check "Ceph bootstrap" "$(job 1 "$(api 1 POST /api/ceph/bootstrap -d "{\"disks\":$DISKS,\"rbd\":true,\"cephfs\":false}")" 400)" "success"
[ "$FAILS" -eq 0 ] || { diag; cleanup_vms; exit 1; }
for _ in $(seq 1 60); do case "$(healthy 1)" in HEALTH_OK*|"HEALTH_WARN 3") break ;; esac; sleep 10; done
check "3 OSDs up" "$(healthy 1)" " 3"
for i in 1 2 3; do check "VM storage on Ceph on cl$i" "$(api "$i" POST /api/ceph/vm-pools -d '{"source":"local","pool":"rbd"}')" '"name":"ceph-local-rbd"'; done
M1=$(id_of 1) M2=$(id_of 2) M3=$(id_of 3)

section "VM cluster"
check "every member online, quorum" "$(api 2 GET /api/vmcluster | py 'print(d["quorum"], sum(1 for m in d["members"] if m["online"]), d["haPossible"])')" "True 3 True"
check "a leader" "$(api 3 GET /api/vmcluster | py 'print(sum(1 for m in d["members"] if m["leader"]))')" "1"
check "cluster-safe CPU model" "$(api 1 PUT /api/vmcluster/settings -d '{"cpuModel":"auto"}' | py 'print(d["cpuModel"] != "")')" "True"
CPU=$(api 2 GET /api/vmcluster | py 'print(d["cpuModel"])')
echo "  CPU model: $CPU"
check "the same table everywhere" "$(for i in 1 2 3; do api "$i" GET /api/vmcluster | py 'print(d["cpuModel"], end=" ")'; done)" "$CPU $CPU $CPU"
check "VM on Ceph" "$(job 1 "$(api 1 POST /api/vms -d '{"name":"tvm","os":"other","vcpus":1,"memoryMb":256,"diskGb":1,"storage":"rbd:ceph-local-rbd","network":"br0"}')")" "success"
check "created with the cluster CPU model" "$(N 1 ssh 'sudo virsh -c qemu:///system dumpxml tvm | grep -A1 "<cpu"')" "$CPU"
api 1 POST /api/vms/tvm/destroy >/dev/null
# CirrOS on the RBD image (the NAS downloads it).
N 1 ssh "set -e; curl -sfL -o /tmp/cirros.img '$CIRROS'; dev=\$(sudo rbd map rbd/tvm-disk0); sudo qemu-img convert -O raw /tmp/cirros.img \$dev; sync; sudo rbd unmap \$dev; echo written" >"$T/cirros.log" 2>&1
check "CirrOS on the VM disk" "$(tail -1 "$T/cirros.log")" "written"
MAC=$(api 1 GET /api/vms | py 'print([n["mac"] for v in d["vms"] if v["name"]=="tvm" for n in v["nics"]][0])')
N 3 ssh "sudo dnsmasq --interface=br0 --bind-interfaces --port=0 --dhcp-range=10.97.0.100,10.97.0.150,1h --dhcp-host=$MAC,10.97.0.50 --pid-file=/run/test-dnsmasq.pid --log-dhcp"
api 1 POST /api/vms/tvm/start >/dev/null
for _ in $(seq 1 40); do N 3 ssh 'ping -c1 -W2 10.97.0.50 >/dev/null' && break; sleep 5; done
check "the VM boots and answers" "$(N 3 ssh 'ping -c2 -W2 10.97.0.50 >/dev/null && echo up')" "up"

section "Cluster-wide view and remote actions"
check "cl2 sees the VM of cl1" "$(api 2 GET /api/vmcluster | py "print([(v['name'], v['state'], v['storage'], v['cpu']) for m in d['members'] if m['id']=='$M1' for v in m['inventory']['vms']])")" "('tvm', 'running', 'ceph', '$CPU')"
check "console relayed by cl2" "$(curl -s --http1.1 -m 5 -i -N -b "$T/ck$(port 2)" -H 'X-Strata: 1' -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -H 'Sec-WebSocket-Protocol: binary' "http://localhost:$(port 2)/api/vmcluster/nodes/$M1/vms/tvm/vnc" 2>/dev/null | head -c 400 | tr -d '\0' | tr -c '[:print:]\n' '.')" "RFB 003"
check "wrong token refused" "$(N 2 ssh "curl -sk -X POST -H 'X-Strata-Cluster: $(printf 'ab%.0s' $(seq 32))' https://10.97.0.1:7443/api/cluster/peer/vmc/inventory")" "unknown NAS"
check "pre-checks: missing bridge mapped" "$(api 2 POST /api/vmcluster/migrate/check -d "{\"node\":\"$M1\",\"name\":\"tvm\",\"target\":\"$M2\",\"bridges\":{\"br0\":\"br9\"}}")" "no network bridge br9"
check "pre-checks pass" "$(api 2 POST /api/vmcluster/migrate/check -d "{\"node\":\"$M1\",\"name\":\"tvm\",\"target\":\"$M2\"}" | py 'print(d["live"], d.get("problems"), d["copyStorage"])')" "True None False"

section "Live migration while pinging"
N 3 ssh 'nohup ping -i 0.2 -W1 10.97.0.50 >/tmp/ping.log 2>&1 & echo $! >/tmp/ping.pid'
T0=$(date +%s)
check "live migration cl1 → cl2 (asked on cl3)" "$(job 3 "$(api 3 POST /api/vmcluster/migrate -d "{\"node\":\"$M1\",\"name\":\"tvm\",\"target\":\"$M2\"}")" 300)" "success"
echo "  migration took $(( $(date +%s) - T0 )) s"
sleep 5
N 3 ssh 'kill -INT $(cat /tmp/ping.pid); sleep 1; tail -3 /tmp/ping.log' >"$T/ping.txt"
LOSS=$(grep -o '[0-9.]*% packet loss' "$T/ping.txt" | cut -d% -f1)
echo "  ping: $(grep 'packet loss' "$T/ping.txt")"
check "the VM kept answering (loss < 5 %)" "$(python3 -c "print('ok' if float('${LOSS:-100}') < 5 else 'loss ${LOSS:-?}%')")" "ok"
check "runs on cl2 only" "$(running_on)" "cl2 "
check "no longer defined on cl1" "$(N 1 ssh 'sudo virsh -c qemu:///system list --all --name | grep -c tvm || true')" "0"
check "traffic was encrypted (TLS on 16514)" "$(N 2 ssh 'sudo ss -ltn | grep -c ":16514" ; sudo test -f /etc/pki/qemu/server-cert.pem && echo certs')" "certs"

section "High availability"
check "HA refused for an unknown VM" "$(api 1 PUT /api/vmcluster/ha/nothere -d "{\"node\":\"$M2\",\"on\":true}")" "not found"
check "HA on (preferred cl3)" "$(api 1 PUT /api/vmcluster/ha/tvm -d "{\"node\":\"$M2\",\"on\":true,\"preferred\":\"$M3\"}")" '"ok":true'
sleep 12
check "HA table everywhere" "$(for i in 1 2 3; do api "$i" GET /api/vmcluster | py 'print([(h["name"], h["running"]) for h in d["ha"]], end=" ")'; done)" "[('tvm', True)] [('tvm', True)] [('tvm', True)]"
N 2 ssh 'sync' >/dev/null
kill -9 "$(cat vm/state-cl2/qemu.pid)"
T0=$(date +%s)
for _ in $(seq 1 60); do [ -n "$(running_on)" ] && break; sleep 5; done
echo "  restarted after $(( $(date +%s) - T0 )) s on: $(running_on)"
check "VM restarted on its preferred NAS" "$(running_on)" "cl3 "
EV=$(api 1 GET /api/system/events | py '[print(e["message"]) for e in d[:60] if e["category"]=="vms"]'; api 3 GET /api/system/events | py '[print(e["message"]) for e in d[:60] if e["category"]=="vms"]')
check "fenced first" "$EV" "Fencing cl2 for VM tvm"
echo "  $(echo "$EV" | grep -m1 Fencing); blocklist: $(N 1 ssh 'sudo ceph osd blocklist ls 2>&1' | tr '\n' ' ')"
for _ in $(seq 1 30); do N 3 ssh 'ping -c1 -W2 10.97.0.50 >/dev/null' && break; sleep 5; done
check "answers again" "$(N 3 ssh 'ping -c2 -W2 10.97.0.50 >/dev/null && echo up')" "up"
check "owner changed" "$(api 1 GET /api/vmcluster | py "print([h['owner']=='$M3' for h in d['ha']])")" "[True]"

section "The failed NAS comes back"
N 2 start >/dev/null
for _ in $(seq 1 60); do curl -sf "http://localhost:$(port 2)/api/setup" >/dev/null && break; sleep 3; done
login 2
for _ in $(seq 1 30); do [ "$(N 2 ssh 'sudo virsh -c qemu:///system list --all --name | grep -c tvm || true')" = 0 ] && break; sleep 10; done
check "its old copy removed, not started" "$(N 2 ssh 'sudo virsh -c qemu:///system list --all --name | grep -c tvm || true')" "0"
check "the VM runs once" "$(running_on)" "cl3 "
check "quorum of 3 again" "$(api 2 GET /api/vmcluster | py 'print(d["quorum"], sum(1 for m in d["members"] if m["online"]))')" "True 3"

section "Maintenance"
check "cl3 in maintenance: its VM moves away" "$(job 1 "$(api 1 POST /api/vmcluster/nodes/$M3/maintenance -d '{"on":true}')" 300)" "success"
check "the VM left cl3" "$(running_on)" "cl"
case "$(running_on)" in *cl3*) ko "not on cl3 any more" "$(where)" ;; *) ok "not on cl3 any more" ;; esac
check "no VM placed on a member in maintenance" "$(api 1 POST /api/vmcluster/migrate/check -d "{\"node\":\"$(api 1 GET /api/vmcluster | py "print([h['owner'] for h in d['ha']][0])")\",\"name\":\"tvm\",\"target\":\"$M3\"}")" "maintenance"
check "end of maintenance" "$(job 1 "$(api 1 POST /api/vmcluster/nodes/$M3/maintenance -d '{"on":false}')")" "success"
check "VM still answering" "$(N 1 ssh 'ping -c2 -W2 10.97.0.50 >/dev/null && echo up')" "up"
check "HA off" "$(api 1 PUT /api/vmcluster/ha/tvm -d '{"on":false}')" '"ok":true'

section "Containers move between NAS"
if [ -z "${NO_LXC:-}" ]; then
  for i in 1 2; do
    check "containers (LXC) on cl$i" "$(job "$i" "$(api "$i" POST /api/system/components/lxc/install)")" "success"
    check "a volume for containers on cl$i" "$(job "$i" "$(api "$i" POST /api/ceph/volumes -d "{\"name\":\"ct$i\",\"source\":\"local\",\"kind\":\"rbd\",\"pool\":\"rbd\",\"image\":\"ct$i\",\"create\":true,\"sizeGb\":3,\"fsType\":\"ext4\",\"confirm\":true}")")" "success"
  done
  check "an Alpine container on cl1" "$(job 1 "$(api 1 POST /api/lxc -d '{"name":"ct","image":{"distro":"alpine","release":"3.22"},"location":"/srv/strata/ct1","backend":"dir","network":{"mode":"nat"}}')")" "success"
  api 1 POST /api/lxc/ct/start >/dev/null
  sleep 5; api 1 POST /api/lxc/ct/exec -d '{"command":["sh","-c","echo kept > /root/mark"]}' >/dev/null
  check "moved to cl2 (asked on cl3)" "$(job 3 "$(api 3 POST "/api/vmcluster/nodes/$M1/lxc/ct/migrate" -d "{\"target\":\"$M2\",\"location\":\"/srv/strata/ct2\"}")")" "success"
  check "running on cl2 with its files" "$(api 2 GET /api/lxc | py 'print([(c["name"], c["state"]) for c in d["containers"]])') $(api 2 POST /api/lxc/ct/exec -d '{"command":["cat","/root/mark"]}' | py 'print(d["stdout"].strip())')" "[('ct', 'running')] kept"
  check "gone from cl1" "$(api 1 GET /api/lxc | py 'print(len(d["containers"]))')" "0"
fi

printf '\n%s passed, %s failed in %d min\n' "$PASSES" "$FAILS" $(( ($(date +%s) - START) / 60 ))
diag
cleanup_vms
[ "$FAILS" -eq 0 ]
